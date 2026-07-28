# Plan 015: Fetch before taking the project writer lock; make recovery visible in failure contracts

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/install/ internal/cli/`
> Plans 001, 011 legitimately change in-scope files — reconcile against
> landed work; on an unexpected mismatch, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: MED (concurrency semantics of project updates)
- **Depends on**: none hard; MUST land before 008; sequence after 011 (same files)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Two legibility failures in the install flow. (1) `Install` and `Restore`
hold the exclusive project writer across `Remote.Fetch` — registry latency
blocks every other local project operation, and
`ErrBusy` answers "waiting on HTTP", not "another process is writing".
A test (`transaction_test.go:690-741`) currently codifies the bad
semantics. (2) Writer acquisition silently performs recovery of a prior
abandoned transaction — it can rename destinations and rewrite the lock —
then if the *requested* command fails, the CLI prints "project files were
not changed" (cli.go:160-169), which is false. The failure contract lies
about consequences.

## Current state

Writer-then-fetch — `internal/install/installer.go:41-55`:

```go
	writer, err := project.acquireWriter(ctx)
	...
	oldLock, _, _, err := writer.readLock()
	matchingDestination, err := writer.preflight(oldLock, requirement.Skill())
	...
	fetched, err := i.remote.Fetch(ctx, requirement)
```

`Restore` does the same per-missing-skill inside the lock
(`installer.go:86-124`). The production remote is HTTP with a 2-minute
timeout (`internal/cli/cli.go:139`), so the lock can be held minutes.

Recovery hidden in acquisition — `internal/install/writer.go:140-148`:
acquire runs orphan cleanup and journal recovery before returning;
recovery mutates (`transaction.go:630-716`). `Install` then fails on
identity/digest/protocol with messages the CLI renders as "project files
were not changed".

CLI mapping at `internal/cli/cli.go:160-169` (plan 003 may have edited the
switch; the "did not match / not changed" branches are the target text).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/install/ ./internal/cli/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/install/installer.go`, `internal/install/writer.go`,
  `internal/install/project.go` (if the writer API changes)
- `internal/install/model.go` (if the failure contract adds a typed fact)
- `internal/cli/cli.go` (message for the recovered-then-failed case)
- tests in those packages

**Out of scope**:
- transaction.go internals/recovery policy — plan 011 (land that first;
  this plan consumes whatever it produces).
- Remote behavior, protocol — fetch semantics unchanged.
- Holding-lock-while-verifying staging — staging under the writer is
  correct (it owns the state dir); only the network fetch moves out.

## Steps

### Step 1: Install — fetch unlocked, then lock and revalidate

Reorder `Install`: `Fetch` first (no writer), then `acquireWriter`,
`readLock`, `preflight`, `stageAndVerify`, and the existing
identity/digest checks — all AFTER the fetch but still validated against
the *requirement*, not the old lock. The pre-lock read of the old lock
(for the "already installed" short-circuit) can move post-acquisition; the
idempotent fast-path still exists, now purely a lock-bearing check.

**Verify**: `go test -race ./internal/install/ -run 'Install|Writer|Concurrent' -count=1` → all pass.

### Step 2: Restore — plan under lock, fetch unlocked, verify under lock

`Restore` becomes three phases: (a) acquire writer, read lock, compute the
missing set, capture lock bytes + destination state, release writer; (b)
fetch + stage-verify all missing publications unlocked; (c) reacquire
writer, require the live lock and preflight state to match the captured
snapshot (else fail with a typed "project changed during restore" error —
fail closed, user retries), then install. Keep existing
`ErrBusy` semantics for contemporaneous writers.

**Verify**: `go test -race ./internal/install/ -run 'Restore' -count=1` → all pass.

### Step 3: Recovery consequences enter the failure contract

Have writer acquisition report whether recovery changed anything
(unexported fact on the writer, or an out-param — smallest honest
mechanism). When Install/Restore then fails, wrap the error with a typed
"recovered a prior interrupted update" fact — new sentinel in install,
e.g. `ErrRecovered`, wrapped alongside the real failure via a small error
type or `errors.Join` (Join keeps both matchable; prefer a dedicated
wrapping that reads well in the CLI). `commandError` in cli renders it:
"... note: a previously interrupted update was recovered; changed: <the
recovered state>" — match the existing message style at cli.go:152-169.

**Verify**: `go test -race ./internal/install/ ./internal/cli/ -count=1` → all pass.

### Step 4: Fix the codified bad semantics + full gate

Update `TestProjectWriterExcludesConcurrentInstaller`
(`transaction_test.go:690-741`): it now asserts non-overlap of *writers*,
not of writer-with-blocked-remote; a blocked remote must no longer
exclude another process's install of a different skill... unless it
mutates the same project — writers still exclude. Reshape honestly to the
new guarantee.

**Verify**: `just check` → exit 0.

## Test plan

- Concurrent install: installer blocked in a fake remote does NOT hold the
  project writer (a second process-shaped writer can proceed — the current
  test's inversion, keeping its remote-blocking fixture at
  transaction_test.go:701-719).
- Restore: mid-restore lock mutation → typed "project changed" error, no
  partial install (assert final tree/lock agreement like the existing
  agreement tests at transaction_test.go:115-163).
- Recovery-then-failure: inject prior journal (existing fixtures) + failing
  fetch → error matches both the real cause and the recovery fact; CLI
  message asserts the new wording.

**Verify**: `go test -race ./internal/install/ ./internal/cli/ -count=1` → all pass.

## Done criteria

- [ ] `Remote.Fetch` is never called while the project writer is held
- [ ] Restore revalidates captured state on reacquisition
- [ ] A recovered-then-failed command tells the user recovery happened
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- `stageAndVerify`'s staging directory lives inside the writer-owned state
  dir in a way that genuinely requires the lock during staging (the plan
  assumes staging can happen unlocked as long as the *mutation* of the
  project is locked — if staging-in-state-dir conflicts with another
  process's writer, say so and describe the isolation options instead of
  picking one).
- Plan 011 hasn't landed and the journal model is still flat — recovery
  facts are then guesswork; land 011 first (index says so).
- Fetch-unlocked surfaces a correctness hole: the **content** fetched for
  a "current" publication can change between fetch and lock. (It can't by
  design — publications are immutable digest-addressed; verify this in
  registry before proceeding, and if violated, STOP.)
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- The invariant to defend in review: the writer lock spans *local project
  mutation only*. Any future call inside the lock that crosses a network or
  another process's pace needs explicit justification.
- `ErrBusy` now honestly means concurrent mutation; its CLI wording
  ("wait and try again") stays accurate.
