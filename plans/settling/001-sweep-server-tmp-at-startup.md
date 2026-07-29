# Plan 001: Sweep the daemon's tmp directory at startup and close the orphaned-archive window

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> `plans/settling/README.md`.
>
> **Drift check (run first)**:
> `jj diff --from 7b926628 -- internal/server/catalog.go internal/server/trees.go internal/server/handlers.go internal/server/catalog_test.go internal/server/handlers_test.go`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Planned at**: working copy `7b926628` (parent `f33fe93e`), 2026-07-29

## Why this matters

Three producers write temporaries into the registry's `<state>/tmp`:
browser-upload staging, tree materialization, and download archives. All
cleanup is deferred inside request handlers, and the shutdown drain
explicitly abandons stuck handlers, so a SIGKILL, OOM, panic, or drain
timeout strands up to ~132 MiB per in-flight request. Nothing ever
enumerates `tmp/` again — disk usage grows monotonically with crash count,
on the same filesystem that holds `registry.sqlite`. The client already
solves this exact problem for its own litter (`sweepLitter` on writer
acquisition); the server has no equivalent. There is also one deterministic
orphan path in the download handler: if `tree.Close()` fails after the
archive was successfully built, the handler returns before the archive
cleanup defer is registered.

## Current state

- `internal/server/catalog.go:81-105` — `openCatalog` takes an exclusive
  flock on `<state>/registry.lock` (`stateLock.TryLock()`), then calls
  `ensureStorageDirectories(absolute, treesDir, tmpDir)` where
  `tmpDir := filepath.Join(absolute, "tmp")`. The flock guarantees
  single-process ownership of the state directory for the daemon's
  lifetime.
- `internal/server/trees.go:45-66` — `ensureStorageDirectories` only
  *creates* directories. Nothing in `internal/server` ever calls
  `os.ReadDir` on `tmpDir` (confirmed by grep at planning time).
- The three temp-name shapes that land in `tmpDir`, all process-owned by
  construction:
  - `internal/safetree/safetree.go:84` — `os.MkdirTemp(parent, ".ts-skills-tree-")` (upload staging directories)
  - `internal/server/trees.go:87` — `os.MkdirTemp(c.tmpDir, ".tree-")` (materialization staging directories)
  - `internal/server/handlers.go:546` — `os.CreateTemp(h.options.StagingParent, ".ts-skills-download-*.zip")` (download archive files)
- `internal/server/daemon.go:397,404` — the drain reports
  `handler drain exceeded %s: abandoning stuck handlers`; abandoned
  handlers' deferred cleanups never run.
- `internal/server/handlers.go:480-497` — `publicationTree`:

  ```go
  archive, err := h.rootlessZIP(r.Context(), tree)
  // The archive holds everything the response needs, so the tree closes
  // before any bytes are written and its close failure is still reportable.
  if closeErr := tree.Close(); closeErr != nil {
      err = errors.Join(err, closeErr)
  }
  if err != nil {
      h.writeAPIDomainError(w, r, err)
      return                      // <- archive != nil here if rootlessZIP succeeded
  }
  defer func() {                  // <- cleanup registered only after that return
      name := archive.Name()
      ...
  }()
  ```

  When `rootlessZIP` succeeds but `tree.Close()` fails, the early return
  orphans a fully written archive file. (`rootlessZIP` itself cleans up on
  its *own* error paths via its `owned` flag, `handlers.go:550-556` — that
  part is fine.)
- Exemplar for the sweep shape: `cmd/ts-skills/writer.go:113-158`
  (`sweepLitter`) — prefix-matched enumeration, unsafe-shape rejection
  (symlinks), errors joined into one `sweepErr`, and its caller
  `cmd/ts-skills/writer.go:90-92` fails acquisition when the sweep fails.

## Commands you will need

Run from the repo root (mise supplies the toolchain; the repo is already
`mise trust`ed).

| Purpose | Command | Expected on success |
|---|---|---|
| Focused tests | `go test ./internal/server/ -race -count=1` | `ok`, exit 0 |
| Full suite | `just test` | all packages `ok` |
| Static analysis | `just vet` | exit 0, no output |

## Scope

