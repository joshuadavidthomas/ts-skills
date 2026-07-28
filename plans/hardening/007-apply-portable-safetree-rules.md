# Plan 007: Apply Windows path restrictions on every platform in safetree

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/safetree/ internal/upload/`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: S
- **Risk**: MED (behavior change: registries reject trees they previously accepted)
- **Depends on**: none (execute after 006 per the index order)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

This is a cross-platform correctness gap, not a style issue. The registry
is shared over a tailnet, and digests are the publication address. A
Linux-hosted daemon currently accepts and publishes trees with
Windows-reserved names (`CON.txt`) or case-colliding paths (`Readme.md` +
`README.md`) — with digests computed happily — while no Windows client can
ever stage or install those trees. The platform that *builds* a tree should
not determine what's *publishable*; the safe intersection of filesystem
rules is a property of the content, per the package-cohesion note
(`package-names-and-cohesion.md`).

**Key assumption** (STOP if false, see below): published trees are intended
to be installable by Windows clients, so the registry should enforce the
intersection everywhere.

## Current state

`internal/safetree/path_other.go` (non-Windows builds — stubs that neuter
the rules):

```go
//go:build !windows

package safetree

func invalidPlatformPathComponent(string) bool {
	return false
}

func canonicalPlatformPath(name string) string {
	return name
}
```

`internal/safetree/path_windows.go` (Windows builds — real checks):

```go
//go:build windows

func invalidPlatformPathComponent(component string) bool {
	return InvalidWindowsPathComponent(component)
}

func canonicalPlatformPath(name string) string {
	return windowsCanonicalPath(name)
}
```

`internal/safetree/windows.go` — the pure-rule implementations
(`InvalidWindowsPathComponent`, `windowsCanonicalPath`,
`isWindowsReservedDeviceName`) have NO build tag; they're already compiled
everywhere, only the dispatch is platform-gated.

Usage (`internal/safetree/safetree.go:101-127`): `canonicalPlatformPath`
feeds the collision-detection map keys only (`key := canonicalPlatformPath(name)`);
stored paths and therefore **tree digests are unaffected** — this change is
purely stricter acceptance, no migration.

`InvalidWindowsPathComponent` is exported; check
`rg "InvalidWindowsPathComponent"` — `internal/upload/` references it for
manifest validation (keep it exported with its name and docs).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (package) | `go test -race ./internal/safetree/ -count=1` | all pass |
| Tests (all) | `just test` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/safetree/safetree.go`
- `internal/safetree/path_other.go`, `internal/safetree/path_windows.go`
  (both deleted)
- `internal/safetree/windows.go` (rename/internalize as needed)
- `internal/safetree/safetree_test.go`

**Out of scope**:
- `internal/safetree/path_other.go`-style dispatch for any genuine OS
  syscalls elsewhere (install's `filesystem_*.go` files are a different,
  correct use of build tags — do not touch).
- `internal/upload/` — keep honoring the exported
  `InvalidWindowsPathComponent`; no signature change.
- Digest computation — must remain bit-identical.

## Steps

### Step 1: Remove the platform dispatch

Delete `path_other.go` and `path_windows.go`. In `safetree.go` (or wherever
they're called), call the real implementations directly — rename
`windowsCanonicalPath` to `canonicalPath` (drop the platform prefix now that
it's universal) and use `InvalidWindowsPathComponent` as-is at call sites,
replacing the two shim functions.

**Verify**: `go build ./... && go test -race ./internal/safetree/ -count=1` → its
existing suite passes; nothing else changed behavior yet on this platform
except cases the suite doesn't yet cover.

### Step 2: Tests that prove portability

Add to `internal/safetree/safetree_test.go` (exemplar: the existing
Builder collision/limits tests):

- `TestBuilderRejectsWindowsReservedNamesOnAllPlatforms`: `CON.txt`,
  `con`, `LPT1.md`, trailing-dot and trailing-space components — all
  rejected via `Builder.AddFile` on the test runner's (non-Windows)
  platform.
- `TestBuilderRejectsCaseAliasOnAllPlatforms`: `Readme.md` then
  `README.md` → duplicate-path error on Linux.

**Verify**: `go test -race ./internal/safetree/ -run 'Reserved|CaseAlias' -v -count=1` → pass.

### Step 3: Full gate

**Verify**: `just check` → exit 0.

## Test plan

Covered in Step 2. No fixtures; `fstest`-free direct Builder tests are the
existing pattern — match it.

## Done criteria

- [ ] `ls internal/safetree/path_*.go` → no such files
- [ ] `rg "canonicalPlatformPath|invalidPlatformPathComponent" internal/` → no matches (shims gone)
- [ ] New portability tests pass on the current (Linux) platform
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The key assumption is wrong — i.e. you discover that registries are
  intended to serve platform-specific trees and Windows installability is
  NOT required (check docs/, README, and any deployment notes before
  assuming). This decides the whole plan; hand it back as a memo question
  instead of guessing.
- The "Current state" excerpts don't match.
- Call sites turn out to USE the canonical key as a stored/computed path
  anywhere (that would change digests — the plan's "digests unchanged"
  invariant breaks; hand back).
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Mention in commit message/changelog: running Linux registries will now
  reject uploads they previously accepted (Windows-hostile names and
  case-colliding paths). Operators may see new 400s from web uploads — by
  design.
- If per-platform registries ever become a real product need, the right
  shape is a registry-level policy knob, not build tags — keep `safetree`
  portable-only.
