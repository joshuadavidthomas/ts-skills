# 001 — Make web own browser upload limits

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Project-wide design audit: browser upload sizing leaks through runtime
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** none
**Executor target:** routine execution ready: yes
**Source type:** audit
**Audit category:** architecture
**Standards concern:** modules and effects
**Impact:** Removes multipart arithmetic and request-body policy from server composition
**Effort:** S
**Risk:** LOW; behavior stays on the same upload route
**Confidence:** HIGH; one browser workflow consumes the limit
**Source direction:** Derive and enforce the cap inside `internal/server/web`

## Purpose

Give the browser upload module complete ownership of its request size policy.
The root runtime should construct a web handler without knowing multipart
framing overhead or globally wrapping unrelated request bodies.

## What Better Means

`web.New` accepts tree policy and staging configuration, then privately applies
the derived body cap to candidate uploads. Root server configuration contains
no `MaxRequestBodyBytes`, and requests unrelated to upload are not governed by
browser multipart policy.

## Current-State Evidence

- `internal/server/web/limits.go:14-16` exports `UploadBodyCap`.
- `internal/server/runtime.go:142-150` and `:198-206` derive and thread the cap.
- `internal/server/router.go:26,68` forwards `MaxRequestBodyBytes`.
- `internal/server/web/handler.go:58,111-116` accepts and revalidates it.
- `internal/server/serve.go:101` applies the cap at the root HTTP server.

## Desired End State

- Browser upload cap derivation is private to `web`.
- The cap is applied only where `createCandidate` reads the request body.
- `runtime`, `handlerOptions`, `web.Options`, and `newHTTPServer` do not expose
  request-body cap configuration.
- Existing near-limit and overflow behavior remains covered.

## Scope

- `internal/server/web/limits.go`, `handler.go`, and tests
- `internal/server/runtime.go`, `router.go`, `serve.go`, and tests

## Out of Scope

- Changing tree limits or multipart field policy
- Changing machine-readable routes
- Introducing general middleware infrastructure

## Design Claim

Per `coding-standards/references/modules.md`, composition wires dependencies but
does not own a child module's policy. Per `effects.md`, externally sized input
must have an explicit owner and bound.

## Architecture Diagnosis

- **Current friction:** Runtime callers calculate and transport an
  implementation detail that `web.New` independently understands.
- **Deepening direction:** Web hides derivation and enforcement behind its
  existing `http.Handler` interface.
- **Deletion test:** Deleting the exported limit helper and option fields
  removes ceremony without spreading complexity; the calculation stays local.
- **Locality / leverage claim:** Future upload-format changes touch web only.
- **Recommendation strength:** Strong
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Characterize route-local body behavior

Preserve or add tests proving a maximal legal browser upload reaches tree
validation, an oversized upload is rejected, and non-upload requests retain
their existing behavior.

### Step 2 — Move enforcement into web

Make upload-cap calculation private. Apply `http.MaxBytesReader` at the
candidate-upload workflow before multipart parsing. Keep the current overflow
checks in the calculation.

### Step 3 — Delete root plumbing

Remove `MaxRequestBodyBytes` from all server/runtime option structures and the
runtime value. Simplify `newHTTPServer`; update tests to construct handlers
without calculating web limits.

## Verification

### Automated

- [ ] `go test ./internal/server/web ./internal/server -race -count=1`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] `rg -n "UploadBodyCap|MaxRequestBodyBytes" internal/server` returns no production matches.
- [ ] A near-maximum legal upload still reaches the tree-limit decision.
- [ ] The limit is applied exactly once, on browser candidate creation.

### Manual

- None.

## Autonomy Boundary

Routine execution may include private helper renames and test-fixture
simplification. Design review is required if the limit must remain global for a
request-smuggling or transport reason not represented in current tests. Human
approval is required to change accepted upload size or response behavior.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Run `jj st`.
- [ ] Re-open cited files and confirm the cap still follows this path.
- [ ] Run the narrow test command before editing.

## STOP Conditions

Stop if another request body consumer now relies on the root cap, the current
cap protects behavior beyond browser uploads, or route-local enforcement cannot
preserve current status/error semantics in one reviewable change.

## Rejected Approaches

- Keep the exported helper but move one call — retains the same ownership leak.
- Add generic body-limit middleware configuration — broadens an implementation
  detail instead of localizing it.

## Standing Policy Updates

None.

## Executor Notes

Do not modify the numerical cap formula. This is an ownership change, not a
limit-policy change.
