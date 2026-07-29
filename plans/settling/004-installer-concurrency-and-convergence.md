# Plan 004: Let concurrent installs succeed and make trash recovery converge on every shape

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> `plans/settling/README.md`.
>
> **Drift check (run first)**:
> `jj diff --from 7b926628 -- cmd/ts-skills/installer.go cmd/ts-skills/writer.go cmd/ts-skills/installer_test.go`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: MED
- **Depends on**: none
- **Planned at**: working copy `7b926628` (parent `f33fe93e`), 2026-07-29

## Why this matters

Three defects in the installer's concurrency/recovery story, all found by
reading, all confirmed against the working copy:

1. Two concurrent `ts-skills install` runs for *different* skills —
   the obvious CI/bootstrap pattern — reliably fail one of them with
   "project changed while the registry was being read; try again", because
   `install` snapshots the lock file *before* fetching and rejects any
   difference after acquiring the writer. Nothing that install actually
   uses depends on that snapshot; every decision is made from state read
   under the flock.
2. A crash between `createTrash`'s `os.Mkdir` and its `record.json` write
   leaves a `.ts-skills-trash-pending-*` directory that **no recovery arm
   will ever touch**: both `recoverLockedTrash` and `trashIsStale` treat
   an unreadable record as "leave it", so the litter survives every future
   sweep, forever. A corrupt record with a moved-aside tree inside is
   silently retained the same way — worse, because that shape holds a
   user's previous skill contents.
3. In `replace`, the one failure branch between "old skill moved aside"
   and "new tree renamed in" that does *not* roll back is
   `verified.transfer()` — a bare `return err` that would leave the
   project with the skill missing. It is latent today (transfer fails only
   on a double-transfer programming error) but it is the single
   unprotected branch in an otherwise uniformly rollback-protected
   function.

The repo's recovery model is re-run convergence (the journaled installer
was deliberately removed — `plans/consolidation/README.md`, owner decision
1); items 2 and 3 are holes in that convergence guarantee, and item 1 is a
false conflict the model doesn't need.

## Current state

- `cmd/ts-skills/installer.go:27-51` — `install`:

  ```go
  fetchedLock, fetchedLockExists, err := readLockSnapshot(project)   // :31, pre-fetch, pre-flock
  ...
  fetched, err := i.remote.fetch(ctx, requirement)                   // :35, network, unlocked
  ...
  writer, err := project.acquireWriter(ctx)                          // :40, exclusive flock
  ...
  oldLock, oldBytes, hadLock, err := writer.readLock()               // :45, under flock
  ...
  if hadLock != fetchedLockExists || !bytes.Equal(oldBytes, fetchedLock) {
      return lockedSkill{}, errProjectChanged                        // :49-51, the false conflict
  }
  ```

  Everything after :45 uses `oldLock`/`oldBytes` (read under the flock):
  `assertManagedDestination` (:56), the no-op fast path (:69),
  `assertUnchanged` (:72), `oldLock.with` (:65). `fetchedLock` /
  `fetchedLockExists` are used at :49 and nowhere else (confirmed by
  grep). Design lineage: the fetch-before-lock + snapshot-reject pattern
  came from hardening plan 015, written for the old journaled installer;
  the consolidation (plan 002) replaced that installer with re-run
  convergence, which makes the reject-on-any-change rule needless for
  `install` — the requirement being installed does not derive from the
  lock. `restore` (`installer.go:81+`, `makeRestorePlan:147`,
  `plan.matches:167`) has a *different* situation — its plan derives from
  the lock — and is out of scope here.
- `cmd/ts-skills/installer.go:191` — `readLockSnapshot` definition; also
  called directly by tests (`installer_test.go:205,218,499`).
- `cmd/ts-skills/writer.go:221-228` — `recoverLockedTrash`:
  `record, skill, err := readTrashRecord(path); if err != nil || !record.HadDestination { return false, nil }`.
- `cmd/ts-skills/writer.go:286-300` — `trashIsStale`: same
  `if err != nil { return nil, nil }` on `readTrashRecord` failure.
- `cmd/ts-skills/writer.go:328-346` — `readTrashRecord` fails on: missing
  `record.json` (`os.ReadFile` → `fs.ErrNotExist`), symlinked record,
  JSON parse failure, skill-ID parse failure. Callers cannot tell these
  apart today.
- `cmd/ts-skills/writer.go:348-365` — `createTrash`: `os.Mkdir(path)`
  (:353) then `writeSyncedFile(record.json)` (:360) — the recordless
  window. The moved-aside tree goes into `<trash>/tree`
  (`trashTreeName`, see `installer.go:369`), always *after* the record
  exists.
- `cmd/ts-skills/installer.go:379-382` — `replace`:

  ```go
  staged, err := verified.transfer()
  if err != nil {
      return err                       // <- only branch here with no rollback
  }
  ```

  Neighbors at :373, :376, :384, :387, :392 all route through
  `w.rollbackReplacement(destination, trash, err, exists, ...)`.
- Sweep exemplar and entry point: `cmd/ts-skills/writer.go:113-158`
  (`sweepLitter`) → `:171-185` (pending-trash loop calling
  `recoverLockedTrash` then `trashIsStale`); sweep errors join into
  `sweepErr` and fail writer acquisition (`writer.go:90-92`).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Focused tests | `go test ./cmd/ts-skills/ -race -count=1` | `ok`, exit 0 |
| Full suite | `just test` | all packages `ok` |
| Static analysis | `just vet` | exit 0 |

