# Plan 006: Export sweep, docs, metrics, final baseline comparison

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/consolidation/README.md.
>
> **Drift check (run first)**:
> `jj log -r '2037ced944acc38456c090ff62e74c9de099318d::@' --no-graph`
> Confirm plans 001–005 all landed (one commit each per repo rule) and
> the README status rows say DONE. If any is not DONE, stop.

## Status

- **Effort**: M
- **Risk**: LOW
- **Depends on**: 002, 003, 004, 005
- **Planned at**: revision `2037ced944acc38456c090ff62e74c9de099318d`, 2026-07-28

## Why this matters

Merges leave residue: exports nothing imports, docs that describe twelve
packages, helper duplication across moved test files, and a baseline that
was only re-checked piecewise. This plan closes the effort with evidence:
a final live run diffed against 001's transcript and a before/after
metrics table, so the claim "same behavior, half the structure" is a
recorded fact rather than an assertion — and so the eval notes pinned to
`2037ced9…` have their measured endpoint.

## Steps

### 1. Dead-surface sweep

- `go vet ./...`; run `golang.org/x/tools/cmd/deadcode ./...` if
  available (`go run` it; do not add a dependency) and delete what it
  proves dead. No `_ = x` appeasements, no re-exports for ghosts.
- `rg '^func [A-Z]|^type [A-Z]|^var [A-Z]|^const [A-Z]' internal/server/`
  — audit every exported identifier: reachable from `cmd/ts-skillsd` or
  a test, or unexported now.
- Same audit for `internal/agentskill` (consumers: both binaries,
  server) and `internal/safetree`.
- `go mod tidy`; confirm no dependency became unused (`git diff go.mod`
  reviewed line by line — removals only).

### 2. Docs

Update to describe the landed shape (human, quickstart-first — repo docs
policy; no defensive scaffolding):

- `AGENTS.md` — package layout section, and add the organizing rule from
  the consolidation README (package = both binaries import it;
  constructor = parses untrusted input; interface = two implementations).
- `README.md` — any package-map or architecture prose.
- `docs/development.md` — dev-mode instructions still accurate
  (verified live in step 4), file references fixed.
- `docs/deployment.md` — unchanged behavior expected; fix file references
  only.
- `docs/SPEC.md` and `docs/research/` — update only if they name Go
  packages; the wire/disk contracts did not change.
- `CHANGELOG.md` — one entry for the effort: install crash contract
  change (re-run convergence, `ErrRecovered` diagnostic removed) is the
  only user-visible note; the rest is internal.

### 3. Metrics (record in the reconciliation entry)

Produce the before/after table; "before" numbers are pinned in the
consolidation README and the inbox note:

| Metric | Before (`2037ced9`) | After |
|---|---|---|
| internal packages | 12 | (count) |
| production lines (non-test .go) | ~7,700 | (count) |
| test lines | ~8,400 | (count) |
| project-defined interfaces | 6 | 0 |
| `install` production lines | ~2,600 | (count) |

Commands: `find . -name '*.go' -not -path './.git/*' -not -name '*_test.go' | xargs wc -l`
and the `_test` complement; `rg -c 'type\s+\w+\s+interface' --glob '!*_test.go'`.

### 4. Final live comparison

Re-run plan 001's full procedure — dev mode **and** live tailnet — with
the same hygiene rules (scratch state under /tmp, ephemeral ports, kill
by exact pid, verify 8080 free). Write
`plans/consolidation/baseline/transcript-after.md` and diff against
`transcript.md`:

- publication digest, tree.zip hash, installed tree, lock-file hash:
  **identical**;
- failure diagnostics and help text: identical except the sanctioned
  `ErrRecovered` removal (002);
- capability gate (403/200) and TLS details: identical;
- if 001 was completed dev-only (its STOP carve-out), the tailnet section
  runs here for the first time and is recorded as *new* coverage, not a
  diff — and the effort's reconciliation entry must say so.

### 5. Close out

Reconciliation entry in the consolidation README: metrics table, diff
result, deviations across the whole effort, and a pointer for the eval
notes (`~/notes/inbox/ts-skills over-decomposition — the tsidp
comparison.md` gets its "after" numbers from here — updating that note is
the owner's call, not this executor's). Mark the effort complete.

## Verification

- `just check` green; `just build` green.
- `go run golang.org/x/tools/cmd/deadcode@latest ./...` (if run) output
  recorded, empty or fully triaged.
- `transcript-after.md` exists with the full diff summary.
- `find internal -maxdepth 1 -type d | sort` →
  `agentskill  safetree  server  version`.
- `ss -tlnp | grep :8080` → empty; no state written under `$HOME`.

## STOP conditions

- The final comparison finds *any* unsanctioned behavioral diff — stop.
  Do not patch behavior inside a sweep plan; hand back with the diff and
  which plan (002–005) introduced it.
- A doc update requires describing behavior nobody verified (e.g.
  deployment steps that were never re-run) — verify or mark it clearly,
  never guess in docs.
- Deadcode flags something a *pending* product feature needs: deletion
  wins (repo rule: no speculative surface); note it in the handback so
  the feature re-adds it with a consumer.

## Maintenance notes

This closes the consolidation effort. The organizing rule lives on in
AGENTS.md; the transcripts stay as point-in-time artifacts. Any future
"should we split this file/package?" discussion starts from the rule and
the comparators (tsidp, golink, tclip), not from taste.
