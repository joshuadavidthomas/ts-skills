# Plan 002: Replace the journaled installer with stage-and-rename

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/consolidation/README.md.
>
> **Drift check (run first)**:
> `jj diff --from 2037ced944acc38456c090ff62e74c9de099318d --to @ -- internal/install/ internal/cli/`
> If in-scope files have changed since this plan was written, compare the
> "Current state" inventory against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: L
- **Risk**: HIGH (largest behavior change in the effort; deletes ~1,900
  production and ~1,900 test lines)
- **Depends on**: 001 (baseline transcript must exist)
- **Planned at**: revision `2037ced944acc38456c090ff62e74c9de099318d`, 2026-07-28
- **Owner decision (2026-07-28)**: crash-recoverable installs are dropped.
  The contract becomes *re-run convergence*: after a crash at any point,
  running `install` or `restore` again produces the correct project state.

## Why this matters

`internal/install` is ~2,600 production lines, of which the transactional
machinery — `journal.go` (357), `recovery.go` (344), `transaction.go`
(336), `durability.go` (215), plus the four `filesystem_*.go` fsync
variants and the `transactionFailure` seam threaded through everything —
exists to make "write skill files into `.agents/skills/`" crash-atomic
with journal replay. That is two-thirds of tsidp's *entire* production
line count spent on a property no comparable tool provides: golink's
durability story for its whole database is "SQLite plus periodic
snapshot"; package managers converge on re-run. The machinery also holds
the package's worst complexity: journal phases, backup/aside trees,
recovery classification, `ErrRecovered` propagation into CLI diagnostics.

## Current state (inventory)

Production files and what survives:

| File | Lines | Fate |
|---|---:|---|
| `journal.go` | 357 | delete |
| `recovery.go` | 344 | delete |
| `transaction.go` | 336 | delete |
| `durability.go` | 215 | delete (a ~40-line `fsutil.go` keeps `writeSyncedFile`-style helpers actually still needed) |
| `filesystem_linux.go` / `_bsd.go` / `_other.go` / `_windows.go` | ~80 | collapse to one `fsutil` seam for fsync; delete per-OS duplication |
| `installer.go` | 404 | rewrite core flow; `Install`/`Restore` signatures unchanged |
| `writer.go` | 268 | keep flock writer; replace recovery hook with staging/trash sweep |
| `project.go` | 219 | keep; delete `operationsDir()` and journal paths |
| `lock.go` | 152 | keep — format is on-disk contract, unchanged |
| `model.go` | ~110 | keep (003/005 touch it, not this plan) |
| `path_windows.go` / `path_other.go`, `windows.go` | ~60 | keep — path validation, unrelated to transactions |

Semantics that must survive verbatim (all landed by hardening plans):

- Fetch happens **before** writer acquisition; `ErrProjectChanged` rejects
  a snapshot that moved between fetch and lock (hardening 015).
- Digest verification of the staged tree before it reaches the
  destination (hardening 001/016: `agentskill.Inspect`).
- The flock writer lock and single-writer guarantee.
- Lock-file bytes: format, ordering, canonical forms — byte-identical for
  identical inputs (baseline transcript has the hash).
- Context cancellation reaching copy/hash work (hardening 001).

Semantics that are **deliberately removed**:

- Journal write-ahead + replay; `ErrRecovered` and its CLI diagnostic
  ("recovered prior update") in `internal/cli/cli.go`.
- Crash-point injection seam (`transactionFailure` /
  `transactionPoint(name)`) and the crash-matrix tests built on it
  (`transaction_test.go`, 1,124 lines).
- The multi-phase backup/aside orchestration in `transaction.go`.

## Target state

New install flow (`installer.go`, one readable function):

1. Fetch (unlocked, unchanged).
2. Acquire writer (flock). Acquisition sweeps stale `staging-*` and
   `trash-*` directories under `.agents/skills/` — best-effort removal,
   errors joined and reported, never fatal to the sweep of other entries.
