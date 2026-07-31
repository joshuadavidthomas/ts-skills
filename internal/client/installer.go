package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

var (
	errBusy           = errors.New("project is being modified by another ts-skills process")
	errLocalChanges   = errors.New("installed skill differs from the project lock")
	errProjectChanged = errors.New("project changed during restore")
)

type installer struct{ remote *remote }

func (i *installer) install(ctx context.Context, project project, requirement requirement) (locked lockedSkill, err error) {
	if err := project.validate(); err != nil {
		return lockedSkill{}, err
	}
	fetched, err := i.remote.fetch(ctx, requirement)
	if err != nil {
		return lockedSkill{}, fmt.Errorf("fetch skill %s: %w", requirement.skillID().String(), err)
	}
	defer func() { err = errors.Join(err, closeFetchedTree(fetched)) }()
	writer, err := project.acquireWriter(ctx)
	if err != nil {
		return lockedSkill{}, err
	}
	defer func() { err = errors.Join(err, writer.close()) }()
	oldLock, oldBytes, hadLock, err := writer.readLock()
	if err != nil {
		return lockedSkill{}, err
	}
	before, err := writer.destinationState(ctx, requirement.skillID())
	if err != nil {
		return lockedSkill{}, err
	}
	if err := assertManagedDestination(oldLock, requirement.skillID(), before); err != nil {
		return lockedSkill{}, err
	}
	verified, err := writer.stageAndVerify(ctx, requirement, fetched)
	if err != nil {
		return lockedSkill{}, err
	}
	defer func() { err = errors.Join(err, verified.close()) }()
	locked = lockedSkill{publication: verified.publication}
	newLock, err := oldLock.with(locked)
	if err != nil {
		return lockedSkill{}, err
	}
	if old, found := oldLock.lookup(requirement.skillID()); found && old.publication == verified.publication && before.exists && before.digest == verified.publication.Tree() {
		return locked, nil
	}
	if err := writer.assertUnchanged(ctx, requirement.skillID(), oldBytes, hadLock, before); err != nil {
		return lockedSkill{}, err
	}
	if err := writer.replace(ctx, verified, newLock, true); err != nil {
		return lockedSkill{}, err
	}
	return locked, nil
}

func (i *installer) restore(ctx context.Context, project project) (err error) {
	if err := project.validate(); err != nil {
		return err
	}
	writer, err := project.acquireWriter(ctx)
	if err != nil {
		return err
	}
	plan, err := makeRestorePlan(ctx, writer)
	err = errors.Join(err, writer.close())
	if err != nil || len(plan.missing) == 0 {
		return err
	}
	fetched := make([]fetchedRepair, 0, len(plan.missing))
	defer func() {
		for _, item := range fetched {
			err = errors.Join(err, closeFetchedTree(item.skill))
		}
	}()
	for _, publication := range plan.missing {
		requirement, exactErr := exact(publication.Skill(), publication.Tree())
		if exactErr != nil {
			return exactErr
		}
		skill, fetchErr := i.remote.fetch(ctx, requirement)
		if fetchErr != nil {
			return fmt.Errorf("fetch locked skill %s: %w", publication.Skill().String(), fetchErr)
		}
		fetched = append(fetched, fetchedRepair{requirement: requirement, skill: skill})
	}
	writer, err = project.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.close()) }()
	if err := plan.matches(ctx, writer); err != nil {
		return err
	}
	for _, item := range fetched {
		verified, verifyErr := writer.stageAndVerify(ctx, item.requirement, item.skill)
		if verifyErr != nil {
			return verifyErr
		}
		if replaceErr := writer.replace(ctx, verified, plan.lock, false); replaceErr != nil {
			_ = verified.close()
			return replaceErr
		}
		if closeErr := verified.close(); closeErr != nil {
			return closeErr
		}
	}
	return nil
}

type restorePlan struct {
	lock    lock
	bytes   []byte
	hadLock bool
	states  map[registry.SkillID]destinationState
	missing []registry.PublicationID
}
type fetchedRepair struct {
	requirement requirement
	skill       fetchedSkill
}

func makeRestorePlan(ctx context.Context, writer *projectWriter) (restorePlan, error) {
	lock, contents, hadLock, err := writer.readLock()
	if err != nil {
		return restorePlan{}, err
	}
	plan := restorePlan{lock: lock, bytes: contents, hadLock: hadLock, states: make(map[registry.SkillID]destinationState)}
	for _, locked := range lock.skills() {
		publication := locked.publication
		state, stateErr := writer.destinationState(ctx, publication.Skill())
		if stateErr != nil {
			return restorePlan{}, stateErr
		}
		plan.states[publication.Skill()] = state
		if !state.exists || state.digest != publication.Tree() {
			plan.missing = append(plan.missing, publication)
		}
	}
	return plan, nil
}