## Scope

**In scope**:
- `cmd/ts-skills/installer.go`
- `cmd/ts-skills/writer.go`
- `cmd/ts-skills/installer_test.go`

**Out of scope**:
- `restore`'s snapshot semantics (`makeRestorePlan`/`matches`) — its plan
  genuinely derives from the pre-fetch lock; changing it is a separate
  design question.
- `cmd/ts-skills/lock.go`, `project.go` — lock codec and paths unchanged.
- `internal/server/` — server knows nothing of any of this.

## Steps

### Step 1: Drop the pre-fetch lock snapshot from `install`

Remove the `readLockSnapshot` call (:31-34) and the comparison (:49-51)
from `install`. The flock (:40) plus the under-lock reads (:45, :52) and
`assertUnchanged` (:72) retain every real protection. Then check remaining
`readLockSnapshot` callers: if only tests remain, the house rule applies
(nothing exists only for tests — `AGENTS.md`); fold its symlink-rejection
duty into wherever the lock is actually read on the install path
(`writer.readLock`) if it is not already there, and delete or repoint the
direct tests (`installer_test.go:205,218,499`). **Confirm before deleting:
`writer.readLock` must reject a symlinked lock file the way
`readLockSnapshot` does — if it doesn't, that guard moves, it does not
disappear.**

**Verify**: `go test ./cmd/ts-skills/ -race -count=1` → `ok`, and
`rg -n "errProjectChanged" cmd/ts-skills/installer.go` shows no install-path
use (restore-path uses remain).

### Step 2: Converge on recordless and corrupt trash

Split `readTrashRecord`'s failure classes so callers can distinguish
"record absent" from "record unreadable/corrupt" (e.g. return an error
matching `fs.ErrNotExist` untouched, wrap parse failures distinctly).
Then, in the sweep path:

- record **absent** and `<trash>/tree` **absent** → the directory carries
  no information (the `createTrash` crash window); treat as stale and
  remove it.
- record absent but `tree/` **present**, or record present but
  **unparseable** → surface as a real `sweepErr` (joined, fails
  acquisition with a message naming the path) instead of silently
  skipping. An operator must see this; it may hold a user's previous
  skill tree.

`recoverLockedTrash` keeps returning "not mine" for both new classes —
recovery decisions stay where they are; only the silent skip dies.

**Verify**: `go test ./cmd/ts-skills/ -race -count=1` → `ok`.

### Step 3: Route the `transfer` failure through rollback

Change `installer.go:379-382` so a `verified.transfer()` failure rolls
back like its neighbors: `return w.rollbackReplacement(destination, trash,
err, exists, false)`. No behavior change on any currently-reachable path.

**Verify**: `go test ./cmd/ts-skills/ -race -count=1` → `ok`.

## Test plan

Structural pattern: `cmd/ts-skills/installer_test.go` already covers
idempotency, rollback-at-every-step, and stale/uncommitted trash recovery
— extend it, matching its fixture style.

1. **Concurrent-install convergence** (Step 1): simulate process B by
   mutating the project lock between fetch and writer acquisition — the
   test remote's fetch hook (or a wrapper around `remote`) appends a
   different skill to `ts-skills.lock` mid-fetch; `install` must succeed
   and the final lock must contain both skills.
2. **Recordless trash, no tree** (Step 2): create
   `.agents/skills/.ts-skills-trash-pending-x/` empty; `acquireWriter`
   succeeds and the directory is gone.
3. **Recordless trash with tree** (Step 2): same, plus a `tree/` subdir
   with a file; `acquireWriter` fails with an error naming the path, and
   the directory survives.
4. **Corrupt record** (Step 2): `record.json` containing invalid JSON;
   `acquireWriter` fails naming the path; directory survives.
5. **Transfer rollback** (Step 3): force `verified.transfer()` to fail
   (construct the `verifiedTree` with ownership already transferred, via
   the same seams the existing rollback tests use); after the failed
   `replace`, the destination still holds the old tree and no
   `pending-` trash remains.

- **Verify**: `just test` → all pass, including the new tests.

## Done criteria

- [ ] `just test` → all packages `ok`
- [ ] `just vet` → exit 0
- [ ] `rg -n "readLockSnapshot" cmd/ts-skills/installer.go` → definition gone or restore/tests-only per Step 1's outcome
- [ ] No files outside the in-scope list are modified (`jj st`)

## STOP conditions

Stop if:

- The code at the "Current state" locations doesn't match the excerpts.
- `writer.readLock` turns out not to reject symlinked lock files and
  moving that guard is not a mechanical change (Step 1's assumption).
- Distinguishing `readTrashRecord` failure classes ripples into recovery
  *decisions* (which trash gets reinstated) rather than just reporting —
  recovery policy changes are a design fork.
- Test 1 cannot be written without new production seams — adding seams is
  a design decision; hand back with what you'd need.

On stopping, write a **handback**: current state, desired outcome,
questions. Descriptive, not prescriptive.

## Maintenance notes

After Step 1, `install` is serialized purely by the flock — two installs
of the *same* skill also both succeed (second becomes the no-op fast path
or a re-install; both converge). Reviewers should scrutinize Step 2's
"remove empty recordless trash" rule against the `createTrash` ordering:
the rule is only safe because the tree is moved in *after* the record is
written — if `createTrash` is ever reordered, this rule must be
revisited. That invariant deserves a comment at the `createTrash` site.
