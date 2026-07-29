# Plan 003: Bound what a read request can cost — verify trees once, cap concurrent tree work, cap preview size

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> `plans/settling/README.md`.
>
> **Drift check (run first)**:
> `jj diff --from 7b926628 -- internal/server/trees.go internal/server/handlers.go internal/server/catalog.go internal/server/templates internal/server/handlers_test.go internal/server/catalog_test.go`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition. **Plan 001 must have landed
> first** (it restructures `publicationTree`, which this plan also edits).

## Status

- **Effort**: M
- **Risk**: MED
- **Depends on**: 001-sweep-server-tmp-at-startup.md
- **Planned at**: working copy `7b926628` (parent `f33fe93e`), 2026-07-29

## Why this matters

The read routes — skill page, candidate review, ZIP download — are open to
every tailnet peer with no capability required, and each request currently
pays unbounded cost. Every `openTree` re-hashes the entire tree
(SHA-256 over every byte) even though trees are immutable, content-
addressed, and were verified when written; downloads then walk the tree a
second time to spool a complete ZIP copy (~132 MiB ceiling) into
`<state>/tmp`; file previews buffer whole files (16 MiB ceiling) into
memory three times over. Nothing caps concurrency, so N parallel requests
cost N × all of that — enough to fill the state volume or OOM the daemon
from any peer, no malice required beyond a retry loop. This plan makes
per-request cost proportional to what the response needs and puts a
ceiling on the total.

## Current state

- `internal/server/trees.go:346-360` — `openTree`:

  ```go
  func (c *catalog) openTree(ctx context.Context, digest agentskill.TreeDigest) (*treeView, error) {
      done, err := c.withOpenState()
      ...
      _, final := c.treePaths(digest)
      if err := verifyTree(ctx, final, digest); err != nil {   // full SumTree, every call
          return nil, err
      }
      c.refsMu.Lock()
      c.openTrees++
      ...
  ```

- `internal/server/trees.go:250-258` — `verifyTree` = full
  `agentskill.SumTree` over the directory.
- `internal/server/trees.go:153` — `materializeTree` already runs
  `verifyTree` when a tree is first written, before its atomic rename into
  `trees/sha256/...`. Written trees are immutable thereafter.
- `openTree` call sites: `internal/server/handlers.go:203` (`skillPage`),
  `:326` (`reviewCandidate`), `:475` (`publicationTree`).
- `internal/server/handlers.go:519-598` — `rootlessZIP` walks the tree and
  copies every file into a temp ZIP per download; size-checked only after
  fully written (`:587`).
- `internal/server/handlers.go:726-750` — `resolveTreeFile` copies the
  selected file into a `bytes.Buffer`, then `contents.String()` (second
  copy); `render` (`:807-816`) buffers the whole page (third copy).
  `safetree.PrototypeLimits().MaxFileBytes` is 16 MiB
  (`internal/safetree/safetree.go:35`).
- `internal/server/daemon.go:411-424` — `handlerGate.admit()` counts
  in-flight handlers for the shutdown drain but never limits them; the
  only per-request bound is the body cap.
- `internal/server/handlers.go:68-100` — `newHandler(catalog,
  resolveCurator, options handlerOptions)`; `handlerOptions` already
  carries `StagingParent`, `Limits`, `Logger` — the natural home for a
  concurrency bound so tests can set it low.
- Prior-art note: the hardening effort deferred "resolveTreeFile unbounded
  reads" as "trusted digest-pinned content"
  (`plans/hardening/README.md`, Deferred). That reasoning covered content
  *trust*, not memory amplification under concurrency; this plan
  supersedes the deferral.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Focused tests | `go test ./internal/server/ -race -count=1` | `ok`, exit 0 |
| Full suite | `just test` | all packages `ok` |
| Static analysis | `just vet` | exit 0 |
| Rebuild committed CSS (only if template class lists change) | `just css` | `internal/server/static/style.css` regenerated |

## Scope

**In scope**:
- `internal/server/trees.go`
- `internal/server/catalog.go` (cache state lives on `catalog`)
- `internal/server/handlers.go`
- `internal/server/templates/skill.html` and/or `review.html` (truncation notice)
- `internal/server/static/style.css` (only via `just css`)
- `internal/server/handlers_test.go`, `internal/server/catalog_test.go`
- `internal/server/daemon.go` (only if the bound must thread through construction)

**Out of scope**:
- `internal/agentskill/` — `SumTree` itself is fine; the problem is calling it per request.
- `cmd/ts-skills/` — the client's own verification of downloaded trees is its defense; do not touch.
- `internal/server/daemon.go` drain/gate semantics — the shutdown path stays exactly as is.

