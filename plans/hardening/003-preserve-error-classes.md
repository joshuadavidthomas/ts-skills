# Plan 003: Preserve error classes across upload, client, and storage boundaries

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/upload/ internal/client/ internal/protocol/ internal/cli/ internal/storage/records.go`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: LOW
- **Depends on**: none (execute after 002 per the index order)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Three boundaries flatten error identity into the wrong class. Cancellation
and disk failures during upload become `ErrMalformedUpload` → the web layer
reports HTTP 400 "upload is invalid" for a client disconnect. The same
flattening in the client's ZIP decode turns `context.Canceled` into
`ErrProtocol`. And two protocol error codes (`invalid_request`, `internal`)
lose their identity entirely, so the CLI cannot map them to user-facing
wording. Per the reference notes (`wrap-errors-deliberately.md`,
`errors-are-values.md`): errors are control flow — only translate the
classes the boundary actually owns.

## Current state

**Flattening 1** — `internal/upload/upload.go:111-116`. Everything except
`ErrLimitExceeded` becomes malformed:

```go
addErr := builder.AddFile(ctx, entry.Path, entry.Size, counter)
if addErr != nil {
	if errors.Is(addErr, safetree.ErrLimitExceeded) {
		return nil, addErr
	}
	return nil, malformed("directory path is unsafe or collides with another path", addErr)
}
```

But `AddFile` (internal/safetree/safetree.go:129-130, and `copyContext`
189-190) also returns `ctx.Err()` and raw I/O errors from staging writes.

**Flattening 2** — `internal/client/client.go:309-315`, same shape inside
`decodeZIP`: any non-limit `AddFile` error becomes `%w: unsafe tree archive
entry: %v` with `protocol.ErrProtocol`, and the `%v` verb severs the chain.

**Flattening 3** — `internal/client/client.go:341-368`, `responseError`:
`CodeNotFound` and `CodeTooLarge` map to inspectable sentinels
(`registry.ErrNotFound`, `safetree.ErrLimitExceeded`), but:

```go
case protocol.CodeInvalidRequest:
	return fmt.Errorf("registry rejected the publication request")
case protocol.CodeInternal:
	return fmt.Errorf("registry could not complete the publication request")
```

— text only, no sentinel, so `cli.commandError` (internal/cli/cli.go:152-171)
cannot map them and they fall to the generic `"%s failed: %w"` branch. The
switch it must join maps each sentinel to user wording; exemplar branch:

```go
case errors.Is(err, registry.ErrNotFound):
	return fmt.Errorf("cannot %s because the requested skill publication was not found: %w", operation, err)
```

**Missing identity** — `internal/storage/records.go:21-27` (and the
publication/skill twins at 82-88 and 156-162):

```go
candidate, err := scanCandidate(row)
if errors.Is(err, sql.ErrNoRows) {
	return registry.Candidate{}, registry.ErrNotFound
}
```

Hiding `sql.ErrNoRows` is correct, but the bare sentinel loses which
candidate was requested. Wrap it: `fmt.Errorf("%s: %w", id, registry.ErrNotFound)`.

Wire codes live in `internal/protocol/protocol.go:33-46`; sentinel style
exemplar is `protocol.ErrProtocol` in the same file and the safetree
`LimitError{...}` pattern (Unwrap → `ErrLimitExceeded`).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/upload/ ./internal/client/ ./internal/cli/ ./internal/storage/` | all pass |
| Tests (all) | `just test` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/upload/upload.go` + `internal/upload/upload_test.go`
- `internal/client/client.go` + `internal/client/client_test.go`
- `internal/protocol/protocol.go` (add sentinels only; wire structs unchanged)
- `internal/cli/cli.go` + `internal/cli/cli_test.go` (commandError mapping only)
- `internal/storage/records.go` (error wrapping only)

**Out of scope**:
- `internal/web/web.go` — the handler's `handleError` switch already routes
  unknown classes to HTTP 500; that fallback is the *desired* behavior for
  non-malformed errors and needs no change.
- `Response Body.Close` / `archive.Close` ordering in `fetchTree` — plan 004
  owns cleanup-error joining.
- Any change to HTTP status codes or the `protocol.ErrorResponse` wire shape.

## Steps

### Step 1: Upload — translate only genuinely malformed input

In `StageBrowserDirectory` (internal/upload/upload.go:111-116), pass through
cancellation and operational I/O errors unchanged; wrap only
`safetree.ErrInvalidPath` (and keep the existing limit and size-mismatch
branches as-is):

```go
if addErr != nil {
	switch {
	case errors.Is(addErr, safetree.ErrLimitExceeded):
		return nil, addErr
	case errors.Is(addErr, context.Canceled), errors.Is(addErr, context.DeadlineExceeded):
		return nil, addErr
	case errors.Is(addErr, safetree.ErrInvalidPath):
		return nil, malformed("directory path is unsafe or collides with another path", addErr)
	default:
		return nil, fmt.Errorf("stage uploaded file %q: %w", entry.Path, addErr)
	}
}
```

(Shape, not final code — but the four-class split is the intent.)

**Verify**: `go test -race ./internal/upload/ -count=1` → all pass.

### Step 2: Client — cancellation survives ZIP decode

In `decodeZIP` (internal/client/client.go:309-315), detect
`context.Canceled`/`context.DeadlineExceeded` from `AddFile` before the
`ErrProtocol` wrap and return them unchanged. While there, change the two
`%v` causes on the existing `ErrProtocol` wraps in this function to `%w` so
the chain survives (wrap-errors-deliberately).

**Verify**: `go test -race ./internal/client/ -count=1` → all pass.

### Step 3: Client — inspectable identity for every valid remote code

Add two sentinels to `internal/protocol/protocol.go` beside `ErrProtocol`:

```go
var ErrInvalidRequest = errors.New("invalid registry request")
var ErrInternal = errors.New("registry internal error")
```

In `responseError`, replace the text-only branches:

```go
case protocol.CodeInvalidRequest:
	return fmt.Errorf("%w: %s", protocol.ErrInvalidRequest, wire.Message)
