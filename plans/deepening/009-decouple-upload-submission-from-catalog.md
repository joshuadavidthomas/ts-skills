# 009 — Decouple browser submission ownership from catalog capture

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Server audit: upload staging exposes concrete snapshot ownership and shallow getters
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 002-delete-catalog-public-facade, 004-explicit-tree-format-policy
**Executor target:** routine execution ready: yes
**Source type:** audit
**Audit category:** architecture
**Standards concern:** modules, effects, and boundaries
**Impact:** Keeps temporary upload ownership in web while catalog borrows only the tree view it needs
**Effort:** S
**Risk:** LOW; capture behavior and cleanup ordering remain unchanged
**Confidence:** HIGH; catalog does not need snapshot ownership operations
**Source direction:** Pass a borrowed `fs.FS` plus root into capture and remove getter ceremony

## Purpose

Stop the catalog capture interface from depending on the concrete temporary
staging type owned by browser upload code.

## What Better Means

Web owns and closes the staged upload. Catalog borrows a filesystem plus root
for the duration of `Capture`, inspects and materializes it synchronously, and
does not learn `tree.Snapshot`. The private `submission` exposes behavior or
direct same-package fields rather than four shallow accessors.

## Current-State Evidence

- `internal/server/web/upload.go:18-56` defines `submission` and four accessors
  around `*tree.Snapshot`.
- `internal/server/web/handler.go` unwraps those accessors to construct a
  catalog capture request.
- `internal/server/catalog/rules.go:13-19` requires
  `Staged *tree.Snapshot`.
- `catalog.capture` only calls `request.Staged.FS()` and uses `request.Root`; it
  does not close, transfer, or otherwise use snapshot ownership.

## Desired End State

- Catalog capture input contains the smallest borrowed tree representation it
  consumes: an `fs.FS` and root, or one equivalent behavior-oriented value.
- The capture call does not retain the borrowed filesystem beyond its return.
- Web retains the single cleanup obligation.
- `submission` getters that only reveal private fields are deleted.
- Capture tests use `fstest.MapFS`, `os.DirFS`, or another caller-realistic
  filesystem rather than manufacturing `tree.Snapshot` when staging behavior
  is not under test.

## Scope

- `internal/server/web/upload.go`, upload/capture handler code, and tests
- `internal/server/catalog/rules.go`, capture request declaration, and tests
- Documentation/comments describing borrow and cleanup ownership

## Out of Scope

- Changing upload parsing, tree limits, candidate identity, catalog
  materialization, or publication workflow
- Moving upload staging into catalog
- Making `submission` or capture input public outside the existing internal
  package seams
- Redesigning read-side tree leasing; plan 008 owns that

## Design Claim

Per `coding-standards/references/modules.md`, dependencies should expose the
smallest meaningful behavior the consumer uses. Per `effects.md`, the creator
of a temporary snapshot owns cleanup unless ownership is explicitly
transferred.

## Architecture Diagnosis

- **Current friction:** Catalog depends on a concrete ownership type while web
  immediately decomposes a private wrapper through getters.
- **Deepening direction:** Web owns the temporary artifact; catalog borrows a
  read-only filesystem for one capture operation.
- **Deletion test:** Deleting the getter façade loses no behavior. Deleting the
  borrow contract would force catalog to understand staging ownership again.
- **Locality / leverage claim:** Upload lifetime changes stay in web; catalog
  capture tests can focus on inspection and persistence.
- **Recommendation strength:** Strong
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Pin synchronous borrow behavior

Add or identify a test proving capture has completed all filesystem reads
before returning and web closes the submission afterward on success and error.

### Step 2 — Narrow catalog capture input

Replace the concrete snapshot field with `fs.FS` plus root, or an equally small
concrete borrowed-tree value. Validate nil/empty input at the constructor or
capture boundary.

### Step 3 — Simplify web submission

Construct the capture request from same-package fields or one behavior-oriented
method. Delete `Snapshot`, `Root`, and `Label` getters that no longer carry a
contract. Retain `Close` because it owns real cleanup.

### Step 4 — Replace plumbing tests

Delete direct getter tests and retain behavior tests for upload parsing,
capture, cleanup, and errors through web/catalog seams.

## Verification

### Automated

- [ ] `go test ./internal/server/web ./internal/server/catalog ./internal/server -race -count=1`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] `internal/server/catalog` has no production dependency on
  `*tree.Snapshot`.
- [ ] Web closes every submission exactly once on success and failure.
- [ ] Catalog does not retain the borrowed filesystem after `Capture` returns.
- [ ] Candidate identity and captured digest tests remain unchanged.

### Manual

- None.

## Autonomy Boundary

Routine execution may narrow private request types and replace shallow tests.
Design review is required if catalog needs ownership beyond the synchronous
call. Human approval is required for candidate, persistence, upload, or cleanup
behavior changes.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plans 002 and 004 have landed and update names accordingly.
- [ ] Inventory all capture-request and submission accessor call sites.
- [ ] Run narrow tests before editing.

## STOP Conditions

Stop if catalog stores the staged filesystem for deferred work, capture can
return before materialization finishes, or narrowing the input requires
changing the tree-validation contract.

## Rejected Approaches

- Move snapshot cleanup into catalog — catalog did not create the temporary
  resource.
- Add an interface matching `Snapshot.FS`/`Close` — combines borrowing and
  ownership and has one adapter.
- Keep getters for “encapsulation” — same-package pass-through methods add no
  policy.

## Standing Policy Updates

Catalog capture borrows tree content synchronously; the staging module owns the
artifact lifetime.

## Executor Notes

Do not weaken validation merely because catalog now receives `fs.FS`. It must
continue to inspect and materialize the tree before returning.
