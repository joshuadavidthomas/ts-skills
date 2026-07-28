# Plan 004: Join cleanup errors on failure paths instead of discarding them

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/install/ internal/storage/ internal/web/web.go internal/cli/cli.go`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: LOW
- **Depends on**: none (execute after 003 per the index order)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Success paths in this codebase already pair acquisition with cleanup and
join the errors — the failure paths mostly don't. Dropped `Close` /
`Rollback` / `RemoveAll` errors hide leaked file locks, abandoned staging
directories, and open transactions exactly when something else just went
wrong and diagnostic value is highest. The pattern to standardize on already
exists in the codebase:

```go
// internal/daemon/daemon.go:263-266
defer func() {
	err = errors.Join(err, active.close())
}()
```

One site (`storage/catalog.go` tx defer) is worse than noisy: a deferred
rollback gated on `err != nil` leaves the transaction open if a panic
escapes mid-`Publish`.

## Current state

All sites below discard errors today (`_ = ...` or equivalent):

1. `internal/install/writer.go:115-146` — every failure path after the
   `fileLock` is created/acquired drops its `Close` error, including
   recovery-failure paths where the writer owns the lock.
2. `internal/install/project.go:137-155` and
   `internal/install/installer.go:196-205` — failed staging setup/copy calls
   `os.RemoveAll` and discards the result.
3. `internal/install/installer.go:142-150, 164-171` — when tree/snapshot
   `Close` fails, a subsequent staging-cleanup error is joined *under the
   outer close label*, so both errors read as coming from one operation.
   Wrap each separately, then join.
4. `internal/storage/catalog.go:76-81` — the TryLock-conflict branch drops
   `stateLock.Close()`:

   ```go
   if !locked {
       _ = stateLock.Close()
       return nil, fmt.Errorf("lock registry state directory: %w", registry.ErrConflict)
   }
   ```

   (Sibling branch two lines up already joins correctly — match it.)
5. `internal/storage/catalog.go` `Publish` (255-345) and `SelectCurrent`
   (321-346):

   ```go
   defer func() {
       if err != nil {
           err = errors.Join(err, tx.Rollback())
       }
   }()
   ```

   Rollback only runs when the named error is non-nil — a panic leaks the
   tx, and a post-`Commit` error would be joined with a confusing
   `sql.ErrTxDone`. Also, `NewPublishResult` currently runs *after*
   `tx.Commit()`, so a fallible constructor sits past the commit point.
6. `internal/storage/trees.go:218-220` — output-creation failure after the
   input opened: `_ = input.Close()`.
7. `internal/web/web.go:285-286` — `defer func() { _ = submission.Close() }()`
   so a removal failure on a successfully-staged upload is invisible;
   `web.go:488-492` — generated ZIP `Close` + `os.Remove` discarded.
8. `internal/cli/cli.go:118-146` — `commandInstaller` returns
   `cleanup func()` and `cleanup := func() { _ = os.RemoveAll(staging) }`;
   constructor-failure paths call it and discard the removal error.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/install/ ./internal/storage/ ./internal/web/ ./internal/cli/ -count=1` | all pass |
| Tests (all) | `just test` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/install/writer.go`, `internal/install/project.go`,
  `internal/install/installer.go`
- `internal/storage/catalog.go`, `internal/storage/trees.go`
- `internal/web/web.go`
- `internal/cli/cli.go`
- test files of the above packages

**Out of scope**:
- `internal/install/transaction.go` — its write-side close/sync errors are
  already joined correctly (398-430); restructuring the file is plan 008,
  context changes are plan 001.
- Adding a logger to `internal/web` — plan 005 owns it. For web cleanup
  errors that surface only after the response is committed, just leave a
  `// TODO(plan 005): log` comment; do not invent logging here.
- `internal/daemon/` — already joins its cleanup correctly.

## Steps

### Step 1: install package failure paths

`writer.go:115-146`: convert to named error returns where needed and join
`fileLock.Close()` on every failure path after acquisition. Exemplar:
`internal/install/installer.go:41-45` already joins writer cleanup — match
its wrapping labels exactly.

