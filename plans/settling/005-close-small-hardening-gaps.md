# Plan 005: Close four small hardening gaps — review-page gating, dev-mode state guard, response headers, client message hygiene

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> `plans/settling/README.md`.
>
> **Drift check (run first)**:
> `jj diff --from 7b926628 -- internal/server/handlers.go internal/server/daemon.go internal/server/handlers_test.go internal/server/daemon_test.go cmd/ts-skills/client.go cmd/ts-skills/client_test.go`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition. If plan 003 has landed,
> `handlers.go` line numbers will have shifted — match on symbols.

## Status

- **Effort**: M (four independent S-sized fixes)
- **Risk**: LOW
- **Depends on**: none (sequence after 003 to avoid `handlers.go` churn)
- **Planned at**: working copy `7b926628` (parent `f33fe93e`), 2026-07-29

## Why this matters

Four small, confirmed gaps, none individually urgent, all cheap and in
the same trust-boundary neighborhood:

1. `GET /candidates/{candidate}` renders unpublished pre-review content
   plus the submitter's Tailscale login name to **any** tailnet peer —
   the only candidate route that never resolves a curator. Candidate IDs
   are 128-bit CSPRNG values, so this is a capability-URL, not a
   brute-forceable hole; the exposure is a shared/leaked link and the
   inconsistency itself.
2. Dev mode refuses authkey env vars but never checks whether the state
   directory it is pointed at already holds an **enrolled tsnet node**.
   `TS_SKILLSD_DEV=1 TS_SKILLSD_STATE_DIR=<prod dir>` (daemon stopped —
   the flock otherwise blocks it) grants loopback callers full curation
   as `dev@localhost` over the real catalog, and publications are
   permanent.
3. No response sets `X-Content-Type-Options`, `Content-Security-Policy`,
   or `Referrer-Policy`. `html/template` escaping is currently correct
   everywhere (audited), so this is pure defense in depth against a
   future escaping mistake — at essentially zero cost.
4. The client interpolates the server-sent `message` string into terminal
   output unvalidated. A compromised or spoofed registry can emit
   ANSI/OSC escapes through the CLI's error path. Every *other* field the
   client decodes must round-trip through a validating parser; the
   message is the one exception.

## Current state

- `internal/server/handlers.go:104-112` — route table; `GET
  /candidates/{candidate}` → `reviewCandidate`.
- `internal/server/handlers.go:315-330` — `reviewCandidate` goes straight
  to `ParseCandidateID` → `h.catalog.candidate` → `openTree`; no curator.
  The gate exemplar is `createCandidate` at `:241-245`:

  ```go
  curator, ok := h.resolveCurator(w, r)
  if !ok {
      return
  }
  ```

  `publishCandidate` (:373) and `setCurrent` (:385) do the same.
  `resolveCurator` (:419) renders 401/403 itself and returns `ok=false`.
- `internal/server/daemon.go:64-69` — `devConfigFromEnv` rejects
  `TS_SKILLSD_AUTHKEY_FILE` / `TS_AUTHKEY`; no state-dir check.
  `normalizeDevConfig` (:81-103) is pure config validation (no
  filesystem). The production path already has the probe this needs:
  `tsnetStateHasNodeKey(filepath.Join(config.StateDir, "tsnet",
  "tailscaled.state"))` at `daemon.go:144`, defined at `:172`.
- `internal/server/handlers.go:829-836` — `writePage` sets only
  `Content-Type`. The ZIP response sets its own headers at `:499-503`.
  Templates: one external script (`templates/upload.html:32`,
  `/static/upload.js`, `defer`), no inline scripts, no inline `style=`
  attributes (grep-verified at planning time). Tailwind output is the
  committed `static/style.css`.
- `cmd/ts-skills/client.go:370-384` — `responseError`:

  ```go
  case codeInvalidRequest:
      return fmt.Errorf("%w: %s", errInvalidRequest, wire.Message)
  ...
  case codeInternal:
      return fmt.Errorf("%w: %s", errInternal, wire.Message)
  ```

  and `cmd/ts-skills/main.go` prints the error chain to stderr. The
  honest server only sends fixed strings
  (`internal/server/handlers.go:618-623`), but the client doesn't require
  that. Validation exemplar in the same package: the canonical
  round-trip checks at `client.go:147-166`.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Server tests | `go test ./internal/server/ -race -count=1` | `ok`, exit 0 |
| Client tests | `go test ./cmd/ts-skills/ -race -count=1` | `ok`, exit 0 |
| Full suite | `just test` | all packages `ok` |
| Static analysis | `just vet` | exit 0 |

## Scope

**In scope**:
- `internal/server/handlers.go`
- `internal/server/daemon.go`
- `internal/server/handlers_test.go`, `internal/server/daemon_test.go`
- `cmd/ts-skills/client.go`, `cmd/ts-skills/client_test.go`

**Out of scope**:
- `internal/server/tailnet.go` — identity resolution is correct; don't touch.
- `internal/server/templates/` — no inline scripts exist, so the CSP needs no template changes; if you find yourself editing templates, stop.
- `cmd/ts-skills/cli.go` — `commandError` wording is covered by plan 006's tests; unchanged here.

