# 010 — Give client command construction an owned session

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Client audit: command construction exposes cleanup and mutable global test seams
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 005-own-archive-receive-lifecycle, 007-deepen-client-project-transaction
**Executor target:** routine execution ready: yes after dependencies
**Source type:** audit
**Audit category:** architecture / tests
**Standards concern:** modules, effects, and verification
**Impact:** Makes temporary staging and cleanup one command-lifetime obligation
**Effort:** S
**Risk:** LOW; install/restore behavior remains behind existing commands
**Confidence:** HIGH; mutable globals have no production variation
**Source direction:** Replace four-value construction and package globals with a private owned command session

## Purpose

Make one private value own the project, installer, staging directory, and
cleanup used by an install or restore command.

## What Better Means

Command handlers construct one session, defer one `Close`, and invoke an
outcome-level install or restore operation. Cleanup errors are joined once by
the session caller. Tests inject construction dependencies privately without
mutating process-global variables.

## Current-State Evidence

- `internal/client/run.go:89-98` and `:121-129` unpack installer, project,
  cleanup function, and error, then repeat cleanup joining.
- `run.go:159-163` defines mutable package globals for remote construction and
  staging removal.
- `run.go:165-196` returns four values and must manufacture a no-op cleanup on
  early errors.
- `internal/client/run_test.go:23` mutates and restores those globals to test a
  construction/cleanup failure.

## Desired End State

- A private constructed command-session value owns staging, project, remote,
  and installer/project-transaction collaborators.
- `Close` is idempotent enough for normal deferred cleanup and reports removal
  failure.
- Install and restore command handlers do not unpack parallel lifecycle values.
- Construction cleanup on partial failure is internal to the constructor.
- No mutable package-level function variables remain for this lifecycle.
- Tests cover observable command and cleanup outcomes through private
  constructor dependencies or filesystem behavior, not global mutation.

## Scope

- `internal/client/run.go` and tests
- Private command session/dependency types, placed within `internal/client`
- Removal of `newClientRemote` and `removeClientStaging` globals

## Out of Scope

- Changing CLI flags, output, errors, exit behavior, default config, remote
  policy, or project mutation semantics
- Creating an exported session or Go interface
- Reopening plan 007's transaction design

## Design Claim

Per `coding-standards/references/modules.md` and `effects.md`, resource
construction and cleanup need one owner. Per `verification.md`, production
globals should not exist solely to expose private choreography to tests.

## Architecture Diagnosis

- **Current friction:** Every command must correlate three returned values and a
  cleanup callback, while tests mutate shared process state.
- **Deepening direction:** A private command-lifetime module hides construction,
  partial cleanup, and final cleanup behind one value.
- **Deletion test:** Deleting the session would spread staging and cleanup
  ordering back into install and restore.
- **Locality / leverage claim:** New client commands can reuse one lifecycle
  without duplicating cleanup or adding globals.
- **Recommendation strength:** Strong
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Characterize command and cleanup behavior

Preserve output/error tests for install, restore, partial construction failure,
operation failure plus cleanup failure, and success plus cleanup failure.

### Step 2 — Construct one session

Introduce a private concrete session and a private dependency value accepted
only by its constructor or test helper. The production constructor supplies
real filesystem and remote operations without mutable globals.

### Step 3 — Simplify command handlers

Replace four-value unpacking with one session, one deferred close/error join,
and one behavior call. Keep user-facing error translation in command handling.

### Step 4 — Delete global test seams

Remove package variables and global mutation tests. Verify tests can run in
parallel without shared state.

## Verification

### Automated

- [ ] `go test ./internal/client -race -count=20`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] `rg -n "newClientRemote|removeClientStaging" internal/client` returns no
  production matches.
- [ ] Install and restore each have one visible session cleanup obligation.
- [ ] Cleanup failures remain joined with operation failures.
- [ ] Client tests can use `t.Parallel` without shared construction state.

### Manual

- None.

## Autonomy Boundary

Routine execution may add private concrete types and private constructor
dependencies. Design review is required if the session starts absorbing flag
parsing or error presentation. Human approval is required for CLI, config,
network, or cleanup behavior changes.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plans 005 and 007 have landed.
- [ ] Re-open run tests and inventory package-level mutable variables.
- [ ] Run repeated client tests before editing.

## STOP Conditions

Stop if plan 007 already provides an equivalent command lifetime, a test seam
would need public surface, or preserving cleanup diagnostics requires changing
the client error contract.

## Rejected Approaches

- Keep the cleanup callback inside another four-value tuple — retains lifecycle
  correlation burden.
- Add a public `Session` interface — there is one production implementation.
- Keep mutable globals but guard them with a mutex — makes test-only variation
  safer without making it meaningful.

## Standing Policy Updates

Command-scoped resources are owned by one private session; tests do not mutate
package globals to replace construction.

## Executor Notes

Do not let the session become a second command parser. It owns resources and
operation collaborators, while `Run` continues to own command interpretation
and diagnostics.
