# Plan 002: Derive the HTTP request body cap from the configured tree limits

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> `plans/settling/README.md`.
>
> **Drift check (run first)**:
> `jj diff --from 7b926628 -- internal/server/daemon.go internal/server/handlers.go internal/server/daemon_test.go internal/server/handlers_test.go`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Planned at**: working copy `7b926628` (parent `f33fe93e`), 2026-07-29

## Why this matters

The upload contract the server validates against says a skill tree may
expand to 128 MiB (`safetree.PrototypeLimits`), and the download side is
sized for that world (`agentskill.TreeArchiveMaxBytes` ≈ 132.5 MiB). But a
hardcoded 32 MiB `MaxBytesReader` on every request means any upload whose
multipart body exceeds ~32 MiB dies mid-stream with a generic
"Upload is too large" 413 — while every limit message the user can reason
about says the ceiling is 134217728 bytes. The advertised contract is
unreachable, and curators get a number four times too large to size
against. The two constants live in different files with nothing asserting
their relationship.

## Current state

- `internal/server/daemon.go:34` — `maxRequestBodyBytes = int64(32 << 20)`.
- `internal/server/daemon.go:415-424` — `newHTTPServer` wraps every
  request: `request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)`.
- `internal/safetree/safetree.go:30-38` — `PrototypeLimits()` returns
  `MaxFiles: 2048`, `MaxFileBytes: 16 << 20`, `MaxExpandedBytes: 128 << 20`.
- `internal/server/daemon.go:559-566` and `:598-605` (production and dev
  construction) — `newHandler(catalog, ..., handlerOptions{StagingParent: ...,
  Limits: safetree.PrototypeLimits(), ...})`. The limits the upload path
  enforces flow through `handlerOptions`; the body cap does not.
- `internal/server/handlers.go` — `handleError` maps
  `*http.MaxBytesError` to the 413 "Upload is too large" rendering (search
  `MaxBytesError`); that error-class mapping is correct and stays.
- The multipart wire format per upload: one `namespace` text part (≤1024
  bytes, `handlers.go` `nextTextPart(body, "namespace", 1024)`), one
  manifest part, then one file part per manifest entry
  (`internal/server/upload.go:100-135`), each with MIME framing overhead
  (boundary + headers, comfortably < 1 KiB per part).

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Focused tests | `go test ./internal/server/ -race -count=1` | `ok`, exit 0 |
| Full suite | `just test` | all packages `ok` |
| Static analysis | `just vet` | exit 0 |

## Scope

**In scope**:
- `internal/server/daemon.go`
- `internal/server/handlers.go` (only if the derivation naturally lives beside `newHandler`'s existing limit validation)
- `internal/server/daemon_test.go`
- `internal/server/handlers_test.go`

**Out of scope**:
- `internal/safetree/` — the tree limits are the source of truth and do not change.
- `internal/agentskill/archive.go` — `TreeArchiveMaxBytes` already derives its envelope from tree limits; it is the exemplar, not a target.
- `cmd/ts-skills/` — the client already sizes itself from the same shared limits.

## Steps

### Step 1: Replace the hardcoded cap with a derivation from `safetree.Limits`

What must be true after this step: the byte limit handed to
`http.MaxBytesReader` is computed from the same `safetree.Limits` value
that the handler enforces, not from an unrelated constant. The derivation
must cover a maximal legal upload: `MaxExpandedBytes` of file content,
plus the manifest part, plus per-part multipart framing for up to
`MaxFiles` files, plus the namespace part — with the formula written down
in one place with a comment stating what each term pays for. The exemplar
for this style of derived envelope is
`agentskill.TreeArchiveCeiling`/`TreeArchiveMaxBytes`
(`internal/agentskill/archive.go`), which derives the download ceiling
from tree limits including overflow protection — match that shape,
including guarding against overflow.

Plumbing: `newHTTPServer` currently takes no limits. Thread the derived
cap from the daemon construction sites (which already hold
`safetree.PrototypeLimits()`) into `newHTTPServer`. Both the production
and dev paths must pass it.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

### Step 2: Assert the relationship at construction

`newHandler` already validates its options (see its existing checks near
`internal/server/handlers.go:68-100`). Add a construction-time guard so a
future limits change cannot silently reintroduce the mismatch: building a
server whose body cap is smaller than the derived minimum for its limits
must fail with an error naming both numbers.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

## Test plan

In `internal/server/daemon_test.go` / `handlers_test.go` (pattern: the
existing `newWebFixture` fixture and any existing `newHTTPServer` tests):

1. Unit-test the derivation: for `PrototypeLimits()`, the cap is ≥
   `MaxExpandedBytes` plus the framing allowance, and overflow-safe for
   absurd limits (mirror the overflow test style used for
   `TreeArchiveCeiling` in `internal/agentskill/archive_test.go`).
2. Construction guard: a handler/server built with an undersized cap for
   its limits fails with the naming error.
3. End-to-end at small scale: build the serving stack with custom small
   `safetree.Limits` (e.g. `MaxExpandedBytes` of a few KiB) and confirm
   (a) an upload just *under* the tree limit is accepted — proving the
   body cap is no longer the binding constraint — and (b) an upload over
   the tree limit is rejected by the safetree limit error, not by a 413.
   Do not write a 128 MiB test body.

- **Verify**: `just test` → all pass, including the new tests.

## Done criteria

- [ ] `rg -n "32 << 20" internal/server/daemon.go` → no matches
- [ ] `just test` → all packages `ok`
- [ ] `just vet` → exit 0
- [ ] No files outside the in-scope list are modified (`jj st`)

## STOP conditions

Stop if:

- The code at the "Current state" locations doesn't match the excerpts.
- You discover a deliberate reason for the 32 MiB cap (e.g. a comment or
  test asserting tsnet/proxy constraints) — that would make this a design
  decision, not a drift bug.
- The derivation needs a multipart-overhead number you cannot justify
  from `internal/server/upload.go`'s actual parsing.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
questions. Descriptive, not prescriptive.

## Maintenance notes

After this lands, `safetree.Limits` is the single knob: raising
`MaxExpandedBytes` automatically raises the body cap and (already) the
download ceiling. A reviewer should scrutinize the framing-allowance term
— too tight and large-file-count uploads fail at the transport layer with
the generic 413 again; too loose costs nothing real (the safetree limits
still bound actual staged bytes).
