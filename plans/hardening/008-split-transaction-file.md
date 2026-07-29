# Plan 008: Split internal/install/transaction.go by concern

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/install/`
> Plans 001, 004, 011, and 015 deliberately change in-scope files — expect
> drift from them; this plan is sequenced after them for exactly that reason. Compare
> "Current state" against the live code before proceeding; on a mismatch
> beyond those plans' known changes, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: LOW (pure move-split; must be zero behavior change)
- **Depends on**: 001-thread-context-through-tree-walks.md, 004-join-cleanup-errors.md, 011-validate-the-recovery-model.md, 015-decouple-fetch-from-writer-lock.md (all reshape transaction.go or installer.go; this move-split must run after they land)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

`internal/install/transaction.go` is 1,210 lines combining four concerns:
transaction application, durability primitives (sync/fsync choreography),
journal encode/decode, and crash recovery. The package is cohesive — this is
about navigability, not decomposition — but a reader looking for "how does
recovery work" currently scrolls through fsync internals to find it. A few
helpers (`preflightTransactionFilesystem`, `restoreOldState`) carry 9–10
correlated positional parameters, which conflates "the operation's paths and
state" with nine separate values at every call site
(`choose-plain-parameters-option-structs-or-functional-options.md`,
`package-names-and-cohesion.md`).

## Current state

Single file `internal/install/transaction.go` (1,210 lines) containing, in
order of concern:

- transaction apply orchestration (the exported CRUD used by
  `installer.go`; entry points around lines 57-181)
- durable write / sync helpers (272-499)
- journal read/write/verify (501-684)
- recovery — `restoreOldState`, journal replay, `recoveryError` at
  1014-1018:

  ```go
  // recoveryError deliberately exposes only ErrRecoveryRequired,
  // using %v for the implementation cause; this matches deliberate
  // wrapping guidance.
  ```

  (recoveryError's masking design is correct — preserve it unchanged.)

`preflightTransactionFilesystem` takes ten correlated arguments (paths +
state for one operation).

Conventions: package `install` stays; build-tagged sibling files
(`filesystem_*.go`, `path_*.go`) show the file-splitting style already in
use. AGENTS.md: `internal/install/` "installs and restores locked skills
with transactional project updates".

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (package) | `go test -race ./internal/install/ -count=1` | all pass, unchanged |
| Build | `just build` | exit 0 |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/install/transaction.go` (shrinks)
- new sibling files in `internal/install/` (see Step 1)
- `internal/install/transaction_test.go`, `internal/install/installer_test.go`
  ONLY if a test helper physically moved needs its imports adjusted — test
  assertions must not change

**Out of scope**:
- Any other package — no exported shape changes allowed; if you find one is
  required, STOP.
- Behavior, error strings, journal format, recovery policy — this is a
  move-split only. Do not "improve" anything while moving.
- The Linux/BSD duplication in `filesystem_linux.go`/`filesystem_bsd.go` —
  recorded in the index as deferred.

## Steps

### Step 1: Establish the file split

Create, moving code verbatim:

- `internal/install/journal.go` — journal encode/decode/verify and its types
- `internal/install/recovery.go` — `restoreOldState`, replay, `recoveryError`
- `internal/install/durability.go` — sync/fsync primitives and durable write helpers
- `internal/install/transaction.go` — what remains: transaction orchestration

Move entire functions; keep each move a pure cut/paste plus package-level
compile fixes. Land this as a compile-green state before touching anything
else.

**Verify**: `go build ./... && go test -race ./internal/install/ -count=1` → all pass.

### Step 2: Collapse correlated parameters

Introduce one small unexported struct per role — e.g.
`type transactionPaths struct` (project dir, staging dir, journal path,
skill destination) and, if 001 added context handling, keep `ctx` a separate
explicit parameter per project convention, NOT a struct field. Re-home the
long parameter lists of `preflightTransactionFilesystem` and
`restoreOldState` onto these. No other refactors.

**Verify**: `go build ./... && go test -race ./internal/install/ -count=1` → all pass.

### Step 3: Full gate

**Verify**: `just check` → exit 0; `rg "" internal/install/transaction.go | wc -l` (or `wc -l`) shows the file substantially reduced.

## Test plan

No new tests. The gate is the existing suite passing unchanged — that IS
the test plan for a move-split. If anything in the suite needs editing to
pass, the split changed behavior; find out why before proceeding.

**Verify**: `go test -race ./internal/install/ -count=1` → all pass with
zero assertion changes.

## Done criteria

- [ ] `wc -l internal/install/transaction.go internal/install/journal.go internal/install/recovery.go internal/install/durability.go` → four files, transaction.go clearly reduced
- [ ] No helper keeps a 9-10-argument signature
- [ ] `just test` → all pass, test files' assertions untouched
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The drift check shows changes beyond plans 001, 004, 011, and 015 in
  transaction.go — reconcile those lands first.
- The split requires an exported API change, moving code out of package
  `install`, changing behavior or persisted data, or transferring
  `Project` or `projectWriter` responsibilities. Same-package calls from
  `writer.go` and `project.go` are expected.
- You feel pulled to fix anything you spot along the way. Record it in the
  index's Deferred section via the handback; do not fold it in.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Concern-based file ownership: new journal logic goes in journal.go, new
  recovery logic in recovery.go. The point of the split is that future
  diffs announce their concern by which file they touch.
- `recoveryError`'s deliberate `%v` masking (transaction.go:1014-1018
  today) must survive any move verbatim — it's a security-shaped decision,
  documented at the site.
