package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

var (
	ErrBusy             = errors.New("project is being modified by another ts-skills process")
	ErrIdentityMismatch = errors.New("registry returned another publication")
	ErrDigestMismatch   = errors.New("fetched tree digest does not match publication")
	ErrLocalChanges     = errors.New("installed skill differs from the project lock")
	ErrProjectChanged   = errors.New("project changed during restore")
)

type Installer struct{ remote *Remote }

func NewInstaller(remote *Remote) (*Installer, error) {
	if remote == nil {
		return nil, fmt.Errorf("installer remote must be provided")
	}
	return &Installer{remote: remote}, nil
}

func (i *Installer) Install(ctx context.Context, project Project, requirement Requirement) (locked LockedSkill, err error) {
	if err := project.validate(); err != nil {
		return LockedSkill{}, err
	}
	fetchedLock, fetchedLockExists, err := readLockSnapshot(project)
	if err != nil {
		return LockedSkill{}, err
	}
	fetched, err := i.remote.Fetch(ctx, requirement)
	if err != nil {
		return LockedSkill{}, fmt.Errorf("fetch skill %s: %w", requirement.Skill().String(), err)
	}
	defer func() { err = errors.Join(err, closeFetchedTree(fetched)) }()
	writer, err := project.acquireWriter(ctx)
	if err != nil {
		return LockedSkill{}, err
	}
	defer func() { err = errors.Join(err, writer.close()) }()
	oldLock, oldBytes, hadLock, err := writer.readLock()
	if err != nil {
		return LockedSkill{}, err
	}
	if hadLock != fetchedLockExists || !bytes.Equal(oldBytes, fetchedLock) {
		return LockedSkill{}, ErrProjectChanged
	}
	before, err := writer.destinationState(ctx, requirement.Skill())
	if err != nil {
		return LockedSkill{}, err
	}
	if err := assertManagedDestination(oldLock, requirement.Skill(), before); err != nil {
		return LockedSkill{}, err
	}
	verified, err := writer.stageAndVerify(ctx, requirement, fetched)
	if err != nil {
		return LockedSkill{}, err
	}
	defer func() { err = errors.Join(err, verified.close()) }()
	locked = LockedSkill{Publication: verified.publication}
	newLock, err := oldLock.With(locked)
	if err != nil {
		return LockedSkill{}, err
	}
	if old, found := oldLock.Lookup(requirement.Skill()); found && old.Publication == verified.publication && before.exists && before.digest == verified.publication.Tree() {
		return locked, nil
	}
	if err := writer.assertUnchanged(ctx, requirement.Skill(), oldBytes, hadLock, before); err != nil {
		return LockedSkill{}, err
	}
	if err := writer.replace(ctx, verified, newLock, true); err != nil {
		return LockedSkill{}, err
	}
	return locked, nil
}

func (i *Installer) Restore(ctx context.Context, project Project) (err error) {
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
		requirement, exactErr := Exact(publication.Skill(), publication.Tree())
		if exactErr != nil {
			return exactErr
		}
		skill, fetchErr := i.remote.Fetch(ctx, requirement)
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
	lock    Lock
	bytes   []byte
	hadLock bool
	states  map[agentskill.SkillID]destinationState
	missing []agentskill.PublicationID
}
type fetchedRepair struct {
	requirement Requirement
	skill       FetchedSkill
}

func makeRestorePlan(ctx context.Context, writer *projectWriter) (restorePlan, error) {
	lock, contents, hadLock, err := writer.readLock()
	if err != nil {
		return restorePlan{}, err
	}
	plan := restorePlan{lock: lock, bytes: contents, hadLock: hadLock, states: make(map[agentskill.SkillID]destinationState)}
	for _, locked := range lock.Skills() {
		publication := locked.Publication
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
		return ErrProjectChanged
	}
	for _, locked := range lock.Skills() {
		state, stateErr := writer.destinationState(ctx, locked.Publication.Skill())
		if stateErr != nil {
			return projectChanged("revalidate installed skill "+locked.Publication.Skill().String(), stateErr)
		}
		if !sameDestination(state, p.states[locked.Publication.Skill()]) {
			return ErrProjectChanged
		}
	}
	return nil
}

func projectChanged(operation string, err error) error {
	return errors.Join(ErrProjectChanged, fmt.Errorf("%s: %w", operation, err))
}

func readLockSnapshot(project Project) ([]byte, bool, error) {
	if err := rejectLink(project.LockPath(), true); err != nil {
		return nil, false, fmt.Errorf("inspect project lock: %w", err)
	}
	contents, err := os.ReadFile(project.LockPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read project lock: %w", err)
	}
	return contents, true, nil
}

func closeFetchedTree(fetched FetchedSkill) error {
	if fetched.Tree == nil {
		return nil
	}
	return fetched.Tree.Close()
}

func assertManagedDestination(lock Lock, skill agentskill.SkillID, state destinationState) error {
	if !state.exists {
		return nil
	}
	locked, found := lock.Lookup(skill)
	if !found {
		return fmt.Errorf("%w: %s is not listed in ts-skills.lock", ErrLocalChanges, skill.String())
	}
	if state.digest != locked.Publication.Tree() {
		return fmt.Errorf("%w: %s", ErrLocalChanges, skill.String())
	}
	return nil
}

