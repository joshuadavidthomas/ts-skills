package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/client"
	"github.com/joshuadavidthomas/ts-skills/internal/config"
	"github.com/joshuadavidthomas/ts-skills/internal/install"
	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
	"github.com/joshuadavidthomas/ts-skills/internal/version"
)

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		return fmt.Errorf("command context must be provided")
	}
	if stdin == nil || stdout == nil || stderr == nil {
		return fmt.Errorf("command input and output streams must be provided")
	}
	if len(args) == 0 {
		return fmt.Errorf("choose a command: install, restore, or version")
	}
	switch args[0] {
	case "install":
		return runInstall(ctx, args[1:], stdout, stderr)
	case "restore":
		return runRestore(ctx, args[1:], stdout, stderr)
	case "version", "--version":
		_, err := fmt.Fprintf(stdout, "ts-skills %s\n", version.Version)
		return err
	default:
		return fmt.Errorf("unknown command %q; choose install, restore, or version", args[0])
	}
}

func runInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ts-skills install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projectPath := flags.String("project", "", "explicit project directory")
	configPath := flags.String("config", "", "configuration file")
	digestText := flags.String("digest", "", "exact sha256 tree digest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectPath == "" {
		return fmt.Errorf("install requires --project <dir>")
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("install requires one <namespace>/<name> argument")
	}
	skill, err := registry.ParseSkillID(flags.Arg(0))
	if err != nil {
		return fmt.Errorf("parse skill %q: %w", flags.Arg(0), err)
	}
	var requirement install.Requirement
	if *digestText == "" {
		requirement, err = install.Current(skill)
	} else {
		digest, parseErr := agentskill.ParseTreeDigest(*digestText)
		if parseErr != nil {
			return fmt.Errorf("parse --digest: %w", parseErr)
		}
		requirement, err = install.Exact(skill, digest)
	}
	if err != nil {
		return err
	}
	installer, project, cleanup, err := commandInstaller(*configPath, *projectPath)
	if err != nil {
		return err
	}
	defer cleanup()
	locked, err := installer.Install(ctx, project, requirement)
	if err != nil {
		return commandError("install", err)
	}
	publication := locked.Publication()
	_, err = fmt.Fprintf(stdout, "Installed %s at %s.\n", publication.Skill().String(), publication.Tree().String())
	return err
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ts-skills restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projectPath := flags.String("project", "", "explicit project directory")
	configPath := flags.String("config", "", "configuration file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectPath == "" {
		return fmt.Errorf("restore requires --project <dir>")
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("restore does not accept positional arguments")
	}
	installer, project, cleanup, err := commandInstaller(*configPath, *projectPath)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := installer.Restore(ctx, project); err != nil {
		return commandError("restore", err)
	}
	_, err = fmt.Fprintln(stdout, "Restored locked skills.")
	return err
}

func commandInstaller(configPath, projectPath string) (*install.Installer, install.Project, func(), error) {
	if configPath == "" {
		var err error
		configPath, err = config.DefaultPath()
		if err != nil {
			return nil, install.Project{}, func() {}, err
		}
	}
	settings, err := config.Load(configPath)
	if err != nil {
		return nil, install.Project{}, func() {}, err
	}
	project, err := install.OpenProject(projectPath)
	if err != nil {
		return nil, install.Project{}, func() {}, err
	}
	staging, err := os.MkdirTemp("", "ts-skills-client-")
	if err != nil {
		return nil, install.Project{}, func() {}, fmt.Errorf("create client staging directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	remote, err := client.NewRemote(settings.Registry, &http.Client{Timeout: 2 * time.Minute}, staging, safetree.PrototypeLimits())
	if err != nil {
		cleanup()
		return nil, install.Project{}, func() {}, err
	}
	installer, err := install.NewInstaller(remote)
	if err != nil {
		cleanup()
		return nil, install.Project{}, func() {}, err
	}
	return installer, project, cleanup, nil
}

func commandError(operation string, err error) error {
	switch {
	case errors.Is(err, install.ErrBusy):
		return fmt.Errorf("cannot %s while another ts-skills process is changing this project; wait and try again: %w", operation, err)
	case errors.Is(err, install.ErrUnmanagedDestination):
		return fmt.Errorf("cannot %s because the skill destination exists outside this project lock; move it aside and try again: %w", operation, err)
	case errors.Is(err, install.ErrLocalChanges):
		return fmt.Errorf("cannot %s because an installed skill has local changes; preserve or remove those changes before trying again: %w", operation, err)
	case errors.Is(err, install.ErrRecoveryRequired):
		return fmt.Errorf("cannot %s because a prior project update needs manual recovery: %w", operation, err)
	case errors.Is(err, install.ErrIdentityMismatch), errors.Is(err, install.ErrDigestMismatch):
		return fmt.Errorf("cannot %s because the registry response did not match the requested skill; project files were not changed: %w", operation, err)
	case errors.Is(err, registry.ErrNotFound):
		return fmt.Errorf("cannot %s because the requested skill publication was not found: %w", operation, err)
	case errors.Is(err, safetree.ErrLimitExceeded):
		return fmt.Errorf("cannot %s because the downloaded skill exceeds the configured safety limits: %w", operation, err)
	case errors.Is(err, protocol.ErrProtocol):
		return fmt.Errorf("cannot %s because the registry returned an invalid response; project files were not changed: %w", operation, err)
	case errors.Is(err, protocol.ErrInvalidRequest):
		return fmt.Errorf("cannot %s because the registry rejected the request as invalid: %w", operation, err)
	case errors.Is(err, protocol.ErrInternal):
		return fmt.Errorf("cannot %s because the registry could not complete the request: %w", operation, err)
	default:
		return fmt.Errorf("%s failed: %w", operation, err)
	}
}
