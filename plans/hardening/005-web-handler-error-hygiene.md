# Plan 005: Web handlers — buffer templates before status, log unexpected errors

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/web/ internal/daemon/`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: S
- **Risk**: LOW
- **Depends on**: 004-join-cleanup-errors.md (same file; serial execution avoids conflict)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

`render` commits the HTTP status before template execution, then swallows
`ExecuteTemplate` errors — a template failure leaves a half-written page with
a 200/4xx status and zero diagnostics. And the `handleError` default branch
turns unexpected catalog/filesystem/identity errors into a generic 500 page
and drops the cause entirely: operators get no signal when the registry is
actually broken. The daemon is the operator-facing surface of this project;
silence here is how small bugs become long outages.

## Current state

`internal/web/web.go:770-775`:

```go
func (h *handler) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.pages.ExecuteTemplate(w, name, data); err != nil {
		return
	}
}
```

`internal/web/web.go:753-766` — `handleError`'s default branch renders a
friendly 500 and the error is never recorded:

```go
default:
	h.renderError(w, http.StatusInternalServerError, "Request could not be completed", "Try again. If this keeps happening, contact the registry operator.")
}
```

The handler carries its config on `h.options` (used at web.go:284 for
`StagingParent` and `Limits`). The daemon constructs the web handler — find the
construction site via `rg "web\.New|options{" internal/daemon/` when wiring
the logger through.

Plan 004 will have left `// TODO(plan 005): log` comments at post-commit
cleanup discard points in this file; this plan resolves them.

Idioms involved: `errors-are-values.md`, `pair-acquisition-with-cleanup.md`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (package) | `go test -race ./internal/web/ -count=1` | all pass |
| Templates | `just css` | regenerates `internal/web/static/style.css` — run only if you touched templates or `internal/web/tailwind.css`; you should not need to |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/web/web.go` + `internal/web/web_test.go`
- `internal/daemon/daemon.go` (logger wiring at the web construction site only)

**Out of scope**:
- Templates under `internal/web/templates/` — no content changes; this plan
  changes *how* they're executed, not what they say. If a template bug
  surfaces during testing, fix only what's needed to keep the test honest.
- `handleError`'s status-code mapping — plan 003's classes must keep their
  current codes.
- Logging anywhere else (daemon lifecycle logging is a separate concern).

## Steps

### Step 1: Buffer templates, then commit

Change `render` to execute into a `bytes.Buffer` first; set headers and
status only after template execution succeeds:

```go
var page bytes.Buffer
if err := h.pages.ExecuteTemplate(&page, name, data); err != nil { /* 500 fallback */ }
w.Header().Set("Content-Type", "text/html; charset=utf-8")
w.WriteHeader(status)
if _, err := w.Write(page.Bytes()); err != nil { /* post-commit: log only */ }
```

On template failure: log the error, write the plain-error fallback
(`renderError`'s 500 shape executed the same buffered way — execute the
error template into its own buffer so a *failed* error template degrades to
`http.Error(w, ...)` plaintext, never a second `WriteHeader`).

**Verify**: `go test -race ./internal/web/ -count=1` → all pass.

### Step 2: One logger for the web layer

Add `Logger *slog.Logger` to the handler options struct; default to
`slog.Default()` when nil (make-the-zero-value-useful). Wire
`slog.Default()` (or the daemon's logger if one exists — check) at the
construction site in `internal/daemon/daemon.go`. No new config knobs.

**Verify**: `go build ./...` → exit 0.

### Step 3: Log the unexpected, keep responses generic

- `handleError` default branch: `h.options.Logger.Error("web request failed", "error", err)` (plus request context fields that cost nothing: method, path).
- Post-commit write failure from Step 1: log it.
- Resolve every `// TODO(plan 005): log` left by plan 004: post-commit cleanup failures get logged at `Warn` (the client already got a success response; this is operator hygiene).

**Verify**: `go test -race ./internal/web/ -count=1` → all pass.

### Step 4: Full gate

**Verify**: `just check` → exit 0.

## Test plan

- `internal/web/web_test.go`: inject a template set whose named template
  deliberately fails (or a closed/broken `*template.Template` seam matching
  how `h.pages` is constructed): assert the response is the 500 fallback,
  not a partial page, and only one status was written (a recording
  `httptest.ResponseRecorder` asserts final code/body; asserting single
  WriteHeader needs no extra seams).
- Logging: build the handler with a `slog.New(slog.NewTextHandler(io.Discard, nil))`
  default-shaped in-memory handler or `slogtest`-style capture; assert the
  default-branch error is recorded. Keep this light — one capture handler
  helper in the test file.

**Verify**: `go test -race ./internal/web/ -count=1` → all pass, including
new tests.

## Done criteria

- [ ] `rg "WriteHeader" internal/web/web.go` → every call happens only after successful template execution
- [ ] No `TODO(plan 005)` comments remain
- [ ] Unexpected errors are logged, client responses stay generic
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The "Current state" excerpts don't match (esp. if plan 004 changed the
  function shapes this plan quotes — reconcile line numbers, but a changed
  *design* is a handback).
- The handler options struct turns out to be constructed in more places
  than `internal/daemon/` and tests (logging must default sensibly for all
  of them).
- A template executes user-controlled filenames into the template *name*
  (not data) — that would be a different bug class; flag it.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- New render paths must go through `render`/`renderError`; a bare
  `w.WriteHeader` + `ExecuteTemplate` pair is now a review rejection.
- The logger intentionally does not set request-scoped attributes beyond
  the cheap ones; structured request logging as middleware is out of scope
  and should be its own decision.