func (p restorePlan) matches(ctx context.Context, writer *projectWriter) error {
	lock, contents, hadLock, err := writer.readLock()
	if err != nil {
		return projectChanged("reread project lock", err)
	}
	if hadLock != p.hadLock || !bytes.Equal(contents, p.bytes) {
		return errProjectChanged
	}
	for _, locked := range lock.skills() {
		state, stateErr := writer.destinationState(ctx, locked.publication.Skill())
		if stateErr != nil {
			return projectChanged("revalidate installed skill "+locked.publication.Skill().String(), stateErr)
		}
		if !sameDestination(state, p.states[locked.publication.Skill()]) {
			return errProjectChanged
		}
	}
	return nil
}

func projectChanged(operation string, err error) error {
	return errors.Join(errProjectChanged, fmt.Errorf("%s: %w", operation, err))
}

func closeFetchedTree(fetched fetchedSkill) error {
	if fetched.tree == nil {
		return nil
	}
	return fetched.tree.Close()
}

func assertManagedDestination(lock lock, skill registry.SkillID, state destinationState) error {
	if !state.exists {
		return nil
	}
	locked, found := lock.lookup(skill)
	if !found {
		return fmt.Errorf("%w: %s is not listed in ts-skills.lock", errLocalChanges, skill.String())
	}
	if state.digest != locked.publication.Tree() {
		return fmt.Errorf("%w: %s", errLocalChanges, skill.String())
	}
	return nil
}

func (w *projectWriter) assertUnchanged(ctx context.Context, skill registry.SkillID, oldBytes []byte, hadLock bool, before destinationState) error {
	_, currentBytes, currentHadLock, err := w.readLock()
	if err != nil {
		return projectChanged("reread project lock", err)
	}
	if currentHadLock != hadLock || !bytes.Equal(currentBytes, oldBytes) {
		return errProjectChanged
	}
	after, err := w.destinationState(ctx, skill)
	if err != nil {
		return projectChanged("revalidate installed skill "+skill.String(), err)
	}
	if !sameDestination(before, after) {
		return errProjectChanged
	}
	return nil
}

func (w *projectWriter) stageAndVerify(ctx context.Context, requirement requirement, fetched fetchedSkill) (verified *verifiedTree, err error) {
	if fetched.tree == nil {
		return nil, fmt.Errorf("%w: fetched tree is missing", protocol.ErrInvalidResponse)
	}
	publication := fetched.publication
	if publication.Skill() != requirement.skillID() {
		return nil, fmt.Errorf("%w: requested %s, received %s", protocol.ErrInvalidResponse, requirement.skillID(), publication.Skill())
	}
	if digest, exact := requirement.exactDigest(); exact && publication.Tree() != digest {
		return nil, fmt.Errorf("%w: registry returned a different exact digest", protocol.ErrInvalidResponse)
	}
	staged, err := copyFetchedTree(ctx, w.project.skillsDir(), fetched.tree)
	if err != nil {
		return nil, err
	}
	inspection, err := registry.Inspect(ctx, staged, ".")
	if err != nil {
		_ = staged.Close()
		return nil, fmt.Errorf("validate staged Agent Skill: %w", err)
	}
	if err := inspection.Verify(publication); err != nil {
		_ = staged.Close()
		return nil, fmt.Errorf("%w: staged tree: %v", protocol.ErrInvalidResponse, err)
	}
	stagedPath, err := staged.TakePath()
	if err != nil {
		_ = staged.Close()
		return nil, fmt.Errorf("take verified install tree: %w", err)
	}
	w.staging[stagedPath] = struct{}{}
	return &verifiedTree{publication: publication, path: stagedPath, owned: true, writer: w}, nil
}

func copyFetchedTree(ctx context.Context, parent string, source fs.FS) (*tree.Snapshot, error) {
	snapshot, err := tree.Stage(ctx, parent, installStagingPrefix, source)
	if err != nil {
		return nil, fmt.Errorf("copy verified install tree: %w", err)
	}
	return snapshot, nil
}

