# Plan 010: Move catalog transition rules from storage into registry

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/registry/ internal/storage/`
> Plans 003, 004, 009, 012, 017, 018 may legitimately change in-scope files
> — reconcile against landed work; on an unexpected mismatch, treat it as a
> STOP condition.

## Status

- **Effort**: L
- **Risk**: MED (moves transactional rule code; must keep atomicity)
- **Depends on**: none hard — MUST be sequenced after 012, 017, 018 per the index (same files)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

AGENTS.md: "`internal/registry/` owns skill identities, candidates,
immutable publications, and catalog rules." In reality `registry.Catalog`
forwards eight of nine operations to a persistence port
(`internal/registry/catalog.go:97-120`), and the catalog rules — publication
idempotence, first-publication-becomes-current, current-selection
replacement — live in SQLite code (`internal/storage/catalog.go:249-369`)
**and** in a ~170-line in-memory reimplementation
(`internal/registry/catalog_integration_test.go:38-197`). Two
implementations of one policy: they can drift, and registry's own tests
currently prove the fake, not production. Role, seam, and tests all point
at the wrong place.

## Current state

The port today is the whole catalog contract —
`internal/registry/catalog.go:27-37`:

```go
type CatalogRecords interface {
	RecordCandidate(context.Context, Candidate, agentskill.Directory) error
	Candidate(context.Context, CandidateID) (Candidate, error)
	OpenCandidateTree(context.Context, CandidateID) (Tree, error)
	PublishCandidate(context.Context, CandidateID, Actor, time.Time) (PublishResult, error)
	SelectCurrent(context.Context, PublicationID, Actor, time.Time) (CurrentPublication, error)
	ListPublishedSkills(context.Context) ([]SkillSummary, error)
	ResolveCurrent(context.Context, SkillID) (Publication, error)
	Publication(context.Context, PublicationID) (Publication, error)
	OpenPublicationTree(context.Context, PublicationID) (Tree, error)
}
```

`Publish` is a one-line forward (`catalog.go:103-104`); the real decision —
conditional insert, `ON CONFLICT DO NOTHING`, first-current selection, and
`NewPublishResult` outcomes — is `internal/storage/catalog.go:249-345`
(excerpt in the design log; verified during review).

The test duplication: `memoryCatalogRecords` at
`internal/registry/catalog_integration_test.go:38-197` reimplements
first-current and idempotent-publish rules; `catalog_integration_test.go:304-417`
then exercises them *through that fake*, not through SQLite. Temporary
SQLite is already the project's test pattern (`internal/storage/catalog_test.go`,
in-process `t.TempDir` per AGENTS.md) and has no import-cycle cost in the
external `registry_test` package.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/registry/ ./internal/storage/ -count=1` | all pass |
| Tests (all) | `just test` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/registry/catalog.go` (+ a new file for transition logic if it
  outgrows it; keep it in-package)
- `internal/storage/catalog.go`, `internal/storage/records.go` (port reshaping)
- `internal/registry/catalog_integration_test.go` (fake removal, real SQLite)
- `internal/storage/catalog_test.go` (case relocation/adjustment)

**Out of scope**:
- Schema/migrations — the tables stay; only *which layer decides* changes.
- `PublishResult`/`CurrentPublication` shape simplification — plan 018 owns
  it; do that first per the index order.
- Curator parameters on these signatures — plan 009 owns it.
- Capture flow / double-staging — plan 012 owns it.

## Steps

### Step 1: Name the persistence facts

Design the narrowed port as a set of atomic persistence operations named
for what SQLite must guarantee, not for catalog rules. Sketch (final
shapes are the executor's jazz, but the *kinds* of operations are the
intent):

```go
type CatalogStore interface {
	// reads (mostly today’s, minus rule-bearing composites)
	Candidate(ctx, id) (Candidate, error)
	Publication(ctx, id) (Publication, error)
	CurrentPublication(ctx, skill) (Publication, error) // ErrNotFound when none current
	...
	// atomic writes — transaction boundaries remain in storage
	RecordCandidate(ctx, Candidate, agentskill.Directory) error
	InsertPublicationTx / withCurrentSelection(...) — one call that persists the
	    registry-computed outcome atomically
}
```

Key constraint: idempotence (`ON CONFLICT`) and the first-current decision
change *how rows are written*, so the port may need a
"apply-this-transition atomically" shape where the port accepts a
registry-computed closure or value — e.g. `RunPublicationTx(ctx, func(tx PublicationTx) error)`
is wrong-grained (leaks tx); prefer `PersistPublication(ctx, publication, setCurrent bool) (inserted bool, err error)`
where storage owns ON CONFLICT + the conditional insert, and registry owns
*deciding* what to ask for. Keep tree-lifecycle methods as-is.

**Verify**: `go build ./internal/registry/ ./internal/storage/` → exit 0
(compile the new port with adapters stubbed, before moving logic).

### Step 2: Move the rules into registry

`registry.Catalog.Publish` and `SetCurrent` now contain the decision logic
(whether a publication is new, whether it becomes current, whether an
existing one is returned unchanged), expressed against the narrowed port.
All result construction (`NewPublishResult` etc.) happens in registry; SQL
only persists what is asked, atomically.

**Verify**: `go test -race ./internal/storage/ -count=1` → storage tests for
persistence mechanics still pass; rule assertions move with the code.

### Step 3: Delete the memory fake; test against real SQLite

Replace `memoryCatalogRecords` in `catalog_integration_test.go` with a real
`storage.OpenCatalog` over `t.TempDir` (exemplar fixture:
`internal/storage/catalog_test.go`'s open helper). The external
`registry_test` package can import storage without a cycle (daemon's test
already composes both; `internal/web/web_test.go:44-63` shows the pattern).
Rule coverage that lived only in the fake adapter moves into
`registry`-side tests against the real SQLite adapter; trivial forwarding
tests can be deleted rather than translated.

**Verify**: `go test -race ./internal/registry/ -count=1` → all pass, no
`memoryCatalogRecords` remains.

### Step 4: Full gate and audit

**Verify**: `rg "memoryCatalogRecords"` → no matches;
`rg "ON CONFLICT" internal/registry/` → no matches (SQL stays in storage);
`just check` → exit 0.

## Test plan

- Registry publish tests: first publication → created+current; repeated
  publication of the same tree → idempotent (existing publication, not
  created); different tree → new publication, current unchanged. Now
  running against real SQLite.
- Set-current: selection of a published version persists; selecting a
  nonexistent publication → `registry.ErrNotFound`.
- Storage keeps transaction/atomicity tests (partial-failure rollback) —
  those belong to the adapter.
- Exemplar structure: `internal/registry/catalog_integration_test.go:304-417`
  (the scenarios are correct; only the adapter changes).

**Verify**: `just test` → all pass.

## Done criteria

- [ ] `rg "memoryCatalogRecords|CatalogRecords" internal/registry/` → no rule-fake and no full-catalog port remain
- [ ] Publish/SetCurrent decision logic reads from `internal/registry/`
- [ ] SQL (`ON CONFLICT`, transactions) exists only in `internal/storage/`
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The drift check shows un-landed shapes at the seam (009/012/018 not yet
  landed but half-applied signatures).
- You can't keep publication+first-current persistence atomic without
  exposing `*sql.Tx` in the port — the port shape is the design fork;
  describe candidate shapes in a handback instead of picking.
- The catalog integration fake turns out to serve scenarios SQLite can't
  reproduce (e.g. deterministic injected failures at transition points) —
  keep minimal fault-injection seams in storage, and describe what you
  kept in the handback/commit message.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions — descriptive, not prescriptive.

## Maintenance notes

- After this, `rg "INSERT INTO publications" internal/` hits only storage —
  keep it that way; a reviewer rejects policy-shaped SQL growing policy
  back.
- The registry↔storage port is now the place where atomicity guarantees
  are named. Document each write op's guarantee in its doc comment
  (this is where the deferred doc-comment guidance from the index applies).
- If a second non-web caller of catalog transitions appears, it gets the
  registry package — never storage directly.