3. Re-read lock; `ErrProjectChanged` check (unchanged).
4. Stage the verified tree into `.agents/skills/staging-<rand>/` —
   same directory ⇒ same filesystem by construction, so
   `requireSameFilesystem` (writer.go:77) is deleted, not relocated.
5. Verify staged digest (unchanged `Inspect` path).
6. Swap: if destination exists, `os.Rename(dest, trash-<rand>)`;
   `os.Rename(staging, dest)`; `os.RemoveAll(trash)` best-effort;
   fsync the parent directory once.
7. Write lock last: temp file in `.agents/`, fsync, `os.Rename` over
   `ts-skills.lock`, fsync parent. One ~25-line helper in `fsutil.go`.

Crash contract, stated in the package comment: *a crash can leave
`staging-*`/`trash-*` litter and, in a narrow window, a destination tree
newer or older than the lock says; both are repaired by re-running
`install` or `restore`, whose writer acquisition sweeps litter and whose
digest preflight (writer.go:244) already detects tree/lock disagreement.*

`Restore` keeps its shape (plan → fetch unlocked → reacquire → match →
apply) with the same per-skill swap; the lock is not rewritten by restore
(it is the input). A partial restore crash leaves some skills updated and
some not — re-run converges, which is already restore's contract.

Expected size: `internal/install` production ~2,600 → **~700 lines**;
tests ~1,950 → **~600**, all at behavior level.

## Steps

1. Write the new `fsutil.go` (synced write, synced rename, dir fsync —
   one build-tagged fsync no-op for platforms that need it) and the new
   swap in `installer.go`, keeping `Install`/`Restore` signatures.
2. Replace the writer-acquisition recovery hook (`writer.go:192
   recoverOrphanStaging` + journal replay) with the litter sweep.
3. Delete `journal.go`, `recovery.go`, `transaction.go`, `durability.go`,
   the `filesystem_*` variants, `ErrRecovered`, and the seam. Compiler
   drives the removal of every reference, including `internal/cli`'s
   recovered-update diagnostic branch.
4. Rewrite tests (new `installer_test.go`, keep `project_test.go` where
   still true): golden-path install; idempotent reinstall; upgrade
   replaces tree byte-exactly; second writer blocked while first holds
   flock; `ErrProjectChanged` on lock drift between fetch and acquire;
   convergence tests that *plant* crash artifacts by hand — a
   `staging-*` orphan, a `trash-*` orphan, a destination tree whose
   digest disagrees with the lock — then assert one `install`/`restore`
   run converges to the locked state; lock-file byte-stability against
   a golden fixture; context cancellation mid-copy.
5. Update the package comment with the crash contract, and
   `docs/development.md` if it names the journal.
6. Re-run the dev-mode golden path (001 §2) and diff lock-file hash,
   installed tree digest, and CLI failure diagnostics against the
   baseline transcript. Only the removed `ErrRecovered` diagnostic may
   differ; anything else is a regression.

## Verification

- `go build ./...`
- `go test -race ./internal/install/ ./internal/cli/ -count=1`
- `just check`
- `rg 'transactionFailure|transactionPoint|ErrRecovered|journal' internal/ cmd/`
  → no production hits.
- `wc -l internal/install/*.go` recorded in the reconciliation entry
  (target: ≤ ~800 production).
- Baseline diff from step 6 recorded.

## STOP conditions

- Any surviving semantic (fetch-before-lock, `ErrProjectChanged`, digest
  preflight, lock byte-stability, cancellation) requires weakening a test
  to pass — stop; that is a design error in this plan, not a test problem.
- The per-skill swap turns out to be observably non-convergent for
  restore (a planted-artifact state exists that re-run does *not* repair)
  — stop and hand back with the reproduction; do not add journaling back
  ad hoc.
- Lock-file bytes change for identical inputs — stop (on-disk contract).

## Maintenance notes

If a real multi-machine or long-running deployment later demands stronger
install atomicity, the answer is a *decision*, not a resurrection: state
the requirement, then choose between re-adding a journal or delegating to
the filesystem (e.g. staging + `renameat2` exchange). This plan's crash
contract is written into the package comment so the tradeoff stays
visible.
