package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

var (
	errBusy             = errors.New("project is being modified by another ts-skills process")
	errIdentityMismatch = errors.New("registry returned another publication")
	errDigestMismatch   = errors.New("fetched tree digest does not match publication")
	errLocalChanges     = errors.New("installed skill differs from the project lock")
	errProjectChanged   = errors.New("project changed during restore")
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
		return nil, fmt.Errorf("%w: fetched tree is missing", errIdentityMismatch)
	}
	publication := fetched.publication
	if publication.Skill() != requirement.skillID() {
		return nil, fmt.Errorf("%w: requested %s, received %s", errIdentityMismatch, requirement.skillID(), publication.Skill())
	}
	if digest, exact := requirement.exactDigest(); exact && publication.Tree() != digest {
		return nil, fmt.Errorf("%w: registry returned a different exact digest", errIdentityMismatch)
	}
	staged, err := copyFetchedTree(ctx, w.project.skillsDir(), fetched.tree)
	if err != nil {
		return nil, err
	}
	inspection, err := registry.Inspect(ctx, os.DirFS(staged), ".")
	if err != nil {
		_ = os.RemoveAll(staged)
		return nil, fmt.Errorf("validate staged Agent Skill: %w", err)
	}
	if err := inspection.RequireName(requirement.skillID().Name()); err != nil {
		_ = os.RemoveAll(staged)
		return nil, fmt.Errorf("%w: SKILL.md names %s", errIdentityMismatch, inspection.Document().Name)
	}
	if inspection.Digest() != publication.Tree() {
		_ = os.RemoveAll(staged)
		return nil, fmt.Errorf("%w: expected %s, got %s", errDigestMismatch, publication.Tree(), inspection.Digest())
	}
	w.staging[staged] = struct{}{}
	return &verifiedTree{publication: publication, path: staged, owned: true, writer: w}, nil
}

func copyFetchedTree(ctx context.Context, parent string, source fs.FS) (string, error) {
	staged, err := temporaryPath(parent, installStagingPrefix)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(staged, 0o700); err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(staged)
		}
	}()
	err = fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			if !entry.IsDir() {
				return fmt.Errorf("fetched tree root is not a directory")
			}
			return nil
		}
		destination := filepath.Join(staged, filepath.FromSlash(name))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Mkdir(destination, 0o755)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fetched tree contains unsupported path %q", name)
		}
		input, err := source.Open(name)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return errors.Join(err, input.Close())
		}
		copied, copyErr := io.Copy(output, &contextReader{ctx: ctx, source: input})
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeOutputErr, closeInputErr); err != nil {
			return err
		}
		if copied != info.Size() {
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("copy verified install tree: %w", err)
	}
	if err := syncTree(ctx, staged); err != nil {
		return "", err
	}
	ok = true
	return staged, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func (w *projectWriter) replace(ctx context.Context, verified *verifiedTree, lock lock, writeLock bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	destination := w.project.destination(verified.publication.Skill().Name().String())
	exists, err := inspectDestination(destination)
	if err != nil {
		return err
	}
	trash, err := w.createTrash(verified.publication, exists)
	if err != nil {
		return fmt.Errorf("create install trash: %w", err)
	}
	if exists {
		if err := w.rename(destination, filepath.Join(trash, trashTreeName)); err != nil {
			return errors.Join(fmt.Errorf("move old skill aside: %w", err), os.RemoveAll(trash))
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
		return errors.Join(w.rollbackReplacement(destination, trash, fmt.Errorf("replace skill destination: %w", err), exists, false), os.RemoveAll(staged))
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
		rollbackErr = os.RemoveAll(destination)
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
	_ = os.RemoveAll(garbage)
	return nil
}

// writeLock reports whether the new lock name replaced the old one. Once the
// rename succeeds, the skill and lock agree even if syncing their parent fails.
func (w *projectWriter) writeLock(lock lock) (committed bool, err error) {
	contents, err := encodeLockBytes(lock)
	if err != nil {
		return false, err
	}
	temporary, err := temporaryPath(filepath.Dir(w.project.lockPath()), lockTemporaryPrefix)
	if err != nil {
		return false, err
	}
	if err := writeSyncedFile(temporary, contents, 0o600); err != nil {
		return false, errors.Join(err, os.Remove(temporary))
	}
	if err := w.rename(temporary, w.project.lockPath()); err != nil {
		return false, errors.Join(fmt.Errorf("replace project lock: %w", err), os.Remove(temporary))
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
