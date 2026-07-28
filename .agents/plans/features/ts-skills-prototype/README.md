# ts-skills Prototype

**Source roadmap item:** N/A
**Source improvement plan:** N/A
**Planned at:** 2026-07-27, working copy `xsprqlup` / `d8ede01a8663`
**Status:** Ready for execution
**Current gate:** Review the final executor plan’s resolved choices and type/interface contracts, then begin Phase 1.

## Purpose

Turn the accepted prototype shape in [`docs/SPEC.md`](../../../../docs/SPEC.md) into reviewable implementation slices without inventing the deferred storage, path, protocol, or deployment decisions.

## What Better Means

The work reaches one demonstrable path: a Tailnet user uploads one Agent Skill as a ZIP or browser-selected directory, reviews and publishes its exact tree, and another user installs and restores that tree into an explicit project by digest. Git/HTTP acquisition, user-wide scope, versions, and registry roles stay outside the prototype.

## Artifact Index

| Artifact | Status | Purpose | Notes |
|---|---|---|---|
| [001-structure-outline](001-structure-outline.md) | Accepted | Split the accepted spec into implementation slices | Six phase boundaries |
| [002-plan](002-plan.md) | Ready for execution | Define exact contracts and executor-safe implementation steps | Review resolved prototype choices before Phase 1 |

## Current Shape

- One persistent Tailnet registry with a basic upload/review/publish frontend.
- ZIP and browser directory inputs feed one normalized tree-capture path.
- Publications and project locks use `(SkillID, TreeDigest)` identity.
- One explicit project scope and one configured registry.
- Scaffold the settled values first, then prove an in-process curate-to-install path before adding adapters.

## Accepted Decisions

- SHA-256 tree identity covers normalized paths and file contents; source modes do not affect identity.
- V1 has no registry-assigned versions or release IDs.
- Candidates remain immutable; publications record approval; current selection is an explicit pointer.
- Every Tailnet user allowed to reach the prototype may upload and publish.
- Git, HTTP acquisition, and user-wide installation are deferred.

## Plan Resolutions

- SQLite metadata plus immutable digest-addressed tree directories.
- Explicit `.agents/skills/<name>` destination, project lock, user config, and strict TOML shapes.
- Root or one-wrapper ZIP input with fixed prototype upload limits.
- Journaled replacement, one physical writer, fail-closed local edits, and complete recovery-state rules.
- Tailnet TLS through `tsnet`; actual ACL, tag, auth key, MagicDNS, and HTTPS changes remain human-controlled deployment work.

## Implementation Routing

Execute [`002-plan.md`](002-plan.md) directly as six phases. It defines exact type and interface signatures, ownership and lifecycle rules, invariants, dependency direction, production/test implementations, adapter mappings, transaction recovery, and boundary contract tests. Stop and hand back when one of its STOP conditions applies.

## Rejected or Deferred

| Item | Reason | Revisit if |
|---|---|---|
| Git and HTTP import | They add acquisition and network policy before the core registry is proven | Manual upload becomes the main source of friction |
| User-wide install | It adds a second ownership and lock scope | Project installation is proven and global use is requested |
| Roles and revocation | Tailnet reachability is enough for the prototype | The team needs separation of duties or withdrawal controls |
| Human versions | Tree digests already give exact identity | Users need memorable aliases or compatibility signals |
