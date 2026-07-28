# internal/ hardening

Hardening plans for this repo's `internal/` packages, planned from two
reviews conducted 2025-07-28 at revision
`a3f57f4975809df1db7c64053922155be4800228`:

1. **Idiom pass** (plans 001–008): repo-wide review against
   `~/notes/wiki/reference/programming-languages/go/idioms/`. Verdict:
   strongly idiomatic; findings were context-not-reaching-the-filesystem,
   flattened error classes, dropped cleanup errors, unbounded shutdown, and
   platform-asymmetric validation.
2. **Design pass** (plans 009–020): same scope under the `coding-standards`
   and `codebase-design` lenses (deep modules, state modeling, boundary
   translation, effect legibility, failure contracts, verification through
   real seams). Verdict: well-built overall; findings concentrate at the
   registry/catalog/recovery trust boundary plus a few ambient-authority
   leaks. Agent logs: `~/.cache/pi/tmp/pi-agent-design-review-*`.

Tracks: **correctness** (001, 002, 011) → **boundary hygiene** (003–007,
013) → **model honesty** (009, 010, 012, 015–018, 020) → **structure**
(014, 019, 008). 008 is a pure move-split and runs last by design.

Execute in the order below unless dependencies say otherwise. Each executor:
read the plan fully before starting, honor its STOP conditions, and update
your row when done.

## Execution order & status

(Recommended execution order — may diverge from numeric order; numbers are
never reused or renumbered.)

| # | Plan | Title | Effort | Depends on | Status |
|---|------|-------|--------|------------|--------|
| 1 | [001](001-thread-context-through-tree-walks.md) | Thread context through tree walks and serving | L | — | DONE |
| 2 | [016](016-single-agentskill-inspection.md) | One agentskill inspection operation | M | 001 | TODO |
| 3 | [002](002-bound-daemon-shutdown.md) | Bound daemon shutdown | M | 001 | TODO |
| 4 | [003](003-preserve-error-classes.md) | Preserve error classes at boundaries | M | — | TODO |
| 5 | [004](004-join-cleanup-errors.md) | Join cleanup errors on failure paths | M | — | TODO |
| 6 | [005](005-web-handler-error-hygiene.md) | Buffer templates before status; log unexpected errors | S | 004 | TODO |
| 7 | [006](006-cli-exit-and-diagnostics-shape.md) | Exit once at main; print diagnostics once | S | — | TODO |
| 8 | [017](017-close-identity-representation-holes.md) | Close Snapshot nil-FS + CandidateID representation holes | S | — | TODO |
| 9 | [018](018-simplify-storage-lifecycle-and-results.md) | Close-phase state; shrink publish result shapes | M | — | TODO |
| 10 | [012](012-carry-validated-trees-through-capture.md) | Carry validated trees through capture; storage agreement check | M | 001 | TODO |
| 11 | [010](010-move-catalog-transition-rules-into-registry.md) | Move catalog transition rules from storage into registry | L | — (sequence after 012, 017, 018) | TODO |
| 12 | [011](011-validate-the-recovery-model.md) | Validate recovery journal as one model; crash-realistic tests | L | — (before 008) | TODO |
| 13 | [015](015-decouple-fetch-from-writer-lock.md) | Fetch unlocked; recovery visible in failure contracts | M | — (after 011; before 008) | TODO |
| 14 | [009](009-require-curator-authority.md) | Require a curator capability at every catalog mutation | M | — | TODO |
| 15 | [013](013-put-archive-contract-in-protocol.md) | Rootless-ZIP contract into protocol; rejections through Fetch | M | — | TODO |
| 16 | [014](014-own-tsnet-credentials-and-diagnostics.md) | Explicit tsnet credentials + diagnostics ownership | M | — (after 002, 006) | TODO |
| 17 | [019](019-split-daemon-runtime-construction.md) | Split daemon runtime construction from serving | M | 002 | TODO |
| 18 | [020](020-type-the-registry-origin.md) | One refined registry-origin value | S | — | TODO |
| 19 | [007](007-apply-portable-safetree-rules.md) | Apply Windows path restrictions on every platform | S | — (needs owner sign-off, see plan) | TODO |
| 20 | [008](008-split-transaction-file.md) | Split internal/install/transaction.go by concern | M | 001, 004, 011, 015 | TODO |

Status values: TODO | IN PROGRESS | DONE | BLOCKED (one-line reason) |
SUPERSEDED (one-line pointer to what replaced it)

## Dependency notes

- **001 → 002 / 016 / 012**: 001 gives `SumTree` a context; 002's bounded
  drain relies on cancellation reaching handler work, 016 composes the new
  signature, 012's capture hashes through it.
