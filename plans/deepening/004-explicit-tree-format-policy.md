# 004 — Make tree format policy explicit

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Project-wide design audit: configurable limits mixed with implicit prototype policy
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** none
**Executor target:** routine execution ready: yes
**Source type:** audit
**Audit category:** architecture / correctness
**Standards concern:** domain model and boundaries
**Impact:** Ensures staging, hashing, encoding, decoding, and upload acceptance use one policy
**Effort:** M
**Risk:** MED; tree acceptance and digest compatibility must not change
**Confidence:** HIGH; implicit policy selection is directly evidenced
**Source direction:** Introduce a validated immutable tree format/policy value and remove hidden defaults

## Purpose

Make it impossible for two tree operations in the same publication flow to
silently apply different limits.

## What Better Means

One constructed value represents the current portable tree format and its
limits. All builders, sources, staging, archive sizing, encoding, decoding, and
registry inspection use that value explicitly. The name reflects a stable
format version rather than a prototype.

## Current-State Evidence

- `internal/tree/tree.go:30-38` exposes `PrototypeLimits`.
- `internal/tree/stage.go:18-22` selects those limits implicitly.
- `internal/tree/encode.go:14-25` selects them implicitly.
- `internal/registry/digest.go:49-53` selects them implicitly while hashing.
- `NewBuilder`, `NewSource`, `EncodeArchive`, `Decode`, browser upload, and the
  client remote accept caller-provided `Limits`.
- `internal/server/runtime.go:142,198` manually selects the prototype policy in
  both runtime modes.

## Desired End State

- A validated immutable value—prefer `tree.Format` with a canonical `tree.V1`
  constructor/value—owns limits and derived archive sizing.
- Tree operations take or are methods on that value; none reach for a global
  default internally.
- Custom limits remain available to focused tests through an explicit
  constructor, without allowing invalid policy values.
- Existing numerical limits, portable-path rules, archive representation, and
  digest output remain byte-for-byte compatible.
- “Prototype” vocabulary is removed if the format is now the stable v1 policy.

## Scope

- `internal/tree` policy, constructors, staging, encoding, and decoding
- `internal/registry` inspection/digest construction
- Client and server composition that supplies tree policy
- Tests and `AGENTS.md` architecture wording if names change

## Out of Scope

- Changing any limit value or accepted path rule
- Changing ZIP encoding, digest algorithm, publication identity, or protocol
- Combining the Agent Skill specification with tree portability policy
- Adding multiple format versions before a second version exists

## Design Claim

Per `coding-standards/references/domain-modeling.md`, an important policy
distinction must have one source of truth. Per `boundaries.md`, callers should
pass a refined constructed value rather than raw configuration that every
operation revalidates independently.

## Architecture Diagnosis

- **Current friction:** Callers cannot tell whether an operation honors their
  limits or silently chooses prototype policy.
- **Deepening direction:** The format value owns validation, limits, and derived
  operations while tree algorithms remain its implementation.
- **Deletion test:** Deleting the format value would reintroduce repeated
  validation and hidden/default policy selection across callers.
- **Locality / leverage claim:** A future format change has one policy owner and
  explicit composition points.
- **Recommendation strength:** Strong
- **ADR conflicts:** None; Agent Skill format and ts-skills portable tree policy
  remain separate.

## Implementation Sequence

### Step 1 — Pin compatibility

Add or identify tests proving the current canonical digest, encoded archive,
maximum archive calculation, and representative limit boundaries. These tests
must use caller-visible tree/registry operations.

### Step 2 — Construct the policy value

Make raw limits private to or validated by a format constructor. Expose access
only where a caller genuinely needs a derived fact. Prefer methods or functions
that take `Format` over a Go interface.

### Step 3 — Remove hidden defaults

Thread the constructed format through staging, encoding, registry inspection,
client remote construction, server web/API construction, and tests. No
operation should call the canonical v1 constructor behind the caller's back.

### Step 4 — Retire prototype vocabulary

Rename the canonical policy to stable v1 terminology and update architecture
documentation. Do not retain aliases without a real compatibility obligation.

## Verification

### Automated

- [ ] `go test ./internal/tree ./internal/registry ./internal/client ./internal/server/... -race -count=1`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`
- [ ] `just tidy`
- [ ] `[ -z "$(jj diff --name-only go.mod go.sum)" ]` — module files did not drift.

### Evals / Regression Checks

- [ ] Existing digest and archive golden behavior is unchanged.
- [ ] `rg -n "PrototypeLimits\\(" internal --glob '*.go'` returns no production call sites.
- [ ] No tree operation silently constructs v1 policy internally when its caller already has one.
- [ ] No exported Go interface was introduced for the format.

### Manual

- None.

## Autonomy Boundary

Routine execution may reshape internal constructors and update all in-repo
callers. Design review is required if `Limits` must remain independently public
for a production caller. Human approval is required for any change to accepted
trees, numerical limits, digest, archive bytes, or wire behavior.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Re-run the compatibility tests before editing.
- [ ] Inventory every production `PrototypeLimits` and raw `Limits` call site.
- [ ] Confirm no external module can import these `internal` packages.

## STOP Conditions

Stop if current operations intentionally use different policies, compatibility
tests reveal policy-dependent digest/archive behavior not captured here, or the
change requires supporting multiple wire/tree versions.

## Rejected Approaches

- Replace `PrototypeLimits()` with a renamed global call everywhere — changes
  vocabulary without making policy ownership explicit.
- Put Agent Skill rules into `tree.Format` — violates the existing spec/package
  separation.
- Add a format interface — there is one format and no adapter seam.

## Standing Policy Updates

Every publication-tree operation receives an explicit constructed format
policy; format implementations do not select ambient defaults.

## Executor Notes

Start with compatibility evidence. This plan must be a semantic no-op even
though it changes many signatures.
