# 002 — Delete the catalog public façade

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Project-wide design audit: `catalog/public.go` pass-through façade
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** none
**Executor target:** routine execution ready: yes
**Source type:** audit
**Audit category:** architecture / DX
**Standards concern:** modules and maintainability
**Impact:** Gives each catalog concept one name and makes implementation discoverable
**Effort:** M; mechanical but touches many catalog declarations
**Risk:** LOW; package is internal and behavior is unchanged
**Confidence:** HIGH; the façade contains aliases and forwarding only
**Source direction:** Export real declarations where implemented and delete `public.go`

## Purpose

Remove the same-package alias layer that makes every catalog type and method
exist under both a private and public name.

## What Better Means

The declaration found by “go to definition” is the real declaration. Catalog
tests and consumers use one vocabulary. No compatibility aliases or forwarding
methods remain.

## Current-State Evidence

- `internal/server/catalog/public.go:13-23` aliases ten private types.
- `public.go:26-31` aliases private sentinel errors.
- `public.go:33-83` forwards constructors and every public method.
- The actual implementation remains lowercase across `catalog.go`, `rules.go`,
  `records.go`, `trees.go`, `identity.go`, and related files.

## Desired End State

- Real catalog types, errors, constructors, and methods are exported in their
  owning files.
- `internal/server/catalog/public.go` is deleted.
- There are no old lowercase twins retained for tests or transition.
- Package behavior and its external consumer surface are unchanged.

## Scope

- All files in `internal/server/catalog`
- Direct catalog consumers under `internal/server`

## Out of Scope

- Changing catalog operations or adding a repository interface
- Moving curation-denial ownership; track that separately after this rename
- Changing SQL schema, migrations, persisted data, or publication semantics
- Redesigning `OpenTree`; plan 008 owns that seam

## Design Claim

Per `coding-standards/references/modules.md`, a pass-through wrapper is not a
module. Per `maintainability.md`, internal end-state changes should not leave
aliases or dual vocabulary without a real compatibility obligation.

## Architecture Diagnosis

- **Current friction:** Navigation lands on forwarding declarations, and every
  catalog concept has two names inside one Go package.
- **Deepening direction:** Preserve the deep catalog package while deleting its
  shallow internal façade.
- **Deletion test:** Deleting `public.go` removes no behavior; only declaration
  visibility must move.
- **Locality / leverage claim:** Each operation's interface and implementation
  become co-located.
- **Recommendation strength:** Strong
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Record the public surface

Use `go doc` or `rg` to list the currently exported catalog names. This is the
behavior-preserving target; do not opportunistically broaden it.

### Step 2 — Export declarations in place

Rename the real types, errors, constructors, and methods in their owning files.
Update receivers, internal references, tests, and server consumers coherently.
Keep storage helpers private.

### Step 3 — Delete the façade

Delete `public.go`. Retain the `fs.FS`/`Close` compile assertion near the real
tree-view declaration if it still provides useful contract evidence.

## Verification

### Automated

- [ ] `go test ./internal/server/catalog ./internal/server/... -race -count=1`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] `internal/server/catalog/public.go` no longer exists.
- [ ] `rg -n "^type .* = |^var .* = err|return c\\.[a-z]" internal/server/catalog` finds no compatibility façade left by this plan.
- [ ] The exported catalog surface is no broader than the recorded baseline.

### Manual

- None.

## Autonomy Boundary

Routine execution includes coherent renames and test updates. Design review is
required if a private declaration cannot be exported without also exposing a
storage or test-only detail. Human approval is required for any behavior,
persistence, or protocol change.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Run `jj st`.
- [ ] Re-open `public.go` and every declaration it forwards.
- [ ] Run the narrow tests before editing.

## STOP Conditions

Stop if external modules outside this repository consume this `internal`
package, the façade now translates behavior instead of forwarding, or a rename
requires a compatibility layer.

## Rejected Approaches

- Keep aliases “for clarity” — the aliases are the navigation problem.
- Split public and private declarations into more files — file placement is not
  a Go package seam.
- Add an interface around catalog — there is one concrete local adapter.

## Standing Policy Updates

Same-package public façades must own translation or policy; aliases and
forwarders alone do not qualify.

## Executor Notes

Use one coherent rename, not a staged dual-name migration. Preserve the
append-only migration ladder exactly.