func (w *projectWriter) replace(ctx context.Context, verified *verifiedTree, lock lock, writeLock bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	destination := w.project.destination(verified.publication.Skill().Name().String())
	exists, err := w.inspectDestination(destination)
	if err != nil {
		return err
	}
	trash, err := w.createTrash(verified.publication, exists)
	if err != nil {
		return fmt.Errorf("create install trash: %w", err)
	}
	if exists {
		if err := w.rename(destination, filepath.Join(trash, trashTreeName)); err != nil {
			return errors.Join(fmt.Errorf("move old skill aside: %w", err), w.removeAll(trash))
		}
		if err := w.syncDirectory(trash); err != nil {
			return w.rollbackReplacement(destination, trash, err, exists, false)
		}
		if err := w.syncDirectory(w.project.skillsDir()); err != nil {
			return w.rollbackReplacement(destination, trash, err, exists, false)
		}
	}
	staged, err := verified.transfer()
	if err != nil {
		return w.rollbackReplacement(destination, trash, err, exists, false)
	}
	if err := w.rename(staged, destination); err != nil {
		return errors.Join(w.rollbackReplacement(destination, trash, fmt.Errorf("replace skill destination: %w", err), exists, false), w.removeAll(staged))
	}
	if err := w.syncDirectory(w.project.skillsDir()); err != nil {
		return w.rollbackReplacement(destination, trash, err, exists, true)
	}
	if writeLock {
		committed, err := w.writeLock(lock)
		if err != nil && !committed {
			return w.rollbackReplacement(destination, trash, err, exists, true)
		}
		if err != nil {
			return errors.Join(err, w.sweepLitter(ctx))
		}
	}
	return errors.Join(w.discardTrash(trash), w.sweepLitter(ctx))
}

func (w *projectWriter) rollbackReplacement(destination, trash string, cause error, hadDestination, removeDestination bool) error {
	if trash != "" {
		var err error
		trash, err = w.transitionTrash(trash, installTrashPendingPrefix, installTrashRecoveryPrefix)
		if err != nil {
			return errors.Join(cause, err)
		}
	}
	var rollbackErr error
	if removeDestination {
		rollbackErr = w.removeAll(destination)
	}
	if rollbackErr == nil && hadDestination {
		rollbackErr = w.rename(filepath.Join(trash, trashTreeName), destination)
	}
	if rollbackErr == nil {
		rollbackErr = w.syncDirectory(w.project.skillsDir())
		if rollbackErr == nil {
			rollbackErr = w.discardTrash(trash)
		}
	}
	return errors.Join(cause, rollbackErr)
}

func (w *projectWriter) discardTrash(trash string) error {
	if trash == "" {
		return nil
	}
	prefix := installTrashPendingPrefix
	if strings.HasPrefix(filepath.Base(trash), installTrashRecoveryPrefix) {
		prefix = installTrashRecoveryPrefix
	}
	garbage, err := w.transitionTrash(trash, prefix, installTrashGarbagePrefix)
	if err != nil {
		return err
	}
	_ = w.removeAll(garbage)
	return nil
}

// writeLock reports whether the new lock name replaced the old one. Once the
// rename succeeds, the skill and lock agree even if syncing their parent fails.
func (w *projectWriter) writeLock(lock lock) (committed bool, err error) {
	contents, err := encodeLockBytes(lock)
	if err != nil {
		return false, err
	}
	temporaryFile, temporary, err := w.createTemp(filepath.Dir(w.project.lockPath()), lockTemporaryPrefix)
	if err != nil {
		return false, err
	}
	temporaryName, err := w.project.managedName(temporary)
	if err != nil {
		return false, errors.Join(err, temporaryFile.Close(), os.Remove(temporary))
	}
	writeErr := func() error {
		_, err := temporaryFile.Write(contents)
		if err == nil {
			err = temporaryFile.Sync()
		}
		return errors.Join(err, temporaryFile.Close())
	}()
	if writeErr != nil {
		return false, errors.Join(fmt.Errorf("write temporary project lock: %w", writeErr), w.root.Remove(temporaryName))
	}
	if err := w.rename(temporary, w.project.lockPath()); err != nil {
		return false, errors.Join(fmt.Errorf("replace project lock: %w", err), w.root.Remove(temporaryName))
	}
	if err := w.syncDirectory(filepath.Dir(w.project.lockPath())); err != nil {
		return true, err
	}
	return true, nil
}

func encodeLockBytes(lock lock) ([]byte, error) {
	var buffer bytes.Buffer
	err := encodeLock(&buffer, lock)
	return buffer.Bytes(), err
}