func (w *projectWriter) assertUnchanged(ctx context.Context, skill agentskill.SkillID, oldBytes []byte, hadLock bool, before destinationState) error {
	_, currentBytes, currentHadLock, err := w.readLock()
	if err != nil {
		return projectChanged("reread project lock", err)
	}
	if currentHadLock != hadLock || !bytes.Equal(currentBytes, oldBytes) {
		return ErrProjectChanged
	}
	after, err := w.destinationState(ctx, skill)
	if err != nil {
		return projectChanged("revalidate installed skill "+skill.String(), err)
	}
	if !sameDestination(before, after) {
		return ErrProjectChanged
	}
	return nil
}

func (w *projectWriter) stageAndVerify(ctx context.Context, requirement Requirement, fetched FetchedSkill) (verified *verifiedTree, err error) {
	if fetched.Tree == nil {
		return nil, fmt.Errorf("%w: fetched tree is missing", ErrIdentityMismatch)
	}
	publication := fetched.Publication
	if publication.Skill() != requirement.Skill() {
		return nil, fmt.Errorf("%w: requested %s, received %s", ErrIdentityMismatch, requirement.Skill(), publication.Skill())
	}
	if digest, exact := requirement.ExactDigest(); exact && publication.Tree() != digest {
		return nil, fmt.Errorf("%w: registry returned a different exact digest", ErrIdentityMismatch)
	}
	snapshot, err := stageFetched(ctx, w.project.StateDir(), fetched.Tree)
	if err != nil {
		return nil, fmt.Errorf("stage fetched tree: %w", err)
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	staged, err := copySnapshotToProject(ctx, w.project.SkillsDir(), snapshot.FS())
	if err != nil {
		return nil, err
	}
	inspection, err := agentskill.Inspect(ctx, os.DirFS(staged), ".")
	if err != nil {
		_ = os.RemoveAll(staged)
		return nil, fmt.Errorf("validate staged Agent Skill: %w", err)
	}
	if err := inspection.RequireName(requirement.Skill().Name()); err != nil {
		_ = os.RemoveAll(staged)
		return nil, fmt.Errorf("%w: SKILL.md names %s", ErrIdentityMismatch, inspection.Document().Name)
	}
	if inspection.Digest() != publication.Tree() {
		_ = os.RemoveAll(staged)
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, publication.Tree(), inspection.Digest())
	}
	w.staging[staged] = struct{}{}
	return &verifiedTree{publication: publication, path: staged, owned: true, writer: w}, nil
}

func copySnapshotToProject(ctx context.Context, parent string, source fs.FS) (string, error) {
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
			return nil
		}
		destination := filepath.Join(staged, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.Mkdir(destination, 0o755)
		}
		if entry.Type()&fs.ModeType != 0 {
			return fmt.Errorf("staged tree contains unsupported path %q", name)
		}
		contents, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, contents, 0o644)
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

func stageFetched(ctx context.Context, parent string, source fs.FS) (_ *safetree.Snapshot, err error) {
	builder, err := safetree.NewBuilder(parent, safetree.PrototypeLimits())
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, builder.Close())
		}
	}()
	err = fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe fetched path %q", name)
		}
		file, err := source.Open(name)
		if err != nil {
			return err
		}
		addErr := builder.AddFile(ctx, name, info.Size(), file)
		return errors.Join(addErr, file.Close())
	})
	if err != nil {
		return nil, err
	}
	return builder.Finish()
}

func (w *projectWriter) replace(ctx context.Context, verified *verifiedTree, lock Lock, writeLock bool) error {
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
		if err := w.syncDirectory(w.project.SkillsDir()); err != nil {
			return w.rollbackReplacement(destination, trash, err, exists, false)
		}
	}
	staged, err := verified.transfer()
	if err != nil {
		return err
	}
	if err := w.rename(staged, destination); err != nil {
		return errors.Join(w.rollbackReplacement(destination, trash, fmt.Errorf("replace skill destination: %w", err), exists, false), os.RemoveAll(staged))
	}
	if err := w.syncDirectory(w.project.SkillsDir()); err != nil {
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
		rollbackErr = w.syncDirectory(w.project.SkillsDir())
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
func (w *projectWriter) writeLock(lock Lock) (committed bool, err error) {
	contents, err := encodeLockBytes(lock)
	if err != nil {
		return false, err
	}
	temporary, err := temporaryPath(filepath.Dir(w.project.LockPath()), lockTemporaryPrefix)
	if err != nil {
		return false, err
	}
	if err := writeSyncedFile(temporary, contents, 0o600); err != nil {
		return false, errors.Join(err, os.Remove(temporary))
	}
	if err := w.rename(temporary, w.project.LockPath()); err != nil {
		return false, errors.Join(fmt.Errorf("replace project lock: %w", err), os.Remove(temporary))
	}
	if err := w.syncDirectory(filepath.Dir(w.project.LockPath())); err != nil {
		return true, err
	}
	return true, nil
}

func encodeLockBytes(lock Lock) ([]byte, error) {
	var buffer bytes.Buffer
	err := EncodeLock(&buffer, lock)
	return buffer.Bytes(), err
}