- **004 → 005**: same web.go paths; 005 consumes the `TODO(plan 005)`
  markers 004 leaves.
- **017, 018, 012 → 010**: 010 reshapes the registry↔storage port; landing
  it over the smaller registry/storage/identity cleanups avoids triple
  reconciliation. Typed as sequenced, not hard edges.
- **011 → 015 → 008**: 011 models the journal; 015 changes who holds the
  writer when; 008 only *moves* the result. Running 008 earlier would
  fossilize the flat journal into a new layout.
- **002 → 019; 002/006 → 014**: daemon.go conflicts; build each on the
  landed shape.
- **009 after 010** (recommended, not hard): catalog mutations gain the
  `Curator` parameter most cleanly once registry owns the transitions.
- 003, 013, 017, 018, 020 are largely independent; the table order resolves
  trivial file overlap.

## Reconciliation log

Newest first. Date, what happened, PR/commit link, deviations, next
executable plan.

- **2026-07-28**: 001 landed — `SumTree` takes ctx and hashes through a
  cancellation-aware stream (fixed vectors unchanged); storage verify,
  registry capture, client fetch, install preflight, and web
  `resolveTreeFile` all thread the request/operation context. Recovery
  paths (`stabilizeDestination` sync, `classifyTree`) hash and sync on
  `context.Background()` deliberately — journal replay never bails on
  cancellation. web.go had a second `resolveTreeFile` call site
  (candidate review page) beyond the one the plan named; both updated.
  `syncTree` gained per-entry ctx checks with phase-boundary checks in
  `install`. Next: 016.

- **2025-07-28**: Second round filed — plans 009–020 from the design
  review; effort renamed idiomatic-hardening → hardening (directory moved),
  008 gained dependencies on 011/015, README rewritten. Next: 001.
- **2025-07-28**: Effort filed — plans 001–008 written from the idiom
  review; no code changes yet. Next: 001.

## Considered and rejected

(So nobody re-plans these.)

- **Splitting `internal/agentskill/agentskill_test.go` per source file**:
  not a smell — one cohesive package, tests grouped by function prefix,
  strongest tests are cross-file. Revisit only if the file passes ~800
  lines.
- **Adding ctx to `agentskill.Load`/`LoadDir`/`Parse`**: single bounded
  file read; SumTree and tree walks are where cancellation matters (001's
  out-of-scope).
- **`copyContext` single-empty-read retry loop** (safetree.go:186-212):
  contract pedantry; stdlib `io.Copy` behaves no better.
- **Doc-comment sweep**: reviewer nudge only — document constructor
  invariants and error contracts when touching a declaration; no sweep plan.
- **An explicit daemon lifecycle enum/state machine**: the lifecycle is one
  linear transition (start → serve → drain); an enum adds weight without
  modeling anything. 019's constructor/serve split is the honest shape.
- **Capability granularity beyond one curator bit** (roles, per-namespace
  ACLs): not today's product model; 009 deliberately adds only one refined
  type.
- **Removing persisted `selected_actor_id`/`selected_at_ns` columns**:
  stored audit data on an append-only ladder across user databases; 018
  keeps the columns and shrinks only the API shape. Revisit only with a
  retention decision.
- **Keeping `safetree.StageFS` general-purpose**: 012 narrows the capture
  flow but doesn't delete the function — whether any caller deserves it is
  revisited after 012 lands.

## Deferred

(Real, but not planned — one line each.)

- **Logging for daemon lifecycle** (serve start/stop, drain events) — 005
  adds web-request logging only; daemon-level logging wants its own
  decision about logger ownership (note: 014 cleans the tailnet side).
- **Test error-checking sweep** (discarded fixture/parse/Stat errors in
  `install` and `client` tests; `t.Cleanup`-registered closes): diffuse
  hygiene — batch when next working in those files.
- **Duplicate `filesystem_linux.go`/`filesystem_bsd.go`**: collapse to one
  unix-tagged file when install next changes meaningfully.
- **`tailnet_test.go` `roundTripFunc` fake**: bypasses real transport —
  route through `httptest.Server` when the tailnet client next changes.
- **`resolveTreeFile` unbounded reads** (web.go): trusted digest-pinned
  content, but revisit if skill size limits relax.
- **`web.Catalog` mirror interface** (web.go:39-49 — one adapter, nine
  forwarders): delete when next touching the handler; the honest consumer
  interface exists in registry.
- **`cli.Run` unused `stdin` parameter**: remove when a subcommand is next
  added or the dispatch changes.
- **`install` exported lock model/codec + `Project.StateDir`**: used only
  by tests — privatize when 011/015 land and the test surface settles.
