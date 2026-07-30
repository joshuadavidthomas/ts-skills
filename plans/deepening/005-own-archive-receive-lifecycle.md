# 005 — Own the archive receive/decode lifecycle

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Project-wide design audit: temporary archive state leaks into client fetch
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 004-explicit-tree-format-policy
**Executor target:** routine execution ready: yes
**Source type:** audit
**Audit category:** architecture
**Standards concern:** modules and effects
**Impact:** Gives receipt, validation, decoding, and temporary archive cleanup one owner
**Effort:** M
**Risk:** MED; malformed archive and cleanup failures must retain classifications
**Confidence:** HIGH; there is one production receive/decode workflow
**Source direction:** Replace the two-stage receive/decode protocol with one operation returning an owned snapshot

## Purpose

Hide the intermediate temporary archive and its mutation/cleanup ordering from
the client. Archive creation for server responses remains a separate lifetime
because its caller genuinely needs a seekable artifact while writing HTTP.

## What Better Means

The client asks the tree module to receive and decode a bounded archive under a
caller-selected staging parent and format policy. The operation cleans its
intermediate archive on every path and returns one owned `Snapshot`. The caller
has one `Close` obligation.

## Current-State Evidence

- `internal/tree/archive.go:79` returns a temporary `*Archive`.
- `internal/tree/decode.go:82` accepts that archive, closes its internal file,
  mutates its state, and returns a `*Snapshot`.
- `internal/client/remote.go:128-167` must defer archive cleanup, call decode,
  defer snapshot cleanup, inspect, verify, and transfer ownership.
- Server response archive creation has a different legitimate lifetime and is
  used by `internal/server/api`.

## Desired End State

- One tree operation owns bounded receipt, archive structural validation,
  decoding, and intermediate cleanup.
- It accepts the explicit format from plan 004 and returns an owned snapshot.
- Cleanup failures are joined without obscuring the primary tree/protocol error.
- The client no longer sees or closes a received `Archive`.
- Server-side archive encoding/response ownership remains separate.

## Scope

- `internal/tree/archive.go`, `decode.go`, and tests
- `internal/client/remote.go` and tests
- Private/archive helpers made obsolete by the deeper operation

## Out of Scope

- Changing ZIP format, limits, HTTP protocol, redirects, or identity verification
- Moving network I/O into `internal/tree`
- Combining publication verification with generic archive decoding
- Redesigning the subsequent project transaction; plan 007 owns that

## Design Claim

Per `coding-standards/references/modules.md`, callers should request an outcome
rather than perform a module's private checklist. Per `effects.md`, the module
that creates a temporary resource owns cleanup unless ownership is explicitly
transferred.

## Architecture Diagnosis

- **Current friction:** The client knows that decode closes and invalidates an
  archive before snapshot ownership exists.
- **Deepening direction:** The tree module owns the intermediate lifecycle and
  exposes only the durable decoded artifact.
- **Deletion test:** Deleting the combined operation would spread receive,
  close, decode, and error-joining order back into callers.
- **Locality / leverage claim:** Archive lifecycle fixes and malformed-input
  handling become local to tree tests.
- **Recommendation strength:** Strong
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Characterize failures and cleanup

Cover short bodies, declared-size mismatches, oversized archives, invalid ZIP
structure, decode failure, cancellation, successful receipt, and cleanup-error
joining through the new caller-facing operation.

### Step 2 — Introduce the owned receive operation

Build the operation from existing receipt and decode implementations. Keep
network/header parsing in `client.remote`; pass only an `io.Reader`, declared
size, staging parent, and explicit tree format into tree.

### Step 3 — Simplify client ownership

Replace the two defers in `remote.fetchTree` with one snapshot ownership path.
Keep Agent Skill inspection and publication verification in registry/client
logic. Delete obsolete production-facing receive/decode helpers if no second
production caller remains.

## Verification

### Automated

- [ ] `go test ./internal/tree ./internal/client -race -count=1`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] `internal/client/remote.go` has one temporary tree ownership obligation.
- [ ] No received archive temp file remains after success, malformed input,
  cancellation, or decode failure.
- [ ] Protocol error classes and publication verification remain unchanged.
- [ ] Server archive-response tests still pass through their existing lifetime.

### Manual

- None.

## Autonomy Boundary

Routine execution may make old receive/decode helpers private or delete them
when there is no production caller. Design review is required if cleanup errors
need a new public failure contract. Human approval is required for archive,
limit, digest, or protocol behavior changes.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plan 004's explicit format exists and use it.
- [ ] Inventory all `ReceiveArchive` and `DecodeArchive` callers.
- [ ] Run narrow tests before editing.

## STOP Conditions

Stop if a second production caller needs the intermediate received archive,
tree code would need to understand HTTP semantics, or cleanup failure
classification cannot be preserved.

## Rejected Approaches

- Add a helper in `client` that still composes both tree lifetimes — leaves the
  shallow tree interface unchanged.
- Return both archive and snapshot — expands the state space.
- Merge response archive creation into the same operation — it has a different
  ownership contract.

## Standing Policy Updates

Temporary archive receipt and decoding form one owned tree operation.

## Executor Notes

The returned snapshot is still caller-owned. Preserve `errors.Join` behavior so
cleanup diagnostics do not replace the primary failure.