`project.go:137-155` / `installer.go:196-205`: join the `os.RemoveAll`
result into the returned error, preserving today's orphan-recovery policy
(report the failure; don't change policy).

`installer.go:142-150, 164-171`: wrap the close error and the
staging-removal error with their own operation labels, then
`errors.Join(wrappedClose, wrappedRemoval)`.

**Verify**: `go test -race ./internal/install/ -count=1` → all pass.

### Step 2: storage lock and copy paths

`catalog.go:76-81`: `return nil, errors.Join(fmt.Errorf("lock registry state directory: %w", registry.ErrConflict), stateLock.Close())` — the sentinel stays matchable through `errors.Is`.

`trees.go:218-220`: join the input close with the create error:
`return fmt.Errorf("create staged tree file %q: %w", name, errors.Join(err, input.Close()))` (or move the per-file copy into a helper with cleanup established immediately — either is fine; keep the error label intact).

**Verify**: `go test -race ./internal/storage/ -count=1` → all pass.

### Step 3: storage transaction defers

In `Publish` and `SelectCurrent` (and audit for other `BeginTx` users in
`catalog.go`): always rollback, ignore only the post-commit case:

```go
defer func() {
	if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
		err = errors.Join(err, rbErr)
	}
}()
```

Then move all fallible value construction (`registry.NewPublishResult`,
`NewCurrentPublication`-style calls) to *before* `tx.Commit()`, so nothing
fallible runs after the commit point.

**Verify**: `go test -race ./internal/storage/ -count=1` → all pass.

### Step 4: web staged uploads and generated archives

`web.go:280-286`: close the submission explicitly at each success point
(before the redirect response is written) so a close failure can still
become `h.handleError(w, err)`; keep a defer only as the early-return
safety net. `web.go:485-492`: hoist `archive.Close()` + `os.Remove` ahead
of the response write and join their errors into the error path. Anything
that can only fail after the response is committed: leave it discarded with
a `// TODO(plan 005): log` comment.

**Verify**: `go test -race ./internal/web/ -count=1` → all pass.

### Step 5: CLI staging cleanup

`cli.go:118-146`: change `commandInstaller` to return `cleanup func() error`.
Its internal failure paths join the cleanup result with the constructor
error. `runInstall`/`runRestore` (cli.go:80-116): named error returns,
`defer func() { err = errors.Join(err, cleanup()) }()`.

**Verify**: `go test -race ./internal/cli/ -count=1` → all pass.

### Step 6: Full gate

**Verify**: `rg "_ = .*\.(Close|RemoveAll)\(" internal/install/ internal/storage/ internal/web/ internal/cli/` → matches only lines with a `TODO(plan 005)` comment or demonstrably post-commit paths; `just check` → exit 0.

## Test plan

Failure-injection seams already exist — exemplar: `safetree.Builder`'s
`removeAll func(string) error` field (internal/safetree/safetree.go:65) and the
lock/seam injection in `internal/install/installer_test.go`. Add:

- install: close-failure on the failure path is reported (joins with primary error).
- storage: `Rollback` returning `sql.ErrTxDone` after successful commit is NOT
  reported; a genuine rollback failure IS joined.
- cli: `os.RemoveAll` failure on a constructor-failure path appears in the
  returned error.

**Verify**: `go test -race ./internal/install/ ./internal/storage/ ./internal/cli/ -count=1` → all pass, including new tests.

## Done criteria

- [ ] The Step-6 `rg` audit shows no unexplained discards
- [ ] No deferred rollback is gated on `err != nil`
- [ ] `just test` → all pass
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The "Current state" excerpts don't match.
- Joining a cleanup error changes what `errors.Is` callers observe in a way
  tests don't already pin (joined errors are matchable, but a changed
  primary error is not).
- The web handler changes appear to need response-shape changes (redirect
  targets, status codes) — that's plan 005 territory; hand back.
- You find an `os.RemoveAll`/close on a committed-install path that is
  deliberately undocumented — one documented case exists
  (transaction.go:161-168, cleanup after commit, orphaned state is
  recoverable); anything else without a comment is a question, not a fix.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- The rule going forward: any `_ = x.Close()` / `_ = os.RemoveAll(...)` in a
  failure path needs either a `errors.Join` or a comment naming why the
  error is unrecoverable-and-irrelevant. Reviewers should ask for one or
  the other.
- `errors.Join` ordering convention: primary (operation) error first,
  cleanup second — `daemon.go:263-266` sets it; keep it uniform.
