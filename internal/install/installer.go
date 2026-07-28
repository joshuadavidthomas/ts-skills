package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/registry"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
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

func (i *Installer) Install(ctx context.Context, project Project, requirement Requirement) (LockedSkill, error) {
	if project.root == "" || requirement.skill.String() == "" {
		return LockedSkill{}, fmt.Errorf("install requires a valid project and requirement")
	}
	if err := prepareManagedDirectories(project); err != nil {
		return LockedSkill{}, err
	}
	fetched, err := i.remote.Fetch(ctx, requirement)
	if err != nil {
		return LockedSkill{}, fmt.Errorf("fetch skill %s: %w", requirement.skill.String(), err)
	}
	snapshot, publication, err := verifyFetched(ctx, project, requirement, fetched)
	if err != nil {
		return LockedSkill{}, err
	}
	defer snapshot.Close()

	destination := filepath.Join(project.SkillsDir(), requirement.skill.Name().String())
	if err := validateDestination(destination); err != nil {
		return LockedSkill{}, err
	}
	staged, err := copySnapshotToProject(ctx, project, snapshot.FS())
	if err != nil {
		return LockedSkill{}, err
	}
	defer os.RemoveAll(staged)
	if err := replaceDestination(destination, staged); err != nil {
		return LockedSkill{}, err
	}
	return NewLockedSkill(publication)
}

func verifyFetched(ctx context.Context, project Project, requirement Requirement, fetched FetchedSkill) (snapshot *safetree.Snapshot, publication registry.PublicationID, err error) {
	tree := fetched.Tree()
	if tree == nil {
		return nil, registry.PublicationID{}, fmt.Errorf("%w: fetched tree is missing", ErrIdentityMismatch)
	}
	defer func() {
		closeErr := tree.Close()
		if closeErr == nil {
			return
		}
		if snapshot != nil {
			closeErr = errors.Join(closeErr, snapshot.Close())
			snapshot = nil
		}
		err = errors.Join(err, fmt.Errorf("close fetched tree: %w", closeErr))
	}()

	publication = fetched.Publication()
	if publication.Skill() != requirement.Skill() {
		return nil, registry.PublicationID{}, fmt.Errorf("%w: requested %s, received %s", ErrIdentityMismatch, requirement.Skill().String(), publication.Skill().String())
	}
	if digest, exact := requirement.ExactDigest(); exact && publication.Tree() != digest {
		return nil, registry.PublicationID{}, fmt.Errorf("%w: registry returned a different exact digest", ErrIdentityMismatch)
	}
	snapshot, err = stageFetched(ctx, project.StateDir(), tree)
	if err != nil {
		return nil, registry.PublicationID{}, fmt.Errorf("stage fetched tree: %w", err)
	}
	directory, err := agentskill.Load(snapshot.FS(), ".")
	if err != nil {
		_ = snapshot.Close()
		return nil, registry.PublicationID{}, fmt.Errorf("validate fetched Agent Skill: %w", err)
	}
	if directory.Document().Name != requirement.Skill().Name() {
		_ = snapshot.Close()
		return nil, registry.PublicationID{}, fmt.Errorf("%w: SKILL.md names %s", ErrIdentityMismatch, directory.Document().Name.String())
	}
	actual, err := agentskill.SumTree(snapshot.FS(), ".")
	if err != nil {
		_ = snapshot.Close()
		return nil, registry.PublicationID{}, fmt.Errorf("hash fetched tree: %w", err)
	}
	if actual != publication.Tree() {
		_ = snapshot.Close()
		return nil, registry.PublicationID{}, fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, publication.Tree().String(), actual.String())
	}
	return snapshot, publication, nil
}

func prepareManagedDirectories(project Project) error {
	if err := ensureRealDirectory(project.root, false); err != nil {
		return err
	}
	for _, directory := range []string{filepath.Join(project.root, ".agents"), project.SkillsDir(), project.StateDir()} {
		if err := ensureRealDirectory(directory, true); err != nil {
			return err
		}
	}
	return nil
}

func ensureRealDirectory(name string, create bool) error {
	info, err := os.Lstat(name)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("managed path %q must be a real directory", name)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) || !create {
		return fmt.Errorf("inspect managed path %q: %w", name, err)
	}
	if err := ensureRealDirectory(filepath.Dir(name), false); err != nil {
		return err
	}
	if err := os.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create managed directory %q: %w", name, err)
	}
	return ensureRealDirectory(name, false)
}

func validateDestination(destination string) error {
	info, err := os.Lstat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect skill destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrUnmanagedDestination, destination)
	}
	return nil
}

func copySnapshotToProject(ctx context.Context, project Project, source fs.FS) (string, error) {
	staged, err := os.MkdirTemp(project.StateDir(), "install-")
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
	ok = true
	return staged, nil
}

func replaceDestination(destination, staged string) error {
	backup := destination + ".ts-skills-backup"
	if _, err := os.Lstat(backup); err == nil {
		return fmt.Errorf("%w: stale backup %s", ErrRecoveryRequired, backup)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect destination backup: %w", err)
	}
	_, destinationErr := os.Lstat(destination)
	hadDestination := destinationErr == nil
	if destinationErr != nil && !errors.Is(destinationErr, fs.ErrNotExist) {
		return fmt.Errorf("inspect existing destination: %w", destinationErr)
	}
	if hadDestination {
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("back up existing destination: %w", err)
		}
	}
	if err := os.Rename(staged, destination); err != nil {
		if hadDestination {
			if rollbackErr := os.Rename(backup, destination); rollbackErr != nil {
				return errors.Join(fmt.Errorf("install verified destination: %w", err), fmt.Errorf("%w: restore previous destination: %v", ErrRecoveryRequired, rollbackErr))
			}
		}
		return fmt.Errorf("install verified destination: %w", err)
	}
	if hadDestination {
		_ = os.RemoveAll(backup)
	}
	return nil
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
