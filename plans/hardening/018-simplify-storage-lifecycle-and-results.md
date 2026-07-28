# Plan 018: Replace storage close-state booleans with a phase; shrink publish result shapes to what callers use

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/storage/ internal/registry/ internal/web/web.go`
> Plans 003, 004, 009, 010 legitimately change in-scope files — reconcile
> against landed work; on an unexpected mismatch, treat it as a STOP
> condition.

## Status

- **Effort**: M
- **Risk**: LOW
- **Depends on**: none hard — MUST be sequenced before 010 (registry shape changes; 010 builds on the simplified result types)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Two "model the real thing" cleanups in the catalog stack:

1. `storage.Catalog` tracks shutdown as four coupled booleans
   (`closing`, `closed`, `dbClosed`, `lockClosed` —
   `internal/storage/catalog.go:24-40, 117-156`) whose legal joint states were never
   stated; tests assert exact flag combinations
   (`catalog_test.go:228-349`) — choreography, not behavior.
2. `PublishResult{Created, BecameCurrent}` exposes transition receipts no
   production caller consumes (web discards both results —
   `web.go:386, 426`; only tests read the flags), and `CurrentPublication`'s
   selector/timestamp are persisted but surface in no read model
   (`ListPublishedSkills` reads identity only). Types exist to tell truths
   callers use; these tell none.

## Current state

```go
// internal/registry/entities.go:30-34
type PublishResult struct {
	publication   Publication
	created       bool
	becameCurrent bool
}
```

(constructor at 76-88 enforces `becameCurrent ⇒ created`). Close path and
flags verified at `internal/storage/catalog.go:24-40, 117-168`, with
`withOpenState` consulting only `closing`.

`internal/storage/sqlite.go` persists `selected_actor_id`, `selected_at_ns`
in `current_publications` — check the schema excerpt there. Persisted data
is user data: this plan does NOT remove columns; see Step 2's constraint.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/storage/ ./internal/registry/ ./internal/web/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/storage/catalog.go`, `internal/storage/catalog_test.go`
- `internal/registry/entities.go`, `internal/registry/catalog.go` (+ integration test)
- `internal/web/web.go` (call-site sweep only)

**Out of scope**:
- Schema migrations. The migration ladder is append-only (AGENTS.md) and
  existing databases keep `selected_*` columns. Dropping them needs a
  retention decision — see STOP conditions.
- Moving transition rules — plan 010 (lands after this).
- Curator parameters on Publish/SetCurrent — plan 009.

## Steps

### Step 1: One close phase

Replace the four booleans with an explicit phase value
(`open → closing → closingDatabase → closingLock → closed`, or however the
retry-resumable progression actually works — derive it from the current
flag combinations, it's a refactor not a redesign). Update
`withOpenState`. Rewrite the close tests to assert observable behavior:
rejects operations after close, retry after mid-close failure resumes,
reopen disallowed — not flag values.

**Verify**: `go test -race ./internal/storage/ -count=1` → all pass.

### Step 2: Publish/Select return what callers use

`registry.Catalog.Publish` (and the `CatalogRecords.PublishCandidate` port)
returns `(Publication, error)`; `SetCurrent` returns `error`. Remove
`PublishResult` from the public surface unless plan 010's landing created a
real consumer — grep first (`rg "PublishResult|Created\(\)|BecameCurrent\("`).
Idempotence *behavior* stays (re-publish returns the existing publication).

`CurrentPublication` stays as a type if reads exist (check
`rg "CurrentPublication" internal/`); if its only producers are the
persist path and its only consumers are tests, reduce the transition to
return `error` and keep the row as stored audit data. Deleting the
persisted columns or the type entirely requires the retention decision —
if the simpler path tempts you there, STOP (data shape is user data).

**Verify**: `go build ./... && go test -race ./internal/registry/ ./internal/web/ ./internal/storage/ -count=1` → all pass.

### Step 3: Update behavior-level tests

The integration scenarios previously asserting `Created()`/`BecameCurrent()`
assert resulting state instead: after re-publish, `ResolveCurrent`/
`Publication` reads prove the same publication/current
(`catalog_integration_test.go:326-366` is the exemplar to translate, not extend).

**Verify**: `go test -race ./internal/registry/ ./internal/storage/ -count=1` → all pass.

### Step 4: Full gate

**Verify**: `just check` → exit 0.

## Test plan

Covered by Steps 1 & 3. Explicit adds: close-retry resumption proven by
behavior (second `Close()` after injected first-close failure succeeds;
injection seams at catalog.go:108-113 already exist — reuse).

**Verify**: `go test -race ./internal/storage/ -run 'Close' -count=1` → all pass.

## Done criteria

- [ ] `rg "closing|dbClosed|lockClosed" internal/storage/catalog.go` → replaced by a phase (one field)
- [ ] No test asserts internal close flags
- [ ] `PublishResult` is gone or has a documented production consumer
- [ ] Web handlers unchanged in behavior (they discarded results already)
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- Plan 010 already landed and registry now owns the transition logic —
  reconcile: apply the shape simplification there instead, without
  regressing its port design.
- *Deleting the select-metadata columns* becomes clearly right (no reader
  will ever exist) — that's an append-only-ladder migration decision with
  user data; handback, don't migrate here.
- A real consumer of `Created`/`BecameCurrent` turns up in the web UI's
  template data path — then keep the flags and only do Step 1; say so in
  the handback.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Transition receipts vs resulting state: tests should read the catalog and
  prove the outcome; flags that narrate the transition invite
  choreography-pinning tests. Keep that rule in future catalog review.
- The selector/timestamp columns remain as stored audit data; if a read
  model ever surfaces "who selected this", expose it through a deliberate
  registry read — don't resurrect PublishResult.
