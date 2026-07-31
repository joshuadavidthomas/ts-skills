package client

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
	"github.com/joshuadavidthomas/ts-skills/internal/version"
)

// Run executes one ts-skills command. It writes every user-facing diagnostic
// exactly once and returns a non-nil error when the process should exit
// unsuccessfully.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	err := run(ctx, args, stdout, stderr)
	if err != nil && stderr != nil && !alreadyReported(err) {
		_, _ = fmt.Fprintf(stderr, "ts-skills: %v\n", err)
	}
	return err
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if ctx == nil {
		return fmt.Errorf("command context must be provided")
	}
	if stdout == nil || stderr == nil {
		return fmt.Errorf("command output streams must be provided")
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

func runInstall(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("ts-skills install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projectPath := flags.String("project", "", "explicit project directory")
	configPath := flags.String("config", "", "configuration file")
	digestText := flags.String("digest", "", "exact sha256 tree digest")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Help was already printed by the FlagSet; help is not an error.
			return nil
		}
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
	var requirement requirement
	if *digestText == "" {
		requirement, err = current(skill)
	} else {
		digest, parseErr := registry.ParseTreeDigest(*digestText)
		if parseErr != nil {
			return fmt.Errorf("parse --digest: %w", parseErr)
		}
		requirement, err = exact(skill, digest)
	}
	if err != nil {
		return err
	}
	installer, project, cleanup, err := commandInstaller(*configPath, *projectPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	locked, err := installer.install(ctx, project, requirement)
	if err != nil {
		return commandError("install", err)
	}
	publication := locked.publication
	_, err = fmt.Fprintf(stdout, "Installed %s at %s.\n", publication.Skill().String(), publication.Tree().String())
	return err
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("ts-skills restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projectPath := flags.String("project", "", "explicit project directory")
	configPath := flags.String("config", "", "configuration file")
	if err := parseFlags(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Help was already printed by the FlagSet; help is not an error.
			return nil
		}
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
	defer func() { err = errors.Join(err, cleanup()) }()
	if err := installer.restore(ctx, project); err != nil {
		return commandError("restore", err)
	}
	_, err = fmt.Fprintln(stdout, "Restored locked skills.")
	return err
}

// parseFlags parses args with a ContinueOnError FlagSet that has already
// written its diagnostics. flag.ErrHelp passes through untouched so the
// caller can treat help as success; any other parse failure is wrapped in
// reportedError because the FlagSet already printed cause and usage.
func parseFlags(flags *flag.FlagSet, args []string) error {
	err := flags.Parse(args)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		return reportedError{err}
	}
	return err
}

// reportedError marks errors whose diagnostics are already printed for the
// user (by the FlagSet), so main exits without printing them again.
type reportedError struct{ err error }

func (e reportedError) Error() string { return e.err.Error() }
func (e reportedError) Unwrap() error { return e.err }

// alreadyReported reports whether err's diagnostics were already shown to
// the user, so the caller should exit nonzero without printing it again.
func alreadyReported(err error) bool {
	_, ok := errors.AsType[reportedError](err)
	return ok
}

// Package-private test seams for construction and staging cleanup failures.
var (
	newClientRemote     = newRemote
	removeClientStaging = os.RemoveAll
)

func commandInstaller(configPath, projectPath string) (*installer, project, func() error, error) {
	noop := func() error { return nil }
	if configPath == "" {
		var err error
		configPath, err = defaultPath()
		if err != nil {
			return nil, project{}, noop, err
		}
	}
	settings, err := load(configPath)
	if err != nil {
		return nil, project{}, noop, err
	}
	openedProject, err := openProject(projectPath)
	if err != nil {
		return nil, project{}, noop, err
	}
	staging, err := os.MkdirTemp("", "ts-skills-client-")
	if err != nil {
		return nil, project{}, noop, fmt.Errorf("create client staging directory: %w", err)
	}
	cleanup := func() error { return removeClientStaging(staging) }
	remote, err := newClientRemote(settings.registry, &http.Client{Timeout: 2 * time.Minute}, staging, tree.PrototypeLimits())
	if err != nil {
		return nil, project{}, noop, errors.Join(err, cleanup())
	}
	installer := &installer{remote: remote}
	return installer, openedProject, cleanup, nil
}

func commandError(operation string, err error) error {
	var message string
	if failure, ok := errors.AsType[*protocol.Failure](err); ok {
		switch failure.Code {
		case protocol.CodeNotFound:
			message = fmt.Sprintf("cannot %s because the requested skill publication was not found", operation)
		case protocol.CodeInvalidRequest:
			message = fmt.Sprintf("cannot %s because the registry rejected the request as invalid", operation)
		case protocol.CodeTooLarge:
			message = fmt.Sprintf("cannot %s because the registry could not return the skill within its response limit", operation)
		case protocol.CodeInternal:
			message = fmt.Sprintf("cannot %s because the registry could not complete the request", operation)
		case protocol.CodeUnavailable:
			message = fmt.Sprintf("cannot %s because the registry is temporarily busy; try again", operation)
		default:
			message = fmt.Sprintf("cannot %s because the registry returned an invalid response", operation)
		}
		return fmt.Errorf("%s: %w", message, err)
	}
	switch {
	case errors.Is(err, errBusy):
		message = fmt.Sprintf("cannot %s while another ts-skills process is changing this project; wait and try again", operation)
	case errors.Is(err, errLocalChanges):
		message = fmt.Sprintf("cannot %s because the installed skill differs from ts-skills.lock; restore it or move it aside, then try again", operation)
	case errors.Is(err, errProjectChanged):
		message = fmt.Sprintf("cannot %s because this project changed while the registry was being read; try again", operation)
	case errors.Is(err, tree.ErrLimitExceeded):
		message = fmt.Sprintf("cannot %s because the downloaded skill exceeds the configured safety limits", operation)
	case errors.Is(err, protocol.ErrInvalidResponse):
		message = fmt.Sprintf("cannot %s because the registry returned an invalid response", operation)
	default:
		message = fmt.Sprintf("%s failed", operation)
	}
	return fmt.Errorf("%s: %w", message, err)
}
