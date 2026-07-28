# Plan 001: Thread context through tree walking, hashing, and serving

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/agentskill/ internal/storage/ internal/registry/ internal/client/ internal/install/ internal/web/`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: L
- **Risk**: MED
- **Depends on**: none
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Cancellation and deadlines never reach filesystem work. `agentskill.SumTree`
walks and hashes an unbounded tree with no context, the web UI's
`resolveTreeFile` walks and reads trees ignoring `r.Context()`, and install
preflight/sync can't observe cancellation. This is the project's stated
convention (AGENTS.md: "Thread `context.Context` through network, storage,
and long-running filesystem work") violated at its largest seams. It also
blocks plan 002: daemon shutdown can only interrupt stuck handlers if their
request context actually cancels the work.

## Current state

The root cause is one signature — `internal/agentskill/digest.go:41`:

```go
func SumTree(fsys fs.FS, dir string) (TreeDigest, error) {
```

The walk and per-file hashing inside it have no context checks. Every
caller inherits the hole:

- `internal/storage/trees.go:107-153, 245-256, 361-364` — capture, verify,
  and sync call into tree hashing with a ctx available but undelivered.
- `internal/registry/catalog.go:71-76` — publish path hashes the staged tree.
- `internal/client/client.go:216-218` — `fetchTree` calls
  `agentskill.SumTree(snapshot.FS(), ".")` after ZIP staging; cancellation
  cannot interrupt it.
- `internal/install/transaction.go:433-466` — preflight hashing;
  `internal/install/transaction.go:501-530` — `syncTree` walks.
- `internal/install/writer.go:93-108` — `acquireWriter` creates and syncs
  directories before checking whether ctx is already cancelled.
- `internal/web/web.go:217` — handler calls
  `resolveTreeFile(tree, r.URL.Query())`; the function at `web.go:663-728`
  does `fs.WalkDir(tree, ".", ...)` then `fs.ReadFile(tree, selectedPath)`
  with no context.

The context-aware patterns to imitate already exist:

- `internal/safetree/safetree.go:186-212` — `copyContext(ctx, dst, src)`
  checks `ctx.Err()` each loop iteration while streaming.
- `internal/storage/trees.go:220-224` — `contextReader` wraps an
  `fs.File`/`io.Reader` so `io.Copy` observes cancellation:
  `io.Copy(output, &contextReader{ctx: ctx, source: input})`.
- `internal/safetree/safetree.go:129-130` — `AddFile` returns `ctx.Err()`
  directly; callers detect it with `errors.Is(err, context.Canceled)`.

Convention: context-first parameter, named `ctx`, never stored on structs
(project-wide; see `client.Remote.get` at internal/client/client.go:329-341).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `just build` | exit 0 |
| Tests | `just test` | race-enabled tests, all pass |
| Static analysis | `just vet` | exit 0 |
| Full gate | `just check` | tests, lint, vet, vuln, tidy all pass |

(Verified in `justfile` / AGENTS.md.)

## Scope

**In scope** (the only files you should modify):
- `internal/agentskill/digest.go`
- `internal/agentskill/agentskill_test.go`
- `internal/storage/trees.go`
- `internal/storage/catalog.go` (only if it calls tree hashing — confirm)
- `internal/registry/catalog.go` (call-site updates only)
- `internal/client/client.go`
- `internal/install/writer.go`
- `internal/install/transaction.go` (call-site/phase checks only — no restructuring)
- `internal/install/installer.go` (call-site updates only)
- `internal/web/web.go`
- test files of the above packages, as needed

**Out of scope** (do NOT touch, even though they look related):
- `internal/agentskill/directory.go`, `document.go`, `name.go` — `Load`,
  `LoadDir`, and `Parse` read a single bounded file; adding ctx there is a
  separate decision (deferred, see README).
- `internal/safetree/safetree.go` — already context-aware; plan 008 of this
  effort touches it separately.
- `internal/daemon/` — shutdown semantics are plan 002.
- `internal/protocol/` — wire types unchanged.

## Steps

### Step 1: Give `SumTree` a context

Change the signature to `func SumTree(ctx context.Context, fsys fs.FS, dir string) (TreeDigest, error)`.
Inside the walk, check `ctx.Err()` per directory entry (matching the
`safetree.copyContext` loop style). Hash file contents through a
cancellation-aware stream — either reuse the `contextReader` pattern or an
inline loop modeled on `safetree.copyContext`.

**Hard invariant**: digest values must not change. The fixed test vectors in
`internal/agentskill/agentskill_test.go` (`TestSumTreeFixedVectors`,
`TestSumTreeIgnoresModesAndMapOrder`) must pass unmodified — cancellation
checks must observe, never participate in, the hash input.

Update `agentskill_test.go` call sites to pass a context. The codebase's tests
use explicit `context.Background()` (see `internal/storage/catalog_test.go`)
— match that rather than introducing `t.Context()`.

**Verify**: `go build ./... && go test -race ./internal/agentskill/` → all
pass (other packages will not compile yet — that's expected mid-step; the
build gate only checks agentskill compiles, run `go test` scoped).

### Step 2: Propagate through storage and registry

Update the `SumTree` call sites in `internal/storage/trees.go`
(capture/verify/sync paths) and `internal/registry/catalog.go` to pass the
`ctx` already in scope. If any storage function is itself missing a ctx it
needs, add the parameter context-first.

**Verify**: `go test -race ./internal/storage/ ./internal/registry/` → all pass.

### Step 3: Propagate through client and install

- `internal/client/client.go` `fetchTree`: pass its `ctx` to `SumTree`.
- `internal/install/writer.go` `acquireWriter`: check `ctx.Err()` before
  creating/syncing any directories (writer.go:93-108).
- `internal/install/transaction.go`: pass ctx into preflight hashing
  (433-466) and `syncTree` (501-530), checking `ctx.Err()` **between
  journaled phases only**. Recovery (`restoreOldState` and journal replay)
  must never bail out mid-recovery on cancellation — a half-restored
  project is worse than a slow one. State checks belong before starting
  each phase, not inside recovery loops.

**Verify**: `go test -race ./internal/client/ ./internal/install/` → all pass.

### Step 4: Make `resolveTreeFile` context-aware

`internal/web/web.go:663-728`: change the signature to
`resolveTreeFile(ctx context.Context, tree fs.FS, query map[string][]string)`.
Check `ctx.Err()` inside the `fs.WalkDir` callback (return it; WalkDir
propagates it). Replace the final `fs.ReadFile(tree, selectedPath)` with an
open + chunked copy through a `contextReader`-style wrapper so a cancelled
request stops mid-read. Update the call site at `web.go:217` to pass
`r.Context()`.

**Verify**: `go test -race ./internal/web/` → all pass.

### Step 5: Full gate

**Verify**: `rg "SumTree\("` → every call site passes a context as the
first argument. `just check` → exit 0.

## Test plan

New tests, each modeled on existing structure in the same package
(`internal/safetree/safetree_test.go` is the exemplar for ctx-injection
tests):

- `internal/agentskill/agentskill_test.go`: `TestSumTreeRespectsCancellation`
  — pre-cancelled context returns an error matching
  `errors.Is(err, context.Canceled)` and a zero digest.
- `internal/web/web_test.go`: cancelled request context to
  `resolveTreeFile` surfaces `context.Canceled` (call directly with a
  cancelled ctx; the tree fixture can be an `fstest.MapFS`).
- `internal/install/transaction_test.go`: pre-cancelled ctx fails before
  any project mutation (assert the project directory is byte-identical
  afterward — reuse an existing fixture's comparison helper if one exists).

**Verify**: `go test -race ./internal/agentskill/ ./internal/web/ ./internal/install/ -count=1` → all pass, including the new tests.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `rg "func SumTree\(ctx context\.Context"` internal/agentskill/digest.go → one match
- [ ] `rg "SumTree\("` → no call site omits a context first argument
- [ ] `just check` → exit 0
- [ ] Fixed digest vectors pass unmodified (no diff to
      `TestSumTreeFixedVectors` expectations)
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The code at the "Current state" locations doesn't match the excerpts.
- A `SumTree` caller surfaces outside the listed files (the signature
  change should ripple predictably; anything unexpected means the coupling
  map is wrong).
- Making hashing cancellation-aware changes any digest value — digests are
  the registry's address scheme; a change there is a protocol break.
- Passing ctx into install transactions appears to require abandoning an
  in-flight recovery on cancellation. Do not design that here.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback** for the planning agent instead of
improvising: current state of the work, desired outcome, lingering
questions. Descriptive, not prescriptive.

## Maintenance notes

- Future filesystem walks or bulk reads in any package must take ctx per
  AGENTS.md; reviewers should reject new `fs.WalkDir`/`io.Copy` loops that
  can't be cancelled.
- `agentskill.Load`/`LoadDir` deliberately remain context-free (single
  bounded file read); if skills grow large bodies, revisit.
- After this lands, plan 002 can rely on `r.Context()` actually stopping
  handler work — do not reorder them.
