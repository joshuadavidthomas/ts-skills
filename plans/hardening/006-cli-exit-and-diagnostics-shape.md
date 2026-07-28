# Plan 006: CLI — exit once at main, print diagnostics once

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- cmd/ internal/cli/`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (execute after 005 per the index order)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Two violations of `exit-once-at-main.md` / `errors-are-values.md`:

1. `cmd/ts-skillsd/main.go` calls `os.Exit(1)` inside `main` **after**
   `defer stop()` is registered but in a way that skips it (os.Exit runs no
   defers), and the startup logic is a nested if-ladder with no testable
   error-returning boundary.
2. Flag parsing double-reports: the `flag` package writes usage/errors to
   stderr on `ContinueOnError`, then `main` prints the same error again.
   Worse, `-h` returns `flag.ErrHelp` up the stack and exits as a *failure*
   with an extra error line — help is not an error.

## Current state

`cmd/ts-skillsd/main.go` (whole function — nested config probing, then):

```go
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dev, err := daemon.DevModeFromEnv()
	...
	if err != nil {
		fmt.Fprintf(os.Stderr, "ts-skillsd: %v\n", err)
		os.Exit(1)
	}
```

`internal/cli/cli.go:47-53` (and the restore twin at 93-98):

```go
flags := flag.NewFlagSet("ts-skills install", flag.ContinueOnError)
flags.SetOutput(stderr)
...
if err := flags.Parse(args); err != nil {
	return err
}
```

`cmd/ts-skills/main.go` — the single exit point (this shape is the goal;
only the ErrHelp/double-print handling needs cli-side support):

```go
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "ts-skills: %v\n", err)
		os.Exit(1)
	}
}
```

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `just build` | exit 0 |
| Tests (package) | `go test -race ./internal/cli/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |
| Manual smoke | `go run ./cmd/ts-skills install --help` | usage text on stdout/stderr, exit 0 |

## Scope

**In scope**:
- `cmd/ts-skillsd/main.go`
- `cmd/ts-skills/main.go`
- `internal/cli/cli.go` + `internal/cli/cli_test.go`

**Out of scope**:
- Subcommand set, flag names, usage prose — no UX redesign.
- `commandError` mapping — plan 003 owns it.
- `commandInstaller` cleanup signature — plan 004 owns it (if 004 hasn't
  landed, this plan does not touch it; if it has, adapt).

## Steps

### Step 1: One diagnostics policy for flag parsing

In `internal/cli/cli.go` introduce an unexported marker for "already shown
to the user" errors, e.g.:

```go
type reportedError struct{ err error }
func (e reportedError) Error() string { return e.err.Error() }
func (e reportedError) Unwrap() error { return e.err }
```

- On `flags.Parse` errors: the flag package already printed cause+usage, so
  return `reportedError{err}` instead of a bare `err`.
- `errors.Is(err, flag.ErrHelp)` specifically: help was already printed; do
  NOT wrap — return nil from the subcommand path (help exits 0).

**Verify**: `go test -race ./internal/cli/ -count=1` → all pass.

### Step 2: main respects the marker

In `cmd/ts-skills/main.go`: on error, `errors.As` for the marker — if
present, `os.Exit(1)` silently; otherwise print once then exit. The
`unexported type + errors.As` from another package needs an exported
predicate: export `func AlreadyReported(err error) bool` from cli rather
than the type.

**Verify**: `just build` → exit 0;
`go run ./cmd/ts-skills install --help` → usage once, exit 0;
`go run ./cmd/ts-skills install` → error once, exit 1.

### Step 3: Extract a run function in ts-skillsd

Restructure `cmd/ts-skillsd/main.go`:

```go
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ts-skillsd: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// version fast-path, signal.NotifyContext, defer stop(),
	// env probing, daemon.Run/RunDev — all defers execute before return
}
```

The nested `DevModeFromEnv`/`DevConfigFromEnv`/`ConfigFromEnv` ladder
becomes straight-line early returns (`check-errors-early.md`). Behavior —
including the dev-mode stderr banner — stays byte-identical.

**Verify**: `just build` → exit 0; `go test -race ./internal/daemon/ -count=1`
→ all pass (daemon tests exercise `daemon.Run` directly; main stays thin).

### Step 4: Full gate

**Verify**: `just check` → exit 0.

## Test plan

- `internal/cli/cli_test.go`:
  - `Run(ctx, []string{"install", "-h"}, ...)` returns nil and writes usage to
    the provided stderr writer.
  - `Run` with unknown flags returns an error satisfying
    `cli.AlreadyReported`.
  - Existing command tests unchanged.
- ts-skillsd `run` is in package main; keep it thin enough that daemon
  tests cover the substance. Do not add a `main_test.go` just to wrap
  `exec` — the smoke commands above are the gate.

**Verify**: `go test -race ./internal/cli/ -count=1` → all pass, including new.

## Done criteria

- [ ] `rg "os.Exit" cmd/` → exactly two calls, one per main, both after all defers have run
- [ ] `go run ./cmd/ts-skills install --help` prints usage once, exits 0
- [ ] `go run ./cmd/ts-skills install` prints the error once, exits 1
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The "Current state" excerpts don't match (plan 004 may have changed
  `runInstall`/`runRestore` shape — reconcile, but if the parse paths moved
  somewhere else entirely, hand back).
- Treating `-h` as success breaks an existing test that asserts failure —
  that test is wrong-by-design; flag it rather than preserving it silently.
- The ts-skillsd env-probing ladder turns out to carry behavior that
  resists straight-line extraction (e.g. partial-config fallthrough
  semantics); handback, don't redesign.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- New subcommands inherit the policy for free as long as they reuse the
  same FlagSet construction — add a package comment in cli.go stating it:
  "flag diagnostics are printed by the FlagSet; errors are reported once."
- `AlreadyReported` is deliberately narrow; do not grow it into a general
  error-type taxonomy.