**In scope** (the only files you should modify):
- `internal/server/catalog.go`
- `internal/server/trees.go` (only if you place the sweep helper here)
- `internal/server/handlers.go` (the `publicationTree` restructure only)
- `internal/server/catalog_test.go`
- `internal/server/handlers_test.go`

**Out of scope** (do NOT touch, even though they look related):
- `cmd/ts-skills/writer.go` — the client sweep is the exemplar, not the target.
- `internal/server/daemon.go` — the drain semantics stay as they are; this plan only reclaims what abandonment leaves behind.
- `internal/safetree/` — staging creation stays where it is.

## Steps

### Step 1: Sweep `tmp/` in `openCatalog`, after the flock

After `openCatalog` has acquired `registry.lock` and
`ensureStorageDirectories` has succeeded, enumerate `tmpDir` and
`os.RemoveAll` every entry. Every name in that directory is a
process-owned temporary by construction (the three prefixes above), and
the flock proves no other daemon owns them — so removing *all* entries is
correct; do not build a prefix allowlist that would silently retain
unknown litter forever. Mirror the exemplar's error discipline: join
per-entry failures and fail `openCatalog` when the sweep fails, matching
how the client fails writer acquisition (`cmd/ts-skills/writer.go:90-92`).
Never touch `trees/` or anything outside `tmpDir`.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok` (existing
tests still pass; new tests come in the test plan).

### Step 2: Register archive cleanup before the tree-close error check in `publicationTree`

Restructure `internal/server/handlers.go:480-497` so that from the moment
`rootlessZIP` returns a non-nil archive, its close-and-remove cleanup is
guaranteed to run on every subsequent return path — including the
`tree.Close()` failure return. The existing deferred cleanup closure
(log-only, `handlers.go:490-497`) is the shape to keep; what moves is
*when* it takes effect relative to the error check. Response-path
semantics (headers, `http.ServeContent`) must not change.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

## Test plan

New tests in `internal/server/catalog_test.go` (structural pattern:
existing `openCatalog` tests in that file, e.g. the lock-conflict test):

1. Seed `<state>/tmp` with a `.ts-skills-download-x.zip` file, a
   `.ts-skills-tree-x/` directory containing a file, and a `.tree-x/`
   directory *before* opening the catalog. After `openCatalog` succeeds,
   assert `tmp/` is empty and `trees/` is untouched.
2. Open a catalog, publish nothing, close it, drop litter into `tmp/`,
   reopen — litter gone (proves the sweep runs on every open, not only
   first creation).

In `internal/server/handlers_test.go` (pattern: `newWebFixture`,
`handlers_test.go:52`): after a successful
`GET /api/v1/.../publications/{digest}/tree.zip` download completes,
assert the fixture's staging parent contains no `.ts-skills-download-*`
entries. (The tree-close-failure path itself has no injection seam through
the concrete catalog; it is covered by the structural guarantee of Step 2
— a reviewer checks there is no return between archive success and cleanup
registration.)

- **Verify**: `just test` → all pass, including the new tests.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `just test` → all packages `ok`
- [ ] `just vet` → exit 0
- [ ] `rg -n "ReadDir" internal/server/catalog.go internal/server/trees.go` shows the sweep enumeration exists
- [ ] No files outside the in-scope list are modified (`jj st`)

## STOP conditions

Stop if:

- The code at the "Current state" locations doesn't match the excerpts.
- A step's verification fails twice after a reasonable fix attempt.
- You find a fourth producer writing into `tmp/` whose entries might be
  shared across processes — the "everything in tmp is ours" assumption
  would be false.
- Sweeping at open breaks an existing test in a way that suggests some
  test relies on `tmp/` contents surviving a catalog reopen.

On stopping, write a **handback**: current state of the work, desired
outcome, lingering questions. Descriptive, not prescriptive.

## Maintenance notes

Any future code that writes temporaries into `<state>/tmp` inherits the
"swept at startup" contract automatically — that is the point. If a future
feature ever needs a temp file to survive a daemon restart, it must live
somewhere else, and this sweep is the place a reviewer will point to.
Plan 003 (read-route cost bounds) touches the same `publicationTree`
handler; land this plan first.
