package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

var (
	ErrBusy                 = errors.New("project is being modified by another ts-skills process")
	ErrUnmanagedDestination = errors.New("destination exists but is not managed by this project lock")
	ErrLocalChanges         = errors.New("managed skill has local changes")
	ErrIdentityMismatch     = errors.New("registry returned another publication")
	ErrDigestMismatch       = errors.New("fetched tree digest does not match publication")
	ErrRecoveryRequired     = errors.New("project transaction requires recovery")
)

type Installer struct {
	remote Remote
}

func NewInstaller(remote Remote) (*Installer, error) {
	if remote == nil {
		return nil, fmt.Errorf("installer remote must be provided")
	}
	return &Installer{remote: remote}, nil
}

func (i *Installer) Install(ctx context.Context, project Project, requirement Requirement) (locked LockedSkill, err error) {
	if project.root == "" || requirement.skill.String() == "" {
		return LockedSkill{}, fmt.Errorf("install requires a valid project and requirement")
	}
	writer, err := project.acquireWriter(ctx)
	if err != nil {
		return LockedSkill{}, err
	}
	defer func() { err = errors.Join(err, writer.close()) }()

	oldLock, _, _, err := writer.readLock()
	if err != nil {
		return LockedSkill{}, err
	}
	matchingDestination, err := writer.preflight(ctx, oldLock, requirement.Skill())
	if err != nil {
		return LockedSkill{}, err
	}
	fetched, err := i.remote.Fetch(ctx, requirement)
	if err != nil {
		return LockedSkill{}, fmt.Errorf("fetch skill %s: %w", requirement.skill.String(), err)
	}
	verified, err := writer.stageAndVerify(ctx, requirement, fetched)
	if err != nil {
		return LockedSkill{}, err
	}
	defer func() { err = errors.Join(err, verified.close()) }()

	locked, err = NewLockedSkill(verified.publication)
	if err != nil {
		return LockedSkill{}, err
	}
	newLock, err := oldLock.With(locked)
	if err != nil {
		return LockedSkill{}, err
	}
	if old, found := oldLock.Lookup(requirement.Skill()); matchingDestination && found && old.Publication() == verified.publication {
		return locked, nil
	}
	if err := writer.install(ctx, verified, newLock); err != nil {
		return LockedSkill{}, err
	}
	return locked, nil
}

func (i *Installer) Restore(ctx context.Context, project Project) (err error) {
	if project.root == "" {
		return fmt.Errorf("restore requires a valid project")
	}
	writer, err := project.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.close()) }()

	lock, _, _, err := writer.readLock()
	if err != nil {
		return err
	}
	missing := make([]registry.PublicationID, 0)
	for _, locked := range lock.Skills() {
		publication := locked.Publication()
		matching, preflightErr := writer.preflight(ctx, lock, publication.Skill())
		if preflightErr != nil {
			return preflightErr
		}
		if !matching {
			missing = append(missing, publication)
		}
	}

	type repair struct {
		verified *verifiedTree
	}
	repairs := make([]repair, 0, len(missing))
	for _, publication := range missing {
		requirement, exactErr := Exact(publication.Skill(), publication.Tree())
		if exactErr != nil {
			return exactErr
		}
		fetched, fetchErr := i.remote.Fetch(ctx, requirement)
		if fetchErr != nil {
			return fmt.Errorf("fetch locked skill %s: %w", publication.Skill().String(), fetchErr)
		}
		verified, verifyErr := writer.stageAndVerify(ctx, requirement, fetched)
		if verifyErr != nil {
			return verifyErr
		}
		defer func() { err = errors.Join(err, verified.close()) }()
		repairs = append(repairs, repair{verified: verified})
	}

	for _, repair := range repairs {
		if err := writer.install(ctx, repair.verified, lock); err != nil {
			return err
		}
	}
	return nil
}

func (w *projectWriter) stageAndVerify(ctx context.Context, requirement Requirement, fetched FetchedSkill) (verified *verifiedTree, err error) {
	tree := fetched.Tree()
	if tree == nil {
		return nil, fmt.Errorf("%w: fetched tree is missing", ErrIdentityMismatch)
	}
	defer func() {
		closeErr := tree.Close()
		if closeErr != nil {
			if verified != nil {
				closeErr = errors.Join(closeErr, verified.close())
				verified = nil
			}
			err = errors.Join(err, fmt.Errorf("close fetched tree: %w", closeErr))
		}
	}()

	publication := fetched.Publication()
	if publication.Skill() != requirement.Skill() {
		return nil, fmt.Errorf("%w: requested %s, received %s", ErrIdentityMismatch, requirement.Skill().String(), publication.Skill().String())
	}
	if digest, exact := requirement.ExactDigest(); exact && publication.Tree() != digest {
		return nil, fmt.Errorf("%w: registry returned a different exact digest", ErrIdentityMismatch)
	}
	snapshot, err := stageFetched(ctx, w.project.StateDir(), tree)
	if err != nil {
		return nil, fmt.Errorf("stage fetched tree: %w", err)
	}
	defer func() {
		if closeErr := snapshot.Close(); closeErr != nil {
			if verified != nil {
				closeErr = errors.Join(closeErr, verified.close())
				verified = nil
			}
			err = errors.Join(err, fmt.Errorf("close verified fetch snapshot: %w", closeErr))
		}
	}()
	directory, err := agentskill.Load(snapshot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("validate fetched Agent Skill: %w", err)
	}
	if directory.Document().Name != requirement.Skill().Name() {
		return nil, fmt.Errorf("%w: SKILL.md names %s", ErrIdentityMismatch, directory.Document().Name.String())
	}
	actual, err := agentskill.SumTree(ctx, snapshot.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("hash fetched tree: %w", err)
	}
	if actual != publication.Tree() {
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, publication.Tree().String(), actual.String())
	}

	staged, err := copySnapshotToProject(ctx, w.project, snapshot.FS())
	if err != nil {
		return nil, err
	}
	w.staging[staged] = struct{}{}
	return &verifiedTree{publication: publication, path: staged, owned: true, writer: w}, nil
}

func copySnapshotToProject(ctx context.Context, project Project, source fs.FS) (string, error) {
	staged, err := createManagedTempDirectory(project.StateDir(), installStagingPrefix)
	if err != nil {
		return "", fmt.Errorf("create install staging: %w", err)
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
		if err := rejectPathComponents(destination, true); err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.Mkdir(destination, 0o755); err != nil {
				return err
			}
			return ensureRealDirectory(destination, false)
		}
		if entry.Type()&fs.ModeType != 0 {
			return fmt.Errorf("staged tree contains unsupported path %q", name)
		}
		contents, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(destination, contents, 0o644); err != nil {
			return err
		}
		return rejectRegularFile(destination)
	})
	if err != nil {
		return "", fmt.Errorf("copy verified install tree: %w", err)
	}
	if err := requireSameFilesystem(project.root, staged); err != nil {
		return "", fmt.Errorf("verified staging is not on the project filesystem: %w", err)
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
		closeErr := file.Close()
		return errors.Join(addErr, closeErr)
	})
	if err != nil {
		return nil, err
	}
	return builder.Finish()
}
