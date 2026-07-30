# 011 — Put curation identity and denial in their owning module

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Server audit: authorization denial is owned by catalog though catalog never produces it
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 002-delete-catalog-public-facade, 003-explicit-server-startup-config
**Executor target:** routine execution ready: no; confirm module depth first
**Source type:** audit
**Audit category:** architecture / security
**Standards concern:** authority, modules, and boundaries
**Impact:** Keeps Tailnet identity translation and authorization failures out of catalog vocabulary
**Effort:** M
**Risk:** MED; denial mapping is security- and user-visible behavior
**Confidence:** HIGH that catalog ownership is wrong; MED on whether a subpackage is deep enough
**Source direction:** Make a focused identity adapter own peer resolution, capability interpretation, and denial

## Purpose

Give Tailnet-to-curator translation and its expected denial failure one honest
owner while keeping catalog focused on publication state.

## What Better Means

Catalog defines curator domain values and validates them when mutating catalog
state, but does not define transport identity failures. The server identity
adapter resolves an HTTP peer into a curator or a classified denial. Web maps
that denial without importing an unrelated catalog error.

## Current-State Evidence

- `internal/server/catalog/errors.go:10` declares `errCurationDenied`, exported
  through the current façade.
- Catalog production code never produces that error.
- `internal/server/tailnet.go:78` produces the denial while resolving peer
  identity/capabilities.
- `internal/server/web/handler.go:459-463` interprets the catalog-owned sentinel.
- `internal/server/tailnet_test.go` tests capability and denial behavior around
  the Tailnet adapter.

## Desired End State

- One identity owner contains Tailnet peer lookup, capability interpretation,
  safe actor construction, and the denial failure.
- Catalog retains `Actor`/`Curator` as domain authority values but no
  transport-resolution error.
- Web receives a resolver whose expected denial can be classified without a
  parent-package import cycle.
- Development identity uses the same resulting curator contract without
  pretending to be a Tailnet adapter.
- HTTP status, rendered message, logging safety, and capability rules remain
  unchanged.

## Scope

- Tailnet actor resolver and tests
- Catalog curation-denial declaration removal
- Web resolver/error dependency and tests
- Server composition for production and development identity

## Out of Scope

- Changing Tailnet capability rules, dev identity, HTTP status, actor fields,
  catalog mutation authorization, or adding application accounts/tokens
- Moving catalog `Actor`/`Curator` domain values into transport code
- General authentication middleware

## Design Claim

Per `coding-standards/references/boundaries.md`, an external identity is
translated at the adapter seam. Per `state.md`, authority and denial must name
their owner. Per `modules.md`, a new package is justified only if it owns the
whole resolution/capability translation, not merely one error variable.

## Architecture Diagnosis

- **Current friction:** Web imports catalog to classify a failure catalog never
  emits, obscuring whether denial is a storage, domain, or identity result.
- **Deepening direction:** The identity adapter owns peer translation,
  capability policy, and expected denial.
- **Deletion test:** A package containing only `ErrDenied` fails. A module
  containing the resolver and policy would spread Tailnet translation back into
  server/web if deleted.
- **Locality / leverage claim:** Capability and identity-provider changes become
  local to one adapter and its contract tests.
- **Recommendation strength:** Worth exploring; confirm depth before package
  extraction.
- **ADR conflicts:** None; production identity remains tsnet peer identity.

## Implementation Sequence

### Step 1 — Characterize authority behavior

Pin peer-not-found, malformed peer, non-curator, curator, local-client failure,
development curator, and web denial mapping through existing seams.

### Step 2 — Choose the smallest honest owner

Prefer `internal/server/identity` only if the resolver, Tailnet adapter,
capability policy, and denial classification can move together without a
dependency cycle. Otherwise keep a concrete root-server identity module and
pass a typed resolver result into web. Do not extract an error-only package.

### Step 3 — Move resolution and denial

Relocate the expected denial and its producer. Remove it from catalog. Adapt
web options/constructor to classify the identity result without learning
Tailnet types.

### Step 4 — Collapse obsolete wrappers and tests

Delete pass-through identity helpers and tests that only assert old ownership;
retain capability and HTTP behavior tests.

## Verification

### Automated

- [ ] `go test ./internal/server/... -race -count=20`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] `rg -n "CurationDenied" internal/server/catalog` returns no matches.
- [ ] Catalog has no dependency on Tailnet identity failures.
- [ ] Non-curating peers receive the same status and safe message.
- [ ] Curator capability tests still exercise the real Tailnet adapter.
- [ ] No package was introduced solely to hold an error sentinel.

### Manual

- None.

## Autonomy Boundary

Routine work may move the existing resolver and sentinel after the ownership
shape passes the deletion test. Design review is required for package placement
and the resolver result type. Human approval is required for any capability,
identity, authorization, or HTTP behavior change.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plans 002 and 003 have landed.
- [ ] Inventory all curator resolver and denial call sites.
- [ ] Run repeated server tests before editing.

## STOP Conditions

Stop if the proposed identity package would contain only aliases/sentinels,
moving the resolver creates a dependency cycle, Tailnet capability rules are
not fully represented in tests, or denial behavior must change.

## Rejected Approaches

- Move only `ErrCurationDenied` to a new package — zero-depth package.
- Leave the sentinel in catalog because `Curator` lives there — a domain value
  does not make transport denial a catalog failure.
- Add generic auth middleware — there is one Tailnet identity model and
  route-specific curation behavior.

## Standing Policy Updates

Identity adapters own provider lookup and expected denial; catalog owns valid
curator values and catalog transition failures.

## Executor Notes

Keep the security policy visible. This is not a reason to weaken route-level
curator resolution or convert denial into a generic internal error.
