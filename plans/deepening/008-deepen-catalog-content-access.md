# 008 — Deepen catalog content access

> **Executor instructions:** This plan contains an explicit design fork. Settle
> it in a short memo or plan update before production edits. If a STOP condition
> occurs, write a handback instead of improvising.

**Source item:** Project-wide design audit: `OpenTree` exposes a repeated lease and shutdown protocol
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 002-delete-catalog-public-facade, 004-explicit-tree-format-policy
**Executor target:** routine execution ready: no
**Source type:** audit
**Audit category:** architecture
**Standards concern:** modules and effects
**Impact:** Lets API and web consume catalog content without managing storage leases
**Effort:** M after design selection
**Risk:** MED; shutdown, concurrency admission, archive cleanup, and previews interact
**Confidence:** MED; the leak is clear, but callback and higher-level content operations have different tradeoffs
**Source direction:** Make catalog or a concrete content module own tree lease lifetime

## Purpose

Remove repeated open/use/close/log ordering from machine and browser handlers
without adding a speculative repository interface.

## What Better Means

Storage lease acquisition and release cannot be forgotten by a handler.
Catalog shutdown no longer depends on every caller manually following an
undocumented cleanup ritual. Browser preview and machine archive generation
retain their distinct resource/cost policies.

## Current-State Evidence

- Catalog `OpenTree` is exported through
  `internal/server/catalog/public.go:81` and implemented in
  `catalog/trees.go:215`.
- `catalog.openTree` increments `openTrees`; `Tree.Close` releases it.
- `catalog.Close` can return `ErrTreesOpen`.
- `internal/server/api/handler.go:96` opens a tree, coordinates work admission,
  creates an archive, closes the tree before response bytes, and joins errors.
- `internal/server/web/handler.go:233` and `:359` repeat open/defer-close/work
  admission for publication and candidate previews.
- There are two production consumers, so this is a real seam rather than
  hypothetical indirection.

## Desired End State

Before implementation, select one:

1. **Borrowed read callback:** catalog exposes a scoped read operation that
   verifies, leases, invokes a callback with `fs.FS`, and always releases; or
2. **Concrete content module:** a server-internal module owns catalog leasing,
   bounded work, preview resolution, and archive creation behind
   behavior-oriented operations.

Whichever shape is chosen:

- handlers cannot retain or forget to close catalog tree leases;
- shutdown and cleanup failures remain observable;
- API archive creation can close catalog storage before writing response bytes;
- browser previews remain size-bounded and cancellable;
- no catalog repository interface or generic content interface is introduced.

## Scope

- Design memo/update choosing the seam
- `internal/server/catalog` tree access
- `internal/server/api` archive consumption
- `internal/server/web` preview consumption
- Shared work-admission ownership if the chosen content module genuinely hides it

## Out of Scope

- Changing routes, response formats, templates, preview limits, archive format,
  digest verification, catalog persistence, or publication lifecycle
- Adding a cache or repository abstraction
- Splitting web files solely to reduce line count; do that after this seam lands
- Moving Tailnet identity or curation authority into content access

## Design Claim

Per `coding-standards/references/modules.md`, a real seam should let callers
forget lifecycle and ordering. Per `effects.md`, whoever acquires a resource
must own or explicitly transfer cleanup and cancellation.

## Architecture Diagnosis

- **Current friction:** Three handler workflows know storage reference counting,
  cleanup logging, work admission, and operation ordering.
- **Deepening direction:** A scoped lease or behavior-oriented content module
  owns those rules.
- **Deletion test:** Deleting the new seam should spread lease and cost policy
  back into both API and web; otherwise it is only a wrapper.
- **Locality / leverage claim:** Verification/lease/shutdown fixes have one owner
  and handlers return to protocol/rendering work.
- **Recommendation strength:** Worth exploring; choose the seam before coding.
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Compare the two shapes against real workflows

Trace API archive and both web preview paths. For callback and content-module
options, document interface burden, error propagation, cancellation, lease
duration, tree-work admission, test seam, and deletion-test result.

Choose the callback if lease ownership is the only common policy. Choose a
content module only if preview/archive operations can share meaningful bounded
content policy without web concerns leaking into API or catalog.

### Step 2 — Characterize behavior

Cover catalog shutdown with active work, close/error joining, request
cancellation, concurrent first verification, busy admission, bounded previews,
and archive cleanup through handler-visible tests.

### Step 3 — Implement the selected scoped interface

Remove raw closeable tree ownership from handlers. Keep concrete types and
local-substitutable filesystem/SQLite tests; do not introduce a repository
port.

### Step 4 — Improve locality after ownership moves

Split web preview workflows into same-package files/private operations only if
the selected seam leaves `handler.go` with clearly separable responsibilities.
Do not create new exported subpackages for file-size reasons.

## Verification

### Automated

- [ ] `go test ./internal/server/catalog ./internal/server/api ./internal/server/web ./internal/server -race -count=20`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] API and web handlers do not call `Close` on catalog tree leases.
- [ ] Catalog cannot finish shutdown while a scoped read is active.
- [ ] Tree-work limits and preview byte limits remain enforced.
- [ ] Archive responses still release catalog storage before response streaming.
- [ ] No new repository/content Go interface exists without a real adapter seam.

### Manual

- [ ] Review and record the callback-versus-content-module decision before edits.

## Autonomy Boundary

Routine execution is limited to analysis and behavior characterization until
the seam is selected. Design review is required for the selected interface,
work-admission ownership, and failure propagation. Human approval is required
for route, response, limit, shutdown, or publication behavior changes.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plans 002 and 004 have landed.
- [ ] Inventory all tree-open/close and tree-work admission call sites.
- [ ] Run the repeated narrow baseline.

## STOP Conditions

Stop if callback scope would require writing response bytes while holding the
catalog lease, a content module would need to depend on both HTTP rendering and
protocol response types, shutdown semantics cannot be preserved, or neither
shape passes the deletion test.

## Rejected Approaches

- Return another closeable wrapper with a different name — retains the cleanup
  protocol.
- Add a `Catalog` interface — SQLite is local-substitutable and there is one
  production implementation.
- Move preview rendering into catalog — mixes storage with browser presentation.
- Put all content behavior into web — API is a second real consumer.

## Standing Policy Updates

Catalog storage leases do not escape into HTTP handler orchestration.

## Executor Notes

The API intentionally materializes the archive before response writing so tree
close failures remain reportable. Preserve that ordering unless an equally
strong error contract replaces it.
