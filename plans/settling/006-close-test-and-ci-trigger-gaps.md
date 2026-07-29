# Plan 006: Cover the untested error surfaces and widen CI's path triggers

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> `plans/settling/README.md`.
>
> **Drift check (run first)**:
> `jj diff --from 7b926628 -- cmd/ts-skills/cli_test.go internal/server/handlers_test.go .github/workflows/test.yml .github/workflows/lint.yml .github/workflows/security.yml`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition. Plans 001–005 may have shifted
> line numbers in the production files this plan *reads* — match on
> symbols; this plan modifies tests and workflows only.

## Status

- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (run last so new tests assert the landed behavior of 001–005)
- **Planned at**: working copy `7b926628` (parent `f33fe93e`), 2026-07-29

## Why this matters

Coverage profiling during the audit found the untested code concentrated
exactly where regressions hurt users most:

- `commandError` (`cmd/ts-skills/cli.go:184-208`) maps nine sentinel
  errors to the messages users see when install/restore fails; only two
  branches execute under test. A reordered switch or changed `errors.Is`
  chain silently degrades busy/conflict/tamper/not-found reporting to
  "install failed".
- `setCurrent` (`internal/server/handlers.go:384+`) — the single
  highest-blast-radius mutation (repoints what every future install
  gets) — has zero coverage on all six validation branches.
- `writeAPIDomainError` (`internal/server/handlers.go:600-609`) — the
  only source of truth for the wire error codes the CLI decodes — has
  never produced `codeNotFound` or `codeTooLarge` under test through the
  real handler stack; the client decodes those codes only against a
  hand-rolled fake.
- CI's three gating workflows all trigger on `**/*.go` (+ own file +
  go.mod/go.sum) only. Templates and static assets are `//go:embed`ed
  into the server (`internal/server/handlers.go:30-33`) and parsed at
  construction — a template-only PR that breaks parsing merges without
  any workflow running.

## Current state

- `cmd/ts-skills/cli_test.go:163-183` —
  `TestCommandErrorMapsRegistryFailureClasses`: a `map[string]struct{err
  error; want string}` table with exactly two rows (`errInvalidRequest`,
  `errInternal`), wrapping each error and asserting substring +
  `errors.Is`. This is the pattern to extend.
- `cmd/ts-skills/cli.go:184-208` — the full switch: `errBusy`,
  `errLocalChanges`, `errProjectChanged`, `errIdentityMismatch`,
  `errDigestMismatch`, `errNotFound`, `safetree.ErrLimitExceeded`,
  `errProtocol`, `errInvalidRequest`, `errInternal`, default.
- `internal/server/handlers_test.go:245` — `postForm(t, fixture, path,
  values)` helper; `:52` — `newWebFixture(t)`. Existing `/current` calls
  post only valid forms (`TestCurationRoutesEscapeReviewPublishAndChangeCurrent:335`,
  `TestNonCuratingIdentityCanReadButCannotMutate:388`).
- `internal/server/handlers.go` — `setCurrent` branches: `ParseForm`
  failure, wrong field count, invalid skill string, invalid digest
  string, `NewPublicationID` failure, catalog `setCurrent` failure.
- `internal/server/handlers.go:600-609` — `writeAPIDomainError`:
  `errNotFound → codeNotFound`, `safetree.ErrLimitExceeded →
  codeTooLarge`, default → log + `codeInternal`. The archive size limit
  that trips `ErrLimitExceeded` comes from `h.maxArchiveBytes`, derived
  in `newHandler` from `options.Limits` — so a test fixture with tiny
  custom `safetree.Limits` can trip it with a small tree.
- `.github/workflows/test.yml:4-16` (and the same shape in `lint.yml`,
  `security.yml`): `paths:` lists `"**/*.go"`, the workflow file,
  `go.mod`, `go.sum` — for both `push` and `pull_request`.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Client tests | `go test ./cmd/ts-skills/ -race -count=1` | `ok`, exit 0 |
