# 007 — Put the complete project mutation transaction behind one interface

> **Executor instructions:** This plan is not routine-ready until plan 006's
> memo is accepted and this plan is reconciled with it. Do not guess durability
> semantics. If a STOP condition occurs, write a handback.

**Source item:** Project-wide design audit: installer and writer split transaction ownership
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 005-own-archive-receive-lifecycle, 006-settle-durable-filesystem-seam
**Executor target:** routine execution ready: no, pending plan 006 acceptance
**Source type:** audit
**Audit category:** architecture / tests
**Standards concern:** state, effects, and verification
**Impact:** Localizes installation, lock commit, rollback, and recovery ordering
**Effort:** L; split into independently reviewable slices after design reconciliation
**Risk:** HIGH; this code protects installed content and recovery after crashes
**Confidence:** HIGH in the diagnosis; implementation shape depends on plan 006
**Source direction:** Keep `install`/`restore` as the small caller interface and move the full mutation protocol behind project-owned outcomes

## Purpose

Let command/install orchestration state the desired publication outcome without
manually coordinating private project-writer phases.

## What Better Means

The project mutation module owns destination checks, staging into the managed
filesystem, publication revalidation, replacement, lock commit, rollback,
trash recovery, and cleanup. Tests exercise install/restore outcomes and named
failure phases rather than replacing raw `rename` and `syncDirectory`
functions.

## Current-State Evidence

- `internal/client/installer.go:26-70` sequences fetch, acquire, inspect, stage,
  verify, lock update, replacement, and cleanup.
- `installer.go:73-156` unlocks around remote work, relocks, compares a plan,
  and applies restoration.
- `internal/client/writer.go:58-69` exposes many private phase operations and
  raw `syncDirectory`/`rename` function fields.
- `installer.go:204-368` continues writer transaction logic in a second file.
- `internal/client/installer_test.go` directly constructs staging states,
  overrides syscall helpers, and calls replacement phases.
- The external client seam at `internal/client/run.go:22` is already deep and
  should remain unchanged.

## Desired End State

- `installer` asks a project transaction to install or reconcile an intended
  verified publication.
- One module owns phase ordering, persisted lock updates, commit points,
  rollback, recovery, and resource cleanup.
- Remote I/O remains outside project locks.
- Fetch returns the single owned snapshot from plan 005.
- Durability operations and outcomes follow the accepted plan 006 memo.
- Failure injection uses private named phases tied to state transitions.
- Behavior tests cross install/restore/project outcomes; tests do not freeze
  helper choreography.
- `lockedSkill`, `rejectLink`, and obsolete phase wrappers are removed when
  their owning paths are reshaped.

## Scope

- `internal/client/installer.go`, `writer.go`, `project.go`, `model.go`,
  `fsutil.go`, and related tests
- Private types representing intended mutation, transaction result, and named
  failure phase
- Plan-bank reconciliation after plan 006

## Out of Scope

- Changing CLI commands, output, lock-file schema, destination naming, registry
  protocol, publication verification, or symlink policy
- Holding project locks during network requests
- Splitting `internal/client` into exported/public subpackages
- Introducing a project-writer Go interface solely for tests

## Design Claim

Per `coding-standards/references/state.md`, transition rules and phase-specific
data belong to the transition owner. Per `effects.md`, commit points, retries,
locks, cleanup, and recovery are part of the effect contract. Per
`verification.md`, tests should prove caller-visible outcomes rather than
private ordering.

## Architecture Diagnosis

- **Current friction:** Installer callers must know writer phases and their
  legal order across two files.
- **Deepening direction:** Project mutation accepts a desired publication state
  and owns the full local transaction.
- **Deletion test:** Deleting this deep transaction would spread validation,
  replacement, lock commit, rollback, and recovery back into install/restore.
- **Locality / leverage claim:** A change to project crash recovery or lock
  semantics can be understood and tested in one workflow owner.
- **Recommendation strength:** Strong, contingent on plan 006
- **ADR conflicts:** None; preserves the existing no-network-under-lock rule

## Implementation Sequence

### Step 1 — Reconcile with the durability memo

Update this plan with the accepted operation/result types, commit points, and
implementation slices from plan 006. If no shared module is accepted, state
which durability transitions remain project-owned.

### Step 2 — Characterize observable transaction outcomes

Before structural edits, ensure behavior-level tests cover fresh install,
upgrade, no-op, conflict, stale plan, lock write failure, pre-commit rename
failure, post-commit sync failure, rollback success/failure, trash recovery,
cancellation, and restore with remote work outside the lock.

### Step 3 — Introduce an intended-mutation value

Represent the verified publication, managed destination, staged ownership, and
expected prior state without nullable or invalid phase combinations. Keep it
private and Go-native; avoid an interface.

### Step 4 — Move complete transitions behind project ownership

In reviewable slices, move staging/revalidation, replacement/rollback, lock
commit, and recovery so the outer installer no longer calls individual writer
phases. Preserve explicit unlock/relock around fetch.

### Step 5 — Replace choreography test seams

Replace raw syscall function fields with a private named phase hook only where
failure injection is necessary. Delete tests and helpers that assert obsolete
call order; retain or strengthen outcome assertions.

### Step 6 — Remove obsolete vocabulary

Delete zero-depth wrappers/types made unnecessary by the new transaction.
Improve internal file locality without creating new exported package seams.

## Verification

### Automated

- [ ] `go test ./internal/client -race -count=20`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`
- [ ] `just tidy`
- [ ] `[ -z "$(jj diff --name-only go.mod go.sum)" ]` — module files did not drift.

### Evals / Regression Checks

- [ ] No network operation occurs while the project lock is held.
- [ ] Every injected failure leaves the project in an asserted recoverable or
  explicitly committed state.
- [ ] Tests do not override raw `os.Rename` or directory-sync functions unless
  the accepted memo explicitly requires that seam.
- [ ] `installer.install` and `restore` no longer replay local filesystem phase
  checklists.
- [ ] Lock-file bytes and CLI behavior remain compatible.

### Manual

- None beyond reviewing the accepted durability contract.

## Autonomy Boundary

Routine execution begins only after plan 006's memo is accepted and this plan
names concrete slices. Design review is required at each commit-state/error
contract change. Human approval is required for any recovery guarantee,
lock-file schema, data-loss exposure, platform support, or user-visible
behavior change.

## Drift Checks

- [ ] Re-read this plan, the effort index, and the accepted plan 006 memo.
- [ ] Confirm plans 005 and 006 are complete.
- [ ] Run `jj st` and the repeated client baseline.
- [ ] Re-open every failure-injection test and map it to an observable outcome.

## STOP Conditions

Stop if plan 006 has no accepted conclusion, a transaction phase lacks a
defined post-failure state, network work would move under the lock, the lock
schema/protocol must change, or the work cannot be split into reviewable
behavior-preserving commits.

## Rejected Approaches

- Merely move methods between `installer.go` and `writer.go` — improves file
  layout without changing the caller interface.
- Add a public writer/repository interface — no real adapter seam exists.
- Remove failure injection wholesale — crash consistency requires focused
  negative-path evidence.
- Lock around fetch for simplicity — violates the existing concurrency design.

## Standing Policy Updates

Project mutation owns local transaction and recovery ordering; installers
provide intent and remote artifacts but do not call filesystem phases.

## Executor Notes

Treat recovery behavior as the contract. File movement is incidental
implementation only after every commit and rollback state is named.
