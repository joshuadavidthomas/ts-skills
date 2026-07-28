# Plan 017: Close small illegal-state holes in safetree Snapshot and registry CandidateID

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/safetree/ internal/registry/identity.go internal/storage/records.go internal/storage/catalog.go`
> Plans 004, 010 legitimately change in-scope files — reconcile against
> landed work; on an unexpected mismatch, treat it as a STOP condition.

## Status

- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (sequence after 006 per the index order; before 010 which touches the same registry files)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Two small representable-illegal-state holes:

1. `(*safetree.Snapshot)(nil).FS()` returns `os.DirFS("")` — relative
   paths resolving from the **daemon's working directory**. Combined with
   `Close` being carefully nil-safe, code holding an absent snapshot reads
   unrelated process files instead of failing. Verified at
   `internal/safetree/safetree.go:249-255`.
2. `registry.CandidateID` is an exported `[16]byte` — any package can
   construct zero/arbitrary values directly, while its own `ParseCandidateID`
   declares zero invalid and `NewCandidate` re-checks it
   (`internal/registry/identity.go:23-24, 125-141`;
   `internal/registry/entities.go:41-46`). The type looks refined but its
   representation permits bypass; storage currently round-trips
   BLOB→hex-string→text parser to reach the value
   (`internal/storage/records.go:234-249`).

Both are cheap honesty fixes with compiler-enforced payoffs.

## Current state

```go
// internal/safetree/safetree.go:249-255
func (s *Snapshot) FS() fs.FS {
	if s == nil {
		return os.DirFS("")
	}
	return os.DirFS(s.path)
}
```

```go
// internal/registry/identity.go:24
type CandidateID [16]byte
```

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/safetree/ ./internal/registry/ ./internal/storage/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/safetree/safetree.go` + tests
- `internal/registry/identity.go` + tests
- `internal/storage/records.go` (BLOB mapping only)
- any compile-forced touch-ups elsewhere in `internal/` (e.g. agentskill
  tests constructing CandidateIDs — grep first: `rg "CandidateID{"`)

**Out of scope**:
- Snapshot close/ownership semantics — only the nil/zero FS behavior
  changes; Close stays nil-safe.
- `PublicationID`, `SkillID` representations (already private-fielded).
- Storage BLOB layout on disk — bytes identical, mapping code only.

## Steps

### Step 1: Snapshot.FS never yields ambient filesystem

Nil, zero-value, and (defensibly) post-`Close` snapshots' `FS()` returns an
`fs.FS` whose `Open` always fails with `fs.ErrClosed` (small unexported
type; `fs.ErrClosed` gives callers a matchable class). Keep current
behavior for live snapshots. Add nil/zero/closed tests.

**Verify**: `go test -race ./internal/safetree/ -count=1` → all pass incl. new.

### Step 2: CandidateID becomes opaque

`type CandidateID struct{ id [16]byte }`. Keep `String()`,
`ParseCandidateID`, `NewCandidateID` signatures. Add explicit
boundary-construction for persistence: `CandidateIDFromBytes([16]byte) (CandidateID, error)`
rejecting zero, plus a `(id CandidateID) Bytes() [16]byte` (or expose the
array via method) for storage's BLOB mapping — storage replaces its
hex-round-trip with a direct bytes↔value mapping through those methods.
Fix all compile-forced comparisons (`id == (CandidateID{})` sites must use
a `IsZero()`-style method or compare `.id`). Audit `NewCandidate`'s zero
check still lands.

**Verify**: `go build ./... && go test -race ./internal/registry/ ./internal/storage/ -count=1` → all pass.

### Step 3: Full gate

**Verify**: `just check` → exit 0; `rg "CandidateID{" internal/` → only
constructor internals.

## Test plan

- safetree: nil, zero-value, and post-Close `FS().Open(...)` all return
  `fs.ErrClosed`; live snapshot unaffected.
- registry: `ParseCandidateID` zero rejection unchanged; direct
  `CandidateIDFromBytes` zero rejection; storage round-trip preserves id
  through BLOB persistence (existing catalog tests cover once the mapping
  change compiles; add one targeted records test if coverage is thin).

**Verify**: `go test -race ./internal/safetree/ ./internal/registry/ ./internal/storage/ -count=1` → all pass.

## Done criteria

- [ ] No path from a nil/zero Snapshot to `os.DirFS`
- [ ] `CandidateID` can't be constructed zero-valued from outside registry
- [ ] BLOB round-trip no longer passes through hex text
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- Some code outside registry *depends* on CandidateID comparability or
  array-ness in a way an opaque struct breaks beyond mechanical fixes
  (describe the dependency; the fix might be different — e.g. the field
  belongs on a wider type).
- Post-Close `FS()` failing turns out to break a real caller that
  intentionally re-opens closed snapshots — that's a contract question;
  keep only the nil/zero parts and hand the rest back.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- `CandidateIDFromBytes` is the *persistence seam*; new uses outside
  storage deserve review pushback.
- The snapshot change makes zero-value safety the convention for owned
  resources in this codebase — mirror it in future closable types and note
  it in their doc comments.