case protocol.CodeInternal:
	return fmt.Errorf("%w: %s", protocol.ErrInternal, wire.Message)
```

(`wire.Message` is server-generated prose; carrying it is fine.)

Add matching branches to `cli.commandError` (internal/cli/cli.go, in the
existing switch) giving each a user-facing sentence in the established
"cannot X because ... : %w" style.

**Verify**: `go test -race ./internal/client/ ./internal/cli/ -count=1` → all pass.

### Step 4: Storage — wrap not-found sentinels with identity

In `internal/storage/records.go` at 21-27, 82-88, and 156-162, wrap the
`registry.ErrNotFound` return with the requested identity (candidate /
publication / skill id as appropriate), keeping `sql.ErrNoRows` hidden,
e.g. `return registry.Candidate{}, fmt.Errorf("candidate %s: %w", id, registry.ErrNotFound)`.

**Verify**: `go test -race ./internal/storage/ ./internal/registry/ -count=1` → all pass.

### Step 5: Full gate

**Verify**: `just check` → exit 0.

## Test plan

- `internal/upload/upload_test.go`: staged `AddFile` cancellation returns
  something matching `context.Canceled`, NOT `ErrMalformedUpload`. The
  Builder ctx injection in `internal/safetree/safetree_test.go` is the
  structural exemplar.
- `internal/client/client_test.go`:
  - `decodeZIP` (via the fetch path) with a cancelled ctx yields
    `errors.Is(err, context.Canceled)` and not `protocol.ErrProtocol`.
  - `responseError` table cases for all four known codes: `not_found`,
    `invalid_request`, `too_large`, `internal` — asserting sentinel
    identity via `errors.Is` (extend the existing response-error coverage;
    per test-through-real-transports.md, drive these through the
    `httptest.Server` paths already used in this file).
- `internal/cli/cli_test.go`: `commandError` output for
  `protocol.ErrInvalidRequest` and `protocol.ErrInternal` hits the new
  branches, not the generic fallback.
- `internal/storage/catalog_test.go` (or records tests): not-found errors
  still match `registry.ErrNotFound` with `errors.Is` AND include the id in
  the message.

**Verify**: `just test` → all pass, including new tests.

## Done criteria

- [ ] `rg "context.Canceled" internal/upload/upload.go internal/client/client.go` → both files gate on it
- [ ] `rg "ErrInvalidRequest|ErrInternal" internal/protocol/ internal/cli/` → sentinels defined and mapped
- [ ] `go test -race ./internal/upload/ ./internal/client/ ./internal/cli/ ./internal/storage/` → all pass
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The "Current state" excerpts don't match.
- Preserving upload error classes turns out to *require* a web
  `handleError` change beyond its existing default branch (that would widen
  scope — hand it back).
- Including `wire.Message` in the wrapped error would expose something the
  server doesn't already send to every client — check
  `internal/web/web.go`'s API error responses first.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- New protocol codes must get a sentinel + `responseError` branch + CLI
  wording in the same change; treat the three as one edit unit. A reviewer
  should reject any future `fmt.Errorf` text-only branch in `responseError`.
- Upload error classes are now part of the HTTP contract: malformed → 400,
  limits → 413, everything else → 500. Web tests should keep that mapping
  pinned.
