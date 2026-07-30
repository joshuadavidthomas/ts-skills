# 012 — Prune remaining shallow surfaces and choreography tests

> **Executor instructions:** Execute this plan last. Each deletion must pass its
> own production-caller and deletion test. If a purportedly shallow name
> carries a real contract, stop that item rather than forcing deletion.

**Source item:** Client/shared/server audits: zero-depth names, unused exports, pass-throughs, and layered tests
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 001 through 011
**Executor target:** routine execution ready: yes after dependencies
**Source type:** audit
**Audit category:** architecture / tests / DX
**Standards concern:** modules, maintainability, and verification
**Impact:** Leaves one coherent end-state vocabulary after the deeper changes land
**Effort:** M, composed of independently reviewable deletion slices
**Risk:** LOW; every item must preserve behavior and internal contracts
**Confidence:** HIGH for named current examples; re-audit required after prior plans
**Source direction:** Delete implementation-level exports, overlapping accessors, pass-through wrappers, and tests that only protect them

## Purpose

Finish the effort by removing shallow compatibility and test scaffolding that
the larger ownership changes make obsolete. This is not a generic
“simplification” sweep: every item needs a named production caller check.

## What Better Means

Public/internal package surfaces contain only names used by real callers or
carrying meaningful policy. Tests prove parsing, state transitions, protocol
behavior, durability, cleanup, and HTTP outcomes—not getters, aliases, blob
plumbing, or private helper call order.

## Current-State Evidence

- `internal/protocol/protocol.go` exports `NewCurrentResponse`,
  `ParseCurrentResponse`, `ParsePublicationHeaders`, `StatusForCode`, and
  `ReadFailure`; the audit found no production caller outside protocol for
  several of these implementation helpers.
- `internal/registry/inspection.go:38-46` exposes both `Directory()` and
  `Document()`, where the latter forwards through the former's value.
- `internal/client/model.go:35-37` wraps one `registry.PublicationID` in
  `lockedSkill`.
- `internal/client/project.go:119-121` defines `rejectLink` as a pass-through to
  broader path-component validation.
- `internal/server/router.go:17-21` aliases and wraps web CSRF key construction.
- `internal/server/tailnet.go:152-157` names a listener-returning method
  `listenerAddr`.
- Audits identified direct getter tests, flat entity/blob round trips, and
  private failure-hook tests layered beneath behavior-level coverage.

## Desired End State

- Protocol retains wire representations, routes, failure types/codes,
  caller-facing response readers/writers, and route parsing. Helpers used only
  to implement those operations become private.
- `registry.Inspection` has one non-overlapping route to each fact callers need.
- Zero-depth client/server wrappers left after prior plans are inlined, renamed
  to their true policy, or deleted.
- Tests of removed plumbing disappear; behavior and negative-path evidence
  remain at real seams.
- No compatibility aliases are retained because these packages are internal.

## Scope

- `internal/protocol` exported helper audit and tests
- `internal/registry.Inspection` accessors and callers
- Remaining named zero-depth client/server wrappers after plans 001–011
- Tests whose only claim is a removed alias/getter/helper or private choreography
- Documentation comments and `AGENTS.md` only when surfaces change

## Out of Scope

- Removing wire structs/header names used by integration tests or real callers
- Weakening durability/concurrency failure injection
- Deleting tests merely to reduce test count
- Changing protocol, CLI, registry identity, Agent Skill behavior, or HTTP output
- Abstracting tiny local duplicates such as `isLoopbackHost` or context readers

## Design Claim

Per `coding-standards/references/modules.md`, pass-through wrappers fail the
deletion test. Per `maintainability.md`, end-state refactors should not retain
internal aliases without compatibility obligations. Per `verification.md`,
tests prove caller-visible claims rather than private choreography.

## Architecture Diagnosis

- **Current friction:** Readers navigate overlapping names and tests freeze
  implementation phases already covered through deeper interfaces.
- **Deepening direction:** Keep the deep owning modules and narrow their
  interfaces to meaningful caller operations.
- **Deletion test:** Each named wrapper/accessor must be removable without
  spreading policy. If deletion spreads meaningful complexity, retain it.
- **Locality / leverage claim:** Future refactors touch fewer call sites and
  tests while preserving stronger behavior evidence.
- **Recommendation strength:** Strong for individually proven deletions
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Re-audit production callers

After plans 001–011, use `rg` and `go doc` to inventory exported protocol and
registry names plus remaining pass-through helpers. Record real production,
cross-package test, and same-package test callers.

### Step 2 — Prune protocol implementation helpers

Make construction, header parsing, status mapping, and failure decoding private
where they only implement retained `Read*`/`Write*` operations. Preserve wire
types/constants used to test or implement the shared contract. Update tests to
exercise retained operations.

### Step 3 — Remove overlapping and zero-depth names

Choose one coherent `Inspection` access pattern based on live callers. Inline
or delete `lockedSkill`, `rejectLink`, CSRF aliases, misnamed listener wrappers,
submission getters, and any equivalent pass-throughs still present. Do not
recreate them under new names.

### Step 4 — Replace, do not layer, test coverage

For each removed test, name the higher-level behavior test that preserves its
real claim. Keep failure hooks when they prove durability, concurrency, cleanup,
or shutdown outcomes; delete tests that only assert field/getter/blob
round-trips or obsolete call order.

## Verification

### Automated

- [ ] `go test ./... -race -count=20`
- [ ] `just lint`
- [ ] `just vet`
- [ ] `just tidy`
- [ ] `just check`

### Evals / Regression Checks

- [ ] Every removed export has no production caller that loses functionality.
- [ ] Protocol end-to-end and malformed-response tests still cover the shared
  contract.
- [ ] Registry inspection still binds parsed document and digest and verifies
  publication identity.
- [ ] Durability, recovery, concurrency, cleanup, and shutdown failure tests
  remain.
- [ ] No replacement pass-through aliases or test-only production seams were
  introduced.

### Manual

- None.

## Autonomy Boundary

Routine execution may delete a name only after proving it is pass-through or
implementation-only and retaining behavior evidence. Design review is required
when two access patterns have different ownership semantics. Human approval is
required for protocol, persisted data, CLI, security, or user-visible changes.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plans 001–011 are complete.
- [ ] Re-run caller inventories; do not trust the original line numbers.
- [ ] Run the full baseline before deleting anything.

## STOP Conditions

Stop an item if it has a real production caller outside its owning module,
removing it spreads policy or sequencing, a test is the only evidence for an
important failure contract, or deletion would change protocol/identity/user
behavior.

## Rejected Approaches

- Delete every one-line function — thin code can still encode policy or improve
  navigation.
- Move local duplicates into utility packages — textual duplication does not
  establish a semantic seam.
- Keep old exports for possible future callers — internal packages have no
  compatibility obligation without an actual caller.
- Delete failure-injection tests because they know phases — named durability
  phases are legitimate when they prove crash-consistency outcomes.

## Standing Policy Updates

Internal exported helpers need a production caller or a boundary contract;
tests alone do not justify production surface.

## Executor Notes

Run this after the ownership work because many named examples may already have
disappeared. The correct result may be a shorter plan diff than this inventory
suggests.