| Server tests | `go test ./internal/server/ -race -count=1` | `ok`, exit 0 |
| Full suite | `just test` | all packages `ok` |
| Workflow lint | `mise exec -- zizmor .github/workflows/` | no new findings vs. before your edit |

## Scope

**In scope** (tests and workflows only — no production code):
- `cmd/ts-skills/cli_test.go`
- `internal/server/handlers_test.go`
- `.github/workflows/test.yml`, `.github/workflows/lint.yml`, `.github/workflows/security.yml`

**Out of scope**:
- Any production `.go` file. If a new test reveals a real bug, that is a
  STOP condition (report it), not a license to fix it here.
- `.github/workflows/release.yml`, `workflows.yml` — release triggers are
  tag-driven; not part of this gap.

## Steps

### Step 1: Extend the `commandError` table to every branch

Add rows to `TestCommandErrorMapsRegistryFailureClasses` for the seven
uncovered sentinels plus one non-matching error for the default branch.
Assert the user-facing wording fragment and `errors.Is` for each, exactly
as the existing rows do. Take each expected wording from the switch in
`cli.go` — the test pins today's wording on purpose.

**Verify**: `go test ./cmd/ts-skills/ -race -count=1 -run TestCommandError` → `ok`.

### Step 2: Exercise `setCurrent`'s validation branches

New test in `handlers_test.go` (fixture: `newWebFixture` + `postForm`)
posting to `/current` as a curator with: a missing `skill` field, a
missing `digest` field, duplicate fields (wrong count), a malformed skill
string, a malformed digest string, and a well-formed publication that was
never published. Each case asserts the response status and — for the
never-published case — that the current selection is unchanged
(read back via the API `current` route or the catalog page).

**Verify**: `go test ./internal/server/ -race -count=1 -run TestSetCurrent` → `ok` (adjust `-run` to your test name).

### Step 3: Drive the real handler to produce `codeNotFound` and `codeTooLarge`

Two handler-level tests through `newWebFixture`'s real stack:
`GET /api/v1/skills/<ns>/<name>/current` for a skill with no current
publication → HTTP status and JSON `code` for not-found;
`GET .../publications/<digest>/tree.zip` against a fixture built with
small custom `safetree.Limits` such that the real archive exceeds
`maxArchiveBytes` → the too-large code. Assert both the status and the
`code` field the CLI switches on.

**Verify**: `go test ./internal/server/ -race -count=1` → `ok`.

### Step 4: Add template/static paths to the three gating workflows

In `test.yml`, `lint.yml`, `security.yml`, add
`internal/server/templates/**` and `internal/server/static/**` to the
`paths:` list under **both** `push` and `pull_request`. Preserve YAML
style (quoting, ordering) of each file.

**Verify**: `mise exec -- zizmor .github/workflows/` → no new findings;
`git diff --stat` (or `jj diff --stat`) shows only the three workflow
files among workflows.

## Test plan

Steps 1–3 *are* the tests. Structural patterns named per step. Final
gate:

- **Verify**: `just test` → all pass, including the new tests.

## Done criteria

- [ ] `just test` → all packages `ok`
- [ ] Every `case` in `commandError` appears in the test table (manual check against `cli.go`)
- [ ] All three workflows list the templates and static paths in both triggers
- [ ] No files outside the in-scope list are modified (`jj st`)

## STOP conditions

Stop if:

- A new test **fails against current production code** — you may have
  found a real bug; report it in a handback rather than adapting the test
  to the buggy behavior or fixing production code.
- The `setCurrent` handler's branch structure differs from the six cases
  listed (plans 001–005 shouldn't touch it, but verify).
- `newWebFixture` cannot take custom `safetree.Limits` — the fixture may
  need a seam; that's a production change, hand back.

On stopping, write a **handback**: current state, desired outcome,
questions. Descriptive, not prescriptive.

## Maintenance notes

The `commandError` table now pins user-facing wording — future wording
changes must update the table, which is the point (wording is contract).
If a workflow is ever added that gates merges, it needs the same
template/static paths; consider whether the `paths:` lists should be
replaced wholesale with a paths-ignore approach if they grow again.