## Steps

### Step 1: Gate `reviewCandidate` on the curate capability

Add the `resolveCurator` gate (exemplar `:241-245`) at the top of
`reviewCandidate`. The post-upload redirect that links to this page
already runs as a curator, so no flow breaks. Update
`TestNonCuratingIdentityCanReadButCannotMutate`
(`handlers_test.go:388`) — a non-curating identity now gets 403 on the
review page; the catalog and skill pages stay readable.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

### Step 2: Refuse dev mode on a state directory with an enrolled node

Where dev startup resolves its state directory (the dev construction
path around `daemon.go:598-605` — after the directory is known, before
the catalog opens), probe
`tsnetStateHasNodeKey(filepath.Join(stateDir, "tsnet", "tailscaled.state"))`
and fail with an error stating that dev mode will not open an enrolled
registry's state directory (name the env vars involved). A probe *error*
other than not-exist fails startup too — matching how the production
path treats it (`daemon.go:144-150`; read it and mirror). Keep
`normalizeDevConfig` pure — the filesystem check does not belong in
config validation.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

### Step 3: Set defensive headers on HTML and ZIP responses

In `writePage`: `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, and
`Content-Security-Policy: default-src 'self'`. In the ZIP response
headers block (`:499-503`): `X-Content-Type-Options: nosniff` and
`Content-Disposition` stays as is. Before finalizing the CSP, check the
committed `internal/server/static/style.css` for `url(data:` references
(Tailwind sometimes inlines SVG); if present, extend with
`img-src 'self' data:` — otherwise keep the single directive.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`, and the
upload page still loads its script in the dev-mode smoke check below.

### Step 4: Stop trusting `wire.Message` in the client

In `responseError`, pass the server message through a validator before
interpolation: valid UTF-8, no control characters (the standard here is
the server's own `validateRecordText` rule — see its use at
`internal/server/handlers.go` upload path), and a short cap (~200 bytes).
On failure, fall back to the client's own per-code wording (it already
has one for every other code). Same treatment for both
`codeInvalidRequest` and `codeInternal` branches.

**Verify**: `go test ./cmd/ts-skills/ -race -count=1` → `ok`.

## Test plan

1. Review-page gating (`handlers_test.go`, pattern:
   `TestNonCuratingIdentityCanReadButCannotMutate:388`): non-curator GET
   on a real candidate URL → 403; curator GET → 200 (the existing
   curation-flow test `TestCurationRoutesEscapeReviewPublishAndChangeCurrent:335`
   likely already covers the 200 side — extend, don't duplicate).
2. Dev-mode guard (`daemon_test.go`, pattern: existing
   `devConfigFromEnv`/`RunDev` validation tests): a dev config pointed at
   a state dir containing a `tsnet/tailscaled.state` with a node key
   (fixture: reuse whatever the `daemon.go:144` production tests use)
   fails to start with the refusal message; a clean dir still starts.
3. Headers (`handlers_test.go`): any rendered page response carries all
   three headers; the ZIP response carries `nosniff`.
4. Client message hygiene (`client_test.go`, pattern: the existing
   `responseError` decode tests around `client_test.go:204`): a response
   whose `message` contains `\x1b]0;` escape bytes produces an error
   string free of control characters; an over-long message is dropped for
   the fallback wording; a normal short message passes through.

Dev-mode smoke check (manual, once): `TS_SKILLSD_DEV=1 go run ./cmd/ts-skillsd`
→ open `http://127.0.0.1:8080/upload` → page renders with styles and the
staging flow works (proves the CSP didn't break the script/CSS).

- **Verify**: `just test` → all pass, including the new tests.

## Done criteria

- [ ] `just test` → all packages `ok`
- [ ] `just vet` → exit 0
- [ ] `rg -n "resolveCurator" internal/server/handlers.go` shows four call sites (create, review, publish, setCurrent)
- [ ] `rg -n "wire.Message" cmd/ts-skills/client.go` shows no unvalidated interpolation
- [ ] No files outside the in-scope list are modified (`jj st`)

## STOP conditions

Stop if:

- The code at the "Current state" locations doesn't match (symbols, not
  line numbers, after plan 003).
- The CSP breaks page rendering in the smoke check and the fix requires
  template changes — templates are out of scope; hand back.
- Gating the review page breaks a flow other than the two named tests —
  there may be a consumer of that URL the audit missed.
- `tsnetStateHasNodeKey`'s signature or semantics changed since planning.

On stopping, write a **handback**: current state, desired outcome,
questions. Descriptive, not prescriptive.

## Maintenance notes

The review page is now curator-only; if a future "share with a
non-curator reviewer" flow is wanted, that is a product decision (signed
links, a viewer capability) — not a reason to drop the gate. The CSP is
deliberately minimal; anyone adding inline scripts or third-party assets
to templates will hit it and should extend it consciously. The client-side
message validator is the precedent for any future server-sent free text.
