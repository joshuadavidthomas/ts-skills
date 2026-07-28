# Plan 019: Split daemon runtime construction from serving

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/daemon/ cmd/ts-skillsd/`
> Plans 002, 006, 014 legitimately change in-scope files — reconcile
> against landed work; on an unexpected mismatch, treat it as a STOP
> condition.

## Status

- **Effort**: M
- **Risk**: LOW (structural; behavior preserved)
- **Depends on**: 002-bound-daemon-shutdown.md (same file, lifecycle region)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

The daemon's shared run path folds two different jobs — "construct a valid
mode-specific runtime" and "serve and drain it" — into one seam, and the
join is awkward: `RunDev` fabricates a production `Config` with a fake
hostname, passes a factory that ignores its `Config` argument, and the
runner validates the fabricated production config the dev path doesn't even
use (daemon.go:222-254, 473-475). The factory can return a partly populated
`runtime`, and serving starts by checking a four-field completeness rule —
an illegal state that construction should make unrepresentable. Tests must
fake that whole apparatus to exercise serving. Two phases, two functions,
one valid-runtime handoff.

## Current state

`internal/daemon/daemon.go:222-254` — dev entry fabricates prod config and
reuses the production runner:

```go
// RunDev validates DevConfig, captures it in a factory ignoring its Config
// argument, builds Config{Hostname: defaultHostname, ...}, calls runWithHandlerGate,
// which validates that fabricated production Config before calling the factory.
```

`daemon.go:253-263` — incomplete-runtime detection + cleanup in the serving
path:

```go
active, err := factory(ctx, config)
...
if active == nil || active.listener == nil || active.handler == nil || active.close == nil {
	if active != nil && active.close != nil {
		return errors.Join(fmt.Errorf("build daemon runtime: factory returned an incomplete runtime"), active.close())
	}
	...
```

`daemon.go:405-408, 473-475` — `buildRuntime`/`buildDevRuntime` each
re-validate their configs (validation done twice in dev).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (package) | `go test -race ./internal/daemon/ -count=1` | all pass |
| Build | `just build` | exit 0 |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/daemon/daemon.go` (+ tests)
- `cmd/ts-skillsd/main.go` (only if signatures force it)

**Out of scope**:
- Shutdown bounding — plan 002 owns it and lands first; preserve its
  BaseContext/drain semantics when restructuring.
- Credential/diagnostics wiring — plan 014; construct-and-serve split must
  not pre-decide its config flow beyond composition order.
- Serving internals (handler gate, HTTP server config) beyond moving them.

## Steps

### Step 1: Extract a complete-runtime constructor per mode

End shape sketch:

```go
func Run(ctx context.Context, config Config) error          // validates config, builds prod runtime, serves
func RunDev(ctx context.Context, config DevConfig) error    // validates dev config, builds loopback runtime, serves

func serve(ctx context.Context, rt runtime, timeout ... ) error // shared: handler gate, HTTP server, drain
```

`buildRuntime` (production) and `buildDevRuntime` each validate ONCE
(their own mode's config) and return a `runtime` whose type makes
listener/handler/close non-optional by construction — no completeness
check in `serve`, no `runtimeFactory` indirection, no fabricated prod
config in the dev path.

**Verify**: `go build ./... && go test -race ./internal/daemon/ -count=1` → all pass.

### Step 2: Tests

Serving tests construct runtimes directly (real in-memory/loopback pieces —
exemplar: existing tests at daemon_test.go:62-168 already run a real HTTP
server with a blocked handler; they now build a runtime without the
factory choreography). Dev-mode tests assert dev validation happens once,
at `RunDev`.

**Verify**: `go test -race ./internal/daemon/ -count=1` → all pass.

### Step 3: Full gate

**Verify**: `rg "runtimeFactory" internal/daemon/` → no matches; `just check` → exit 0.

## Test plan

Covered in Step 2; the suite's existing behavior coverage (shutdown,
dev-mode loopback enforcement, actor mapping) is the regression net and
must pass unchanged except where the factory seam disappears from test
setup.

## Done criteria

- [ ] No fabricated production config in the dev path
- [ ] No incomplete-runtime detection in serving (construction makes it unrepresentable)
- [ ] Config validation runs once per mode
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- Plan 002 hasn't landed (or landed differently) and the shutdown
  lifecycle region doesn't match this plan's assumptions — reconcile to
  its actual final shape; don't move its semantics.
- External (test) callers exist that construct serving with deliberately
  incomplete runtimes for testing — those narrow to real runtimes; if a
  test *needs* the illegal state, describe why rather than preserving the
  seam.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Rule: validate-once-at-construction; serving receives only valid
  runtimes. New modes (if any) get their own constructor + shared `serve`.
- After plans 002/014/019 all land, daemon.go should read as:
  config-from-env → mode constructor → serve. Reviewers should keep new
  lifecycle steps inside one of those three phases.