## Steps

### Step 1: Verify each digest tree once per process

What must be true: the first `openTree` for a digest verifies as today;
subsequent opens of the same digest in the same process skip
re-hashing. The cache is per-`catalog` (process-lifetime), keyed by
`agentskill.TreeDigest`, guarded for concurrent access, and seeded by
`materializeTree`'s existing post-write verification so a freshly
published tree's first download does not re-hash either. Concurrent first
opens of the same digest may both verify (harmless) or coordinate — either
is acceptable; state which you chose in the plan's status row when done.
A verification *failure* must not poison the cache (retry re-verifies).

The semantic change to state plainly in the commit message: on-disk
corruption of a tree is now detected at first open per process (and at
publish), not on every request. A daemon restart re-arms detection.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

### Step 2: Cap concurrent expensive tree work with a semaphore

What must be true: the number of simultaneously running
tree-spool/tree-read operations (`rootlessZIP` and `resolveTreeFile`, at
minimum) has a fixed ceiling; a request arriving when the ceiling is
reached receives 503 with a `Retry-After` header rather than queueing
unboundedly or running. Implement as a bounded token channel (or
`semaphore.Weighted`) owned by the handler, with the bound in
`handlerOptions` (default: a small constant, e.g. 4; tests set 1). Do not
entangle it with `handlerGate` — the gate is shutdown bookkeeping and its
semantics must not change.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

### Step 3: Cap the preview read in `resolveTreeFile`

What must be true: the preview pane reads at most a display budget
(256 KiB) of the selected file via `io.LimitReader`-style bounding, marks
the result truncated when the file is larger, and the skill/review
templates render a visible "truncated — download the ZIP for the full
file" notice in that case. Binary detection (`utf8.Valid`) operates on
the bounded bytes. Keep the existing `requestContextReader` cancellation
wrapping (`handlers.go:752-762`). If template class lists change, run
`just css` and commit the regenerated `style.css` (repo rule,
`AGENTS.md`).

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

## Test plan

Patterns: `newWebFixture` (`internal/server/handlers_test.go:52`) for
handler-level tests; existing `openTree`/materialization tests in
`internal/server/catalog_test.go`.

1. **Verify-once**: open a tree, close it, corrupt one file on disk under
   `trees/sha256/...`, open the same digest again — succeeds (cache hit
   proves no re-hash). Then build a *fresh* catalog on the same state dir
   and open — fails with the tree-mismatch error (restart re-arms).
2. **Cache not poisoned**: corrupt a tree before its first open — open
   fails; repair the file — open succeeds.
3. **Semaphore**: with bound 1, hold the token via a slow in-flight
   request (a download against a fixture tree gated by a blocking reader,
   or simply acquire the token directly through a test seam), and assert
   a concurrent preview/download request gets 503 + `Retry-After`.
4. **Preview cap**: publish a fixture tree containing a file larger than
   the display budget; `GET /skills/{ns}/{name}?file=...` returns 200,
   body contains the truncation notice, and the handler's peak behavior
   no longer depends on file size (assert the rendered content length is
   bounded).

- **Verify**: `just test` → all pass, including the new tests.

## Done criteria

- [ ] `just test` → all packages `ok`
- [ ] `just vet` → exit 0
- [ ] `rg -n "verifyTree" internal/server/trees.go` shows it called from materialization and the cache-miss path only
- [ ] No files outside the in-scope list are modified (`jj st`)

## STOP conditions

Stop if:

- The code at the "Current state" locations doesn't match the excerpts
  (plan 001 changes `publicationTree` — re-read it before editing).
- Capping concurrency interacts with the shutdown drain in a way that
  makes `TestRun...` daemon tests hang or flake — do not "fix" the gate;
  hand back.
- The truncation UX needs a product decision beyond a notice line (e.g.
  paging, byte-range previews).
- You are tempted to add an LRU or eviction to the digest cache — the
  set of digests is small and append-only per process; if memory of the
  cache itself is somehow a concern, that is a design fork to hand back,
  not a thing to improvise.

On stopping, write a **handback**: current state, desired outcome,
questions. Descriptive, not prescriptive.

## Maintenance notes

The verified-digest cache changes the corruption-detection story: a
scrub-on-restart (or periodic re-verify) is the deliberate future hook if
operators ever want stronger guarantees; note it in the commit message
but do not build it. The semaphore bound is a fixed default on purpose —
making it operator-configurable is deferred until someone actually needs
it. Reviewers should scrutinize Step 1's concurrency (two goroutines
first-opening the same digest) under `-race`.
