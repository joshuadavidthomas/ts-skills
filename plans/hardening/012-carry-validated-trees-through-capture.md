# Plan 012: Carry the validated upload snapshot into capture; bind candidate to content at the storage seam

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/upload/ internal/registry/catalog.go internal/storage/ internal/web/web.go internal/safetree/`
> Plans 001, 003, 004, 010 legitimately change in-scope files — reconcile
> against landed work; on an unexpected mismatch, treat it as a STOP
> condition.

## Status

- **Effort**: M
- **Risk**: MED
- **Depends on**: 001-thread-context-through-tree-walks.md (Capture's hashes gain ctx there; build on it)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

A browser upload is validated and staged by `upload`, then demoted to a
generic `fs.FS` + raw root string, staged a second time by
`registry.Capture` (`safetree.StageFS`), and copied a third time into
durable storage. Three copies of one tree because the proof of validation
can't cross seams. Relatedly, the storage seam accepts a `Candidate` plus
an `agentskill.Directory` as an un-bound pair: `RecordCandidate` verifies
the digest but nothing checks the staged `SKILL.md` name against the
candidate's skill name — an illegal pair is persistable. Two facets of one
problem: validated-tree facts should flow as typed values, and persistence
must refuse combinations that violate the aggregate invariant.

## Current state

Upload holds the proof, then erases it — `internal/upload/upload.go:20-60`:
`Submission` wraps a `*safetree.Snapshot` but exposes only `FS() fs.FS`,
`Root() string`, `Label() string`.

```go
// internal/registry/catalog.go:19-25
type CaptureRequest struct {
	Namespace  Namespace
	Source     fs.FS
	Root       string
	Provenance Provenance
}
```

`Capture` (registry/catalog.go:62-94) runs `safetree.StageFS(...)` into the
catalog's own staging parent, loads + hashes the tree, then calls
`records.RecordCandidate(ctx, candidate, directory)`, which materializes to
durable storage (`internal/storage/catalog.go:193-223`, copying again in
`materializeTree`) — and `internal/storage/trees.go:87-112`
(`materializeTree`) hashes the copied bytes but never compares the staged
`SKILL.md` name with `candidate.Skill().Name()`.

Multipart choreography also leaks: web pre-consumes the manifest part and
passes a positioned reader (`internal/web/web.go:256-281`,
`upload.StageBrowserDirectory(ctx, parent, manifest, body, limits)`) whose
hidden cursor state the upload module then assumes
(`internal/upload/upload.go:69-78`).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/upload/ ./internal/registry/ ./internal/storage/ ./internal/web/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/upload/upload.go` (+ test) — Submission carries the snapshot as such
- `internal/registry/catalog.go` (+ integration test) — Capture accepts the refined input
- `internal/storage/catalog.go`, `internal/storage/trees.go` (+ tests) — content/identity agreement check
- `internal/web/web.go` (+ tests) — pass the snapshot through; hand the raw reader to upload

**Out of scope**:
- Moving catalog *transition policy* — plan 010. Keep `Capture`'s rule
  logic in registry where it already lives.
- `safetree.StageFS` existence — a public CLI/registry import path may
  still want fs→staging; only the *web upload* flow must stop
  double-staging. If no other caller remains after this, that's a finding
  for the commit message, not deletion in this plan.
- Digest format, protocol, wire shapes — unchanged.

## Steps

### Step 1: upload owns its own multipart cursor

Change `StageBrowserDirectory` to take the untouched `*multipart.Reader`
(after web consumes only its `namespace` field): upload fetches and
validates the manifest part itself instead of receiving it pre-consumed
(web.go:256-281 shrinks to: resolve namespace part, hand reader over).
`upload_test.go:25-32` loses its fake pre-consumption dance.

**Verify**: `go test -race ./internal/upload/ ./internal/web/ -count=1` → all pass.

### Step 2: Submission hands capture the real snapshot

Give `Submission` a way to *lend* its snapshot — e.g. `Snapshot() *safetree.Snapshot`
(documented: caller must not Close; Submission still owns it) or a direct
`Capture`-oriented method. `CaptureRequest` changes shape:

```go
type CaptureRequest struct {
	Namespace  Namespace
	Staged     *safetree.Snapshot // already validated+staged tree
	Root       string
	Provenance Provenance
}
```

`Capture` skips `StageFS` when given a staged snapshot: load + hash from
`snapshot.FS()` directly (post-001: `agentskill.SumTree(ctx, ...)`). Web
keeps the `Submission` alive until `Capture` returns, then closes (defer
ordering — submission close after capture completes; plan 004's joined
cleanup applies).

**Verify**: `go test -race ./internal/registry/ ./internal/web/ -count=1` → all pass;
one fewer temp tree per upload (add an instrumented assertion if the
existing fixtures allow, otherwise a window assertion in a capture test
that `StageFS` was not on the path).

### Step 3: Storage refuses a mismatched candidate pair

In `internal/storage` (`materializeTree` after the staged-copy + hash, or
`RecordCandidate` around catalog.go:193-223): load the staged `SKILL.md`
and require its parsed name to equal `candidate.Skill().Name()` before any
tree rename or metadata insert. New sentinel or reuse `registry.ErrConflict`?
Neither fits — use a plain wrapped error with clear text
(`fmt.Errorf("candidate names %s but SKILL.md names %s", ...)`) since no
caller maps it; if a caller later needs it, promote then.

**Verify**: `go test -race ./internal/storage/ -count=1` → all pass.

### Step 4: Full gate

**Verify**: `rg "StageFS" internal/` → remaining callers are deliberate;
`just check` → exit 0.

## Test plan

- Storage seam: matching digest + mismatched SKILL.md name → explicit
  error, no tree visible afterwards, no candidate row (assert via the
  package's existing catalog reads, exemplar `catalog_test.go` failure
  restart tests at 607-654).
- Capture-through-web: existing upload integration tests pass unchanged at
  their assertions; add one test asserting digest equality through the
  no-restage path (the candidate's tree digest equals the upload's staged
  digest byte-for-byte — proves no recopy aliasing).
- upload: manifest position validation belongs solely to upload now (move
  the web-level assertion into upload's table).

**Verify**: `go test -race ./internal/upload/ ./internal/registry/ ./internal/storage/ ./internal/web/ -count=1` → all pass.

## Done criteria

- [ ] Browser upload staged once by upload, zero times by capture
- [ ] Storage rejects candidate/SKILL.md name mismatch even with a matching digest
- [ ] `StageBrowserDirectory` no longer takes a pre-consumed manifest part
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The drift check shows plan 010 already reshaped `Capture`'s port —
  reconcile signatures; if `Capture` moved wholesale, re-anchor this plan's
  excerpts and proceed only if the seam is still registry's.
- A non-web production caller of `CaptureRequest.Source fs.FS` exists that
  genuinely needs fs-input capture (the design assumes only the upload
  flow uses it; a second caller shape is a memo question).
- Keeping the submission's snapshot alive across `Capture` conflicts with
  plan 004's cleanup joining in a way that forces a signature choice —
  describe the tension, handback.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Ownership rule after this: `Submission` owns its snapshot; `Capture`
  borrows; web sequences the close. Put that sentence in `Submission`'s doc
  comment.
- The name-agreement check at the storage seam is defense-in-depth —
  capture derives the candidate FROM the directory, so agreement is
  normally tautological. It exists because the storage port is separately
  callable; keep it cheap.
