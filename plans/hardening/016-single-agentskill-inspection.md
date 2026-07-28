# Plan 016: One agentskill inspection operation returning document, tree, and digest

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/agentskill/ internal/registry/ internal/client/ internal/install/`
> Plan 001 legitimately changes in-scope files (it adds ctx to SumTree) —
> this plan is sequenced after it and builds on it.

## Status

- **Effort**: M
- **Risk**: LOW
- **Depends on**: 001-thread-context-through-tree-walks.md
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

`agentskill.Load` proves only "SKILL.md parses"; the tree walk and digest
live in a separate call, and binding the parsed name to the digest is left
to every caller. That exact three-step choreography — Load, SumTree, name
check — is currently repeated at three trust boundaries
(`internal/registry/catalog.go:71-87`,
`internal/client/client.go:206-218`,
`internal/install/installer.go:173-185`), and a fourth
(registry/storage `materializeTree`) hashes without identity binding
(plan 012 patches that one in place). `Load`-success meaning
"partially validated" is exactly the caller burden the module should
absorb: inspect an Agent Skill tree, hand back a refined value.

## Current state

`internal/agentskill/directory.go:16-59` (`Load`): reads and parses
`SKILL.md`, checks its basename, returns `Directory{document, files}`
without walking the tree.

`internal/agentskill/digest.go:41` (`SumTree`): walks + hashes, rejects
unsafe entries — no ctx today; plan 001 gives it
`SumTree(ctx, fsys, dir)`. Build this operation on the post-001 signature.

Repeated choreography (all verified during review):
- registry capture: `agentskill.Load` → `SumTree(snapshot.FS(), root)` → `NewSkillID(ns, directory.Document().Name)`
- client download-accept: `agentskill.Load(snapshot.FS(), ".")` + name equality vs publication + `SumTree` digest equality
- install verify: `stageAndVerify` (installer.go:164-190) same shape

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/agentskill/ ./internal/registry/ ./internal/client/ ./internal/install/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/agentskill/` (new refined type + operation; tests)
- `internal/registry/catalog.go` — capture call site
- `internal/client/client.go` — fetch verification call site
- `internal/install/installer.go` — stageAndVerify call site

**Out of scope**:
- `internal/storage/trees.go` materialize/re-verification — plan 012 puts a
  name-agreement check at the storage seam; keep it independent (storage
  re-validates persisted input on a separate trust boundary).
- `Directory`, `SumTree`, `Load` public APIs — they stay; the new op
  composes them. Removing any is a bigger-facing decision, not this plan.
- Wire/protocol shapes.

## Steps

### Step 1: Define the refined value and operation

In agentskill, sketch:

```go
// Inspection is a loaded Agent Skill tree with its identity facts bound.
type Inspection struct {
	directory Directory
	digest    TreeDigest
}

// Load -> Directory, Digest() -> TreeDigest, Document()/FS() passthroughs.
func Inspect(ctx context.Context, fsys fs.FS, dir string) (Inspection, error)
```

`Inspect` = `Load` + `SumTree(ctx, ...)` composed, one entry. Add a name
binding helper or make callers keep their one-line comparison (the
comparisons differ per caller: capture *derives* the identity; client and
install *require* equality with an expected name — provide
`Inspection.RequireName(name agentskill.Name) error` returning a wrapped
`ErrInvalidTree`, so all three call sites shrink honestly rather than
uniformly).

**Verify**: `go test -race ./internal/agentskill/ -count=1` → all pass.

### Step 2: Swap the three call sites

registry `Capture`, client `fetchTree` verification, install
`stageAndVerify` each replace their Load/SumTree/name-check sequences with
`Inspect` + (for the latter two) `RequireName`. Error wrapping style at
each seam unchanged (`ErrProtocol`/`ErrIdentityMismatch` wraps stay the
caller's mapping of the new error).

**Verify**: `go test -race ./internal/registry/ ./internal/client/ ./internal/install/ -count=1` → all pass.

### Step 3: Full gate

**Verify**: `rg "agentskill.Load\(" internal/registry/ internal/client/ internal/install/` → no matches outside tests composing deliberately; `just check` → exit 0.

## Test plan

- agentskill: `Inspect` on valid tree → document+digest bound; unsafe tree
  entry → error; cancelled ctx (post-001) → `context.Canceled`. Exemplar:
  existing Load/SumTree tests in agentskill_test.go.
- Call-site behavior unchanged — existing registry/client/install tests are
  the gate; no new assertions expected.

**Verify**: `go test -race ./internal/agentskill/ -count=1` → all pass incl. new tests.

## Done criteria

- [ ] `Inspect` exists and composes Load + ctx-aware SumTree
- [ ] The three listed call sites use it; the choreography appears once per package at most
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- Plan 001 hasn't landed (no ctx-aware `SumTree`). Hand back; don't
  duplicate the threading.
- A fourth caller needs partial validation (Load without hashing) for
  performance reasons — the refined op keeps both stages mandatory by
  design; a measured need for the half-shape is a memo.
- `RequireName` semantics diverge between client and install beyond naming
  (e.g. install needs digest-exact matching instead) — don't generalized
  the wrong helper; handback.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- `Load`+`SumTree` stay public for genuinely separate uses (e.g. web tree
  browsing reads without hashing); new trust-boundary consumers should use
  `Inspect`.
- Keep `Inspection` read-only like `Directory` (`Document()` clone behavior
  — see directory.go's alias-safety) — it's a proof value, mutability
  would undermine it.
