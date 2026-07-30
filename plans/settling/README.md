# Settling: post-consolidation audit fixes

Fixes planned from a full nine-category audit (correctness, security,
performance, tests, debt, deps, DX, docs, direction) run 2026-07-29
against working copy `7b926628` (parent `f33fe93e`), after the
consolidation effort (`plans/consolidation/`) and its four follow-up fix
commits. The audit's verification baseline was green: `go vet` clean,
full `-race` suite passing, `golangci-lint` zero issues, docs verified
current. Findings cluster in two tracks: **server resource lifecycle**
(001–003 — the daemon can be made to spend unbounded disk/CPU/memory by
any tailnet peer, and strands temp files on crashes) and **client
convergence** (004 — false conflicts and recovery holes in the
stage-and-rename installer), with a hardening batch (005) and a
test/CI batch (006) closing out.

Execute in the order below unless dependencies say otherwise. Each
executor: read the plan fully before starting, honor its STOP conditions,
and update your row when done.

## Execution order & status

(Recommended execution order — may diverge from numeric order; numbers
are never reused or renumbered.)

| Plan | Title | Effort | Depends on | Status |
|------|-------|--------|------------|--------|
| [001](001-sweep-server-tmp-at-startup.md) | Sweep daemon tmp at startup; close orphaned-archive window | S | — | DONE |
| [002](002-derive-body-cap-from-tree-limits.md) | Derive HTTP body cap from tree limits | S | — | DONE |
| [004](004-installer-concurrency-and-convergence.md) | Concurrent installs succeed; trash recovery converges | M | — | DONE |
| [003](003-bound-read-route-cost.md) | Bound read-route cost: verify once, cap concurrency, cap previews | M | 001 | DONE — concurrent first opens may both verify |
| [005](005-close-small-hardening-gaps.md) | Review-page gating, dev state guard, headers, client message hygiene | M | — (after 003) | DONE |
| [006](006-close-test-and-ci-trigger-gaps.md) | Cover untested error surfaces; widen CI path triggers | S | — (run last) | TODO |

Status values: TODO | IN PROGRESS | DONE | BLOCKED (one-line reason) |
SUPERSEDED (one-line pointer to what replaced it)

## Dependency notes

- **001 → 003**: both restructure `publicationTree` in
  `internal/server/handlers.go`; 001's cleanup-ordering fix is the base
  003 edits on.
- **003 before 005** (soft): both edit `handlers.go`; 005 matches on
  symbols so it survives either order, but this order avoids churn.
- **006 last** (soft): its tests pin the landed behavior (e.g. 005
  changes review-page authorization in the same test file).
- 002 and 004 are independent of everything.

## Reconciliation log

Newest first. A few lines per entry — date, what happened, link,
deviations, next executable plan.

- **2026-07-29**: Completed 004 — removed the install-only stale lock
  snapshot check, made pending-trash recovery converge or report retained
  data, and routed the last replacement failure through rollback. Next:
  005.
- **2026-07-29**: Completed 005 — restricted candidate reviews to
  curators, rejected dev startup on enrolled state, added defensive
  response headers, and sanitized registry error messages. Next: 006.
- **2026-07-29**: Completed 003 — cached verified tree digests per
  process (concurrent first opens may both verify), bounded concurrent
  preview/ZIP work, and limited rendered previews to 256 KiB. Next: 004.
- **2026-07-29**: Completed 002 — derived the multipart request cap from
  `safetree.Limits`, added overflow and construction guards, and covered
  near-limit uploads. Next: 004 (or 003, after its 001 dependency).
- **2026-07-29**: Effort filed — six plans from the audit; no code
  changes. Baseline at planning: full `-race` suite green on working copy
  `7b926628`. Next: 001.

## Considered and rejected

(So nobody re-plans these.)

- **Drift-guard test for the duplicated client/server wire constants**
  (`cmd/ts-skills/protocol.go` vs `internal/server/protocol.go`): the
  duplication is the consolidation's deliberate "each binary owns its
  wire vocabulary" rule, and `cmd/ts-skills/e2e_test.go` exercises the
  real round trip; a literal-equality test adds coupling the house style
  rejected.
- **Migrating off `gorilla/csrf`** (toolkit marked discontinued): v1.7.3
  is current, correctly wired (Origin allowlist, strict Referer in TLS
  mode, HttpOnly/SameSite cookies), no known vulnerability. Monitor;
  migrate to a maintained fork only on a real advisory.
- **Caching/eviction design for the verified-digest cache beyond a
  process-lifetime set** (plan 003): digest set is small and append-only
  per process; anything fancier is speculative.
- **Docs sweep**: audited stale-docs surface post-consolidation — README
  quickstart flags, `docs/*.md` package references, justfile targets,
  Dockerfile env all verified current. Nothing to fix.

## Deferred

(Real, vetted findings — not planned this run, one line each.)

- **Windows-reserved device names are valid skill names**
  (`internal/agentskill/name.go:49-76` accepts `con`, `nul`, `com1`;
  one such lock entry aborts `restore` for the whole project on Windows
  via `cmd/ts-skills/installer.go:155-158` + `windows.go`): the fix is a
  *narrowing* contract change to `ParseName` (already-published names
  would become unparseable) and wants an owner decision first — same
  sign-off precedent as hardening plan 007. The softer half
  (restore degrading per-skill instead of aborting the project) can ride
  any future installer plan.
- **Client HTTP timeout is a single 2-minute whole-exchange budget**
  (`cmd/ts-skills/cli.go:176`, enforced by `client.go:75-77`): installs
  need sustained >1.1 MB/s for a max-size skill or fail with an unmapped
  `context.DeadlineExceeded` → generic "install failed". Fix is a
  timeout-policy decision (header timeout + per-op deadlines) — owner
  call on the policy, then an S plan.
- **Direction options surfaced by the audit** (maintainer's call, all
  grounded, none planned): a `ts-skills list` verb + one JSON route
  reusing `listPublishedSkills` (`internal/server/records.go:157` —
  data exists, no CLI/API surface; SPEC.md scoped discovery out of v1);
  `ts-skills uninstall` (SPEC.md:51 excluded it; the writer/lock
  machinery already contains the inverse operations); CLI/scriptable
  publish (SPEC.md:307 excluded it; needs a non-browser auth story
  around CSRF — the largest of the three).
- **Daemon lifecycle logging, Windows CI, `internal/version` →
  buildinfo, `tailnet_test.go` fake**: still deferred from the prior
  efforts (`plans/consolidation/README.md`, `plans/hardening/README.md`);
  unchanged by this audit.
