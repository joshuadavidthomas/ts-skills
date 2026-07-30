# 006 — Settle the durable filesystem seam

> **Executor instructions:** This is a design-gate plan, not authorization to
> implement a guessed shared package. Produce the named memo and obtain design
> review. If a STOP condition occurs, write a handback instead of improvising.

**Source item:** Project-wide design audit: repeated sync/rename protocols lack an owner
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 004-explicit-tree-format-policy
**Executor target:** routine execution ready: no
**Source type:** audit
**Audit category:** architecture / correctness
**Standards concern:** effects, state, and modules
**Impact:** Prevents three modules from independently encoding crash-consistency rules
**Effort:** M design investigation; implementation to be estimated by the memo
**Risk:** HIGH; incorrect semantics can lose installed skills, lock state, keys, or catalog trees
**Confidence:** MED; duplication is certain, but the honest shared outcomes need caller-by-caller derivation
**Source direction:** Define outcome-level durable operations before creating any shared package

## Purpose

Resolve what is genuinely shared among the client, server, catalog, and tree
filesystem transitions. The output is an accepted design memo precise enough
to update plan 007 and create one or more independently landable
implementation plans.

## What Better Means

The project has a reviewed table of filesystem transitions, their security
preconditions, durability point, rollback obligations, failure states, and
platform assumptions. The proposed module interface lets callers request
outcomes instead of sequencing syscalls. A one-function `SyncDirectory`
utility package is explicitly avoided unless the memo proves that
platform-locality alone supplies sufficient leverage.

## Current-State Evidence

- `internal/client/fsutil.go:10-23` syncs a directory after enforcing managed
  path policy.
- `internal/server/files.go:8-15` and
  `internal/server/catalog/trees.go:189-197` implement the same syscall sequence
  without client policy.
- `internal/tree/stage.go:75-137` recursively syncs complete trees and rejects
  special entries.
- `internal/server/runtime.go:315-346` durably installs a CSRF key.
- `internal/client/installer.go:348-368` durably replaces the lock file.
- `internal/client/installer.go` and `writer.go` coordinate trash, rename,
  parent sync, rollback, and recovery.
- `internal/server/catalog/trees.go:85-175` installs immutable digest trees and
  records failure-injection phases.

## Desired End State

This plan produces `plans/deepening/memo-durable-filesystem-seam.md` containing:

- an inventory of every production durability transition;
- the exact outcome each caller needs;
- preconditions that stay with callers, especially client symlink/path policy;
- durability and commit points for files, directories, mkdir, and rename;
- what state is observable after every syscall failure;
- cancellation policy;
- Unix/Windows support assumptions and tests;
- proposed concrete Go types/functions with package ownership;
- deletion-test and caller-burden analysis for each proposed operation;
- implementation slices and required failure-injection coverage;
- rejected alternatives, including a generic filesystem interface.

The memo must conclude either:

1. a deeper shared module is justified and plan 007 can consume it;
2. only a small platform primitive is honestly shared, with an explicit
   leverage argument; or
3. the workflows should remain local and only naming/error semantics should be
   aligned.

## Scope

- Read-only analysis of client, server, catalog, and tree transitions
- Existing tests, failure hooks, platform files, and documented durability claims
- The design memo and updates to this plan bank

## Out of Scope

- Production source changes
- New filesystem packages or interfaces
- Changing recovery guarantees or deleting failure tests
- Cross-filesystem moves, network filesystems, or unsupported platforms unless
  current code claims to support them

## Design Claim

Per `coding-standards/references/modules.md`, a module earns its seam by hiding
meaningful complexity, not repeated syntax. Per `effects.md` and `state.md`,
durability, commit points, retry safety, and recovery states are part of the
operation's interface.

## Architecture Diagnosis

- **Current friction:** Callers independently remember that file sync is not
  enough, rename commit requires parent sync, and later failures may be
  rollback-safe or already committed.
- **Deepening direction:** Outcome-level operations own sequencing and expose
  meaningful failure/commit state.
- **Deletion test:** A package containing only `SyncDirectory` mostly moves
  three lines; a module owning complete transitions would spread crash
  consistency back into callers if deleted.
- **Locality / leverage claim:** Platform fixes and failure-state reasoning
  become centralized while domain security policies remain local.
- **Recommendation strength:** Worth exploring; shared outcome shape is not yet
  accepted.
- **ADR conflicts:** None known

## Implementation Sequence

### Step 1 — Inventory transitions

For each production call path, record inputs, output, preconditions, filesystem
operations, durability point, cleanup, rollback, and every injected failure.
Distinguish recursive tree sync from directory-entry sync.

### Step 2 — Derive semantic operations

Group only transitions with the same outcome and failure contract. Sketch
concrete Go functions and result types. Do not introduce an interface unless a
real adapter seam exists.

### Step 3 — Test the deletion and policy boundaries

For each proposed operation, show what caller knowledge disappears. Identify
client symlink/path validation, catalog immutable-tree identity, and server key
permissions as policies that may need to remain outside the shared
implementation.

### Step 4 — Write and review the memo

Create the memo, update plan 007 with the accepted contract and dependency, and
add implementation plan(s) if work should land before plan 007.

## Verification

### Automated

- [ ] `rg -n "Sync\\(|syncDirectory|Rename|CreateTemp|Mkdir|RemoveAll" internal --glob '*.go'` inventory is represented in the memo or explicitly excluded.
- [ ] `go test ./internal/client ./internal/server/... ./internal/tree -race -count=1` establishes the pre-design baseline.

### Evals / Regression Checks

- [ ] Every proposed operation states its commit point and post-failure state.
- [ ] Every current production call site maps to exactly one retained local or
  proposed shared owner.
- [ ] The memo does not propose a generic `Filesystem` interface.
- [ ] Test seams correspond to named durability phases or observable outcomes.

### Manual

- [ ] Maintainer reviews and accepts one of the three conclusions.

## Autonomy Boundary

Routine execution is limited to evidence gathering and writing the memo. Design
review is required for the proposed seam, types, error semantics, and package
ownership. Human approval is required if the proposal changes durability,
recovery, data-loss, platform, or compatibility guarantees.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plan 004 has landed.
- [ ] Run `jj st` and the baseline tests.
- [ ] Re-inventory filesystem calls; do not rely on line numbers alone.

## STOP Conditions

Stop if current recovery guarantees cannot be inferred from tests/comments,
different filesystems/platforms require contradictory contracts, an operation
would need to absorb client or catalog domain policy to be useful, or the
design cannot fit into independently landable slices.

## Rejected Approaches

- Immediately create `internal/durablefs.SyncDirectory` — deduplicates syntax
  without settling the semantic seam.
- Create a generic filesystem interface — no production adapter variation and
  it would broaden every caller.
- Move all filesystem work into `tree` — CSRF keys and lock files are not trees.
- Force all security preconditions into shared code — client and catalog trust
  policies differ.

## Standing Policy Updates

If accepted, add the durable-filesystem ownership rule to `AGENTS.md`.

## Executor Notes

This plan succeeds by making the implementation decision explicit, including a
well-supported decision not to abstract. Do not measure success by producing a
new package.
