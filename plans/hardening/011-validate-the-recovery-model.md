# Plan 011: Validate the recovery journal as one model; test recovery against real crashes

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/install/`
> Plans 001 and 004 legitimately change in-scope files — reconcile against
> landed work; on an unexpected mismatch, treat it as a STOP condition.

## Status

- **Effort**: L
- **Risk**: HIGH (recovery correctness; wrong moves can orphan user project state)
- **Depends on**: none hard; MUST land before 008 (which is a move-split of these files)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Crash recovery is the highest-stakes code in the project — its one job is
to decide, from on-disk remnants, whether an interrupted transaction
committed or not. Today that decision is built from independently validated
string fields: a journal can carry a valid new-lock snapshot that selects
digest A while its own `NewDigest` field and the hashed destination say B,
and recovery will classify everything as "new" and finalize a *split*
state. That's a state-modeling failure: the journal is flat data where
recovery needs one cross-validated operation. Compounding it, the fault
matrix tests simulate *normal error returns* — a real crash never runs
defer chains, and the matrix is discovered from the same injection hook it
tests.

## Current state

Flat journal — `internal/install/transaction.go:32-43`: `transactionJournal`
stores phase, provenance flags (`HadDestination`, `HadLock`-style
booleans), digests, and lock hashes as independent primitives.

Piecemeal decode — `internal/install/transaction.go:773-799` (`readJournal`
validates each field in isolation) and `687-699` (`readLockSnapshot`
decodes each lock snapshot and discards the value). Recovery then derives
lock state from snapshot hashes (`581-584`) and destination state from
journal digests (`561-565`), and finalizes when the two *independent*
classifications agree (`630-637`).

Unstated implications a maintainer must hold: e.g. `HadDestination`
implies the old lock contains a skill entry, which implies `HadLock`.
Nothing checks these.

Crash-simulation gap — `internal/install/transaction.go:45-53`
(`transactionFailure` injection hook), discovered by the matrix in
`transaction_test.go:617-636`, then re-entered for "recovery" through
normal `Install` returns (`transaction_test.go:145-163`) and the private
`project.acquireWriter` re-entry at `670-681`. Normal returns run the
writer/verified-tree defers that a crash never would.

Lenses: `state.md` (transitions explicit), `boundaries.md` (persisted data
→ validated model before acting), `verification.md` (evidence must match
the crash claim).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (package) | `go test -race ./internal/install/ -count=1` | all pass |
| Crash tests | `go test -race ./internal/install/ -run 'Crash|Recover' -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/install/transaction.go` (journal decode, recovery classification)
- `internal/install/transaction_test.go`, `internal/install/installer_test.go`
- New test helper(s) under `internal/install/` for subprocess crash tests

**Out of scope**:
- Journal file *format* on disk — the encoding stays byte-compatible;
  decode/validate into a model, don't change what's written. (Changing the
  written format is a compatibility decision that belongs in a memo.)
- `internal/cli`, `internal/web` — user-facing recovery messaging is plan 015.
- transaction.go file-splitting — plan 008 runs *after* this plan.

## Steps

### Step 1: Give the journal a validated model

Introduce (unexported) a decoded recovery operation — sketch:

```go
type recoveryPhase uint8 // unknown, prepared, committed-named phases — no bare strings

type recoveryOperation struct {
	skill  registry.SkillID
	phase  recoveryPhase
	hadDestination, hadLock bool
	oldDigest, newDigest    agentskill.TreeDigest // optional-presence modeled, not empty-string
	oldLock, newLock        lockSnapshot          // decoded, retained
}
```

`readJournal` becomes a two-stage parse: decode raw fields, then construct
`recoveryOperation` with ALL cross-field invariants checked — at minimum:
(a) snapshot-selected digests must equal the journal's old/new digests;
(b) flag implications (`hadDestination ⇒ old lock carries the skill ⇒ hadLock`);
(c) phase is one of the known ordering points. Any violation → recovery
fails closed (the existing `ErrRecoveryRequired` path with its deliberate
`%v` masking at transaction.go:1014-1018 — preserve that masking exactly).

**Verify**: `go build ./internal/install/ && go test -race ./internal/install/ -count=1` → existing recovery tests pass against the new model.

### Step 2: Cross-field corruption tests

Table-driven tests at the journal-decode seam: valid journal with
mismatched snapshot digest; missing implied lock; unknown phase; truncated
snapshot. Each must fail closed, never "recover" into mixed state.
Exemplar: the existing journal-decode tests at the same seam.

**Verify**: `go test -race ./internal/install/ -run 'Journal|Recovery' -count=1` → all pass.

### Step 3: Crash-realistic tests

Add subprocess crash cases alongside the injection matrix. Shape: a test
binary (the standard `TestMain`/`helper process` pattern — see stdlib
examples) runs `Install`/`Restore` against fixture project + fake remote,
then `os.Exit(0)` (or `runtime.Goexit`-free hard exit — must NOT run
defers) at each independently *listed* crash window (journal written, files
staged, renames half-done, lock replaced). The parent process re-enters via
the PUBLIC `Installer.Install`/`Installer.Restore` (not
`project.acquireWriter`) and asserts final lock/tree/destination agreement
by reading the project — no private-field assertions.

Keep `transactionFailure` injection for faults that are hard to cause
(fsync failure). The window list is explicit in the test file — if someone
adds a new mutating step later, adding its window is a deliberate act, not
accidental coverage.

**Verify**: `go test -race ./internal/install/ -count=1` → matrix + crash
cases pass; `rg "_ = .*\.(lockedFiles|staging)" internal/install/transaction_test.go`
-style private assertions reduced to outcomes (where they've been replaced).

### Step 4: Full gate

**Verify**: `just check` → exit 0.

## Test plan

Covered in Steps 2–3. One more: a journal written by the CURRENT build
must still recover correctly after this change (byte-compatibility guard):
commit a fixture produced by the pre-change encoder into the test data.

**Verify**: `go test -race ./internal/install/ -count=1` → all pass.

## Done criteria

- [ ] Journal decode produces one validated `recoveryOperation` (or fails closed)
- [ ] `rg "readLockSnapshot|transactionJournal" internal/install/transaction.go` → snapshot values are retained and cross-checked, not discarded
- [ ] Crash windows tested through real subprocess exits and public re-entry
- [ ] Pre-change journal fixture recovers correctly
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The drift check shows plan 008 already split transaction.go (order was
  violated — reconcile against the new file layout, but if recovery logic
  also changed, hand back).
- Byte-compatible modeling proves impossible for a real inconsistency class
  (e.g. the current format can't represent "absent digest" distinctly) —
  that's a format-version decision; handback, don't version silently.
- A cross-field invariant you intend to enforce is *legitimately* violated
  by a state a healthy pre-fix process could leave — that changes the
  recovery policy, not just the model; handback.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Any new mutating step in a transaction must (a) appear in the crash
  window list and (b) extend the journal model's invariants if it adds
  persisted fields. The window list location should be obvious in the test
  file header comment.
- Keep `recoveryError`'s `%v` masking at transaction.go:1014-1018 verbatim —
  plan 008's maintenance note repeats this; it's load-bearing.
