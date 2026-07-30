# Deepening: consolidate policy and lifecycle ownership

**Source roadmap:** N/A
**Source feature artifacts:** N/A
**Planned at:** 2026-07-30, working copy `d28c9e6b` (parent `6a886ab8`, bookmark `main`)
**Scope:** Project-wide architecture follow-up after the client/server/package consolidation
**Planner:** Codex project-wide design audit

## Purpose

Turn the project-wide module-design audit into one dependency-ordered effort.
The audit found a sound package map, but several callers still coordinate
configuration, temporary files, durable filesystem transitions, and resource
leases that the owning modules can hide.

This effort is deliberately not a sweep of every thin function. It first
removes proven pass-through structure, then makes tree policy and artifact
ownership explicit, and only then changes the filesystem and client transaction
seams that depend on those decisions.

## What Better Means

- Each policy or lifecycle has one owner.
- Callers request outcomes instead of replaying private checklists.
- The Agent Skill specification, registry identity, protocol, CLI behavior,
  persisted catalog, and publication digest remain unchanged.
- New packages earn their existence by hiding sequencing or policy; no generic
  utility package is introduced merely to deduplicate a few lines.
- Tests prove caller-visible behavior and crash-consistency outcomes rather
  than helper call order.
- The external client remains `client.Run`, the server remains a concrete
  runtime over a concrete catalog, and no speculative repository interfaces
  are added.

## Current State

- Directory syncing is implemented independently in
  `internal/client/fsutil.go`, `internal/server/files.go`,
  `internal/server/catalog/trees.go`, and recursively in
  `internal/tree/stage.go`.
- `tree.Limits` is accepted by some operations while `tree.PrototypeLimits()`
  is selected implicitly by `tree.Stage`, `tree.Encode`, and
  `registry.SumTree`.
- Archive receipt and decoding expose two temporary-resource lifetimes to the
  client.
- The client project transaction is ordered across `installer.go` and
  `writer.go`.
- Browser multipart sizing is exported through the root server runtime.
- `internal/server/catalog/public.go` aliases and forwards declarations that
  already live in the same Go package.
- Server startup divides environment ownership between `cmd/ts-skillsd` and
  `internal/server`, with empty configuration values acting as mode switches.
- Catalog tree reads expose a closeable lease protocol repeated by the machine
  and browser handlers.

Baseline on 2026-07-30:

- `just test` passed.
- `just lint` passed.
- `just vet` passed.
- `jj st` reported a clean working copy before this plan bank was written.

## Desired End State

The mechanical server leaks are gone. Tree format policy is a constructed,
explicit value used by every tree operation. Receiving a publication tree has
one ownership contract. Durable filesystem operations have an agreed semantic
interface before any shared package is introduced. The client project module
owns its complete mutation transaction. Catalog consumers no longer repeat its
tree-lease shutdown protocol. Browser upload ownership, command-session
cleanup, Tailnet identity translation, and the remaining package surfaces each
have one explicit owner.

## Source Summary

| Opportunity | Source type | Audit category | Standards concern | Impact | Effort | Risk | Confidence | Source evidence |
|---|---|---|---|---|---|---|---|---|
| Browser upload limit ownership | Audit | Architecture | Modules / effects | Removes multipart policy from root runtime | S | LOW | HIGH | `internal/server/runtime.go`, `router.go`, `serve.go`, `web/limits.go` |
| Catalog façade deletion | Audit | Architecture / DX | Modules / maintainability | Removes dual names and navigation indirection | M | LOW | HIGH | `internal/server/catalog/public.go` |
| Explicit startup configuration | Audit | Architecture | Boundaries / state | Gives environment parsing and normalized startup facts one owner | M | MED | HIGH | `cmd/ts-skillsd/main.go`, `internal/server/config.go`, `runtime.go` |
| Explicit tree format policy | Audit | Architecture / correctness | Domain model / boundaries | Prevents policy drift between staging, hashing, and archives | M | MED | HIGH | `internal/tree`, `internal/registry/digest.go` |
| Owned receive/decode lifecycle | Audit | Architecture | Modules / effects | Hides temporary archive state and cleanup ordering | M | MED | HIGH | `internal/tree/archive.go`, `decode.go`, `internal/client/remote.go` |
| Durable filesystem seam | Audit | Architecture / correctness | Effects / state / modules | Centralizes crash-consistency knowledge without a utility dumping ground | L | HIGH | MED | client, server, catalog, and tree filesystem transitions |
| Client project transaction | Audit | Architecture / tests | State / effects / verification | Moves mutation ordering and recovery behind the project module | L | HIGH | HIGH | `internal/client/installer.go`, `writer.go` |
| Catalog content access | Audit | Architecture | Modules / effects | Removes repeated tree lease and shutdown ordering | M | MED | MED | catalog `OpenTree`, API and web callers |
| Upload-to-catalog handoff | Audit | Architecture | Modules / effects / boundaries | Keeps staging ownership in web and narrows catalog capture input | S | LOW | HIGH | `web/upload.go`, `catalog/rules.go` |
| Client command session | Audit | Architecture / tests | Modules / effects / verification | Removes four-value lifecycle construction and mutable globals | S | LOW | HIGH | `internal/client/run.go`, `run_test.go` |
| Curation identity ownership | Audit | Architecture / security | Authority / modules / boundaries | Removes transport denial from catalog vocabulary | M | MED | MED | `catalog/errors.go`, `tailnet.go`, `web/handler.go` |
| Shallow surface and test pruning | Audit | Architecture / tests / DX | Modules / maintainability / verification | Removes implementation exports, overlapping accessors, aliases, and plumbing tests | M | LOW | HIGH | protocol, registry inspection, client/server pass-throughs |

## Audit Reconciliation

This table is the completeness ledger for the three specialist reports. “Folded”
means the finding is an explicit step or eval inside another plan rather than a
standalone project.

| Source finding | Disposition | Where |
|---|---|---|
| Client 1 — project mutation has the wrong internal interface | Planned | 007 |
| Client 2 — durability has no owner | Design gate, then planned implementation | 006 → 007 |
| Client 3 — fetch leaks temporary ownership and repeats verification | Planned | 005, then 007 for project-local transfer/revalidation |
| Client 4 — command construction exposes cleanup and mutable globals | Planned | 010 |
| Client 5 — `lockedSkill` and `rejectLink` are zero-depth | Folded, with final sweep | 007 and 012 |
| Shared 1 — durable filesystem behavior has no owner | Design gate | 006 |
| Shared 2 — tree policy is configurable and secretly fixed | Planned | 004 |
| Shared 3 — archive interface exposes a temporary-file state machine | Planned | 005 |
| Shared 4 — protocol exports implementation helpers | Planned last | 012 |
| Shared 5 — daemon/configuration seam is ambiguous | Planned | 003 |
| Shared 6 — `Inspection` has overlapping accessors | Planned last | 012 |
| Server 1 — browser upload cap leaks through runtime | Planned | 001 |
| Server 2 — catalog public façade is shallow | Planned | 002 |
| Server 3 — directory sync duplication must preserve caller policy | Design gate | 006 |
| Server 4 — catalog tree access exposes leases and shutdown ordering | Design gate and implementation | 008 |
| Server 5 — startup uses sentinel configuration | Planned | 003 |
| Server 6 — curation denial is owned by the wrong module | Planned with depth check | 011 |
| Server 7 — web has weak internal locality | Folded after ownership changes | 008 step 4 |
| Server 8 — upload staging exposes snapshot implementation/getters | Planned | 009 |
| Server 9 — CSRF alias and `listenerAddr` pass-throughs | Folded, with final sweep | 003 and 012 |
| Server 10 — tests layer onto implementation | Folded across owners, final audit | 002, 007–010, and 012 |
| Preserve `agentskill`, registry identity/inspection, tree algorithms, protocol contract, catalog, `client.Run`, routes, serving lifecycle, and runtime cleanup | Standing constraint | Index policies, plan out-of-scope sections, and regression checks |
| Keep tiny policy-specific duplicates local; do not split client by size; do not add catalog/remote repository interfaces | Rejected direction | Considered and Rejected |
| `version` is intentionally shallow but useful | No action | Considered and Rejected |

## Plan Order

| Plan | Status | Audit category | Standards concern | Depends on | Ready for routine execution? | Needs deeper planning? | Autonomy boundary | Notes |
|---|---|---|---|---|---|---|---|---|
| [001](001-web-owns-upload-limits.md) | TODO | Architecture | Modules / effects | None | Yes | No | Routine execution | Smallest proven ownership leak |
| [002](002-delete-catalog-public-facade.md) | TODO | Architecture / DX | Modules / maintainability | None | Yes | No | Routine execution | Mechanical end-state rename |
| [003](003-explicit-server-startup-config.md) | TODO | Architecture | Boundaries / state | 001 | Yes | No | Routine within specified shape | Removes sentinel configuration |
| [004](004-explicit-tree-format-policy.md) | TODO | Architecture / correctness | Domain model / boundaries | None | Yes | No | Design review if format semantics move | Foundation for 005 and 007 |
| [005](005-own-archive-receive-lifecycle.md) | TODO | Architecture | Modules / effects | 004 | Yes | No | Routine execution | Preserve response-archive lifetime separately |
| [006](006-settle-durable-filesystem-seam.md) | TODO | Architecture / correctness | Effects / state / modules | 004 | No | Yes | Design artifact and review required | Do not create a one-function package |
| [007](007-deepen-client-project-transaction.md) | TODO | Architecture / tests | State / effects / verification | 005, 006 | No until 006 is accepted | Yes | Design review required, then routine slices | Highest leverage, highest risk |
| [008](008-deepen-catalog-content-access.md) | TODO | Architecture | Modules / effects | 002, 004 | No | Yes | Design review required | Callback versus content module must be settled |
| [009](009-decouple-upload-submission-from-catalog.md) | TODO | Architecture | Modules / effects / boundaries | 002, 004 | Yes | No | Routine execution | Narrows write-side catalog input |
| [010](010-own-client-command-session.md) | TODO | Architecture / tests | Modules / effects / verification | 005, 007 | Yes after dependencies | No | Routine execution | Removes mutable global test seams |
| [011](011-own-curation-identity.md) | TODO | Architecture / security | Authority / modules / boundaries | 002, 003 | No | Yes | Depth and package review required | Never extract an error-only package |
| [012](012-prune-shallow-surfaces-and-tests.md) | TODO | Architecture / tests / DX | Modules / maintainability / verification | 001–011 | Yes after dependencies | No | Routine caller-audited deletion | Run last against the end state |

Status values: TODO | IN PROGRESS | DONE | BLOCKED (reason) |
SUPERSEDED (replacement)

## Dependency Notes

- Plans 001 and 002 are independent and can land first in either order.
- Plan 003 follows 001 so server constructor and runtime option churn happens
  once.
- Plan 004 establishes the tree policy vocabulary used by all later artifact
  and durability work.
- Plan 005 removes the temporary archive lifecycle before the client transaction
  is reshaped.
- Plan 006 is intentionally a design gate. Plan 007 must use its accepted
  durability outcomes rather than inventing another filesystem abstraction.
- Plan 008 is independent of client transaction work after plans 002 and 004.
- Plan 009 shares the catalog/tree declaration churn of plans 002 and 004 but
  is otherwise independent of read-side plan 008.
- Plan 010 follows client artifact and transaction work so it wraps the final
  command collaborators rather than an intermediate shape.
- Plan 011 follows catalog façade and startup changes so identity ownership is
  evaluated against the final composition seam.
- Plan 012 runs last; it re-audits live callers and deletes only scaffolding
  left after the deeper ownership changes.

## Verification Baseline

- `just test` — race-enabled behavior suite.
- `just lint` — formatting, Go lint, and workflow validation.
- `just vet` — Go static analysis.
- `just tidy` — module files remain canonical after package changes.
- `just check` — final full-project gate after the effort.

## Evals / Regression Checks

- `rg -n "PrototypeLimits\\(" internal --glob '*.go'` should eventually show
  only the canonical v1 policy definition and intentional test construction.
- No new exported Go interface is accepted without two real adapters or a
  documented translation seam.
- No new package may contain only pass-through filesystem helpers.
- Existing end-to-end client/server tests continue to prove protocol and
  publication identity compatibility.
- Failure-injection coverage must assert durable outcomes and recoverability,
  not merely helper invocation order.

## Autonomy Boundary

| Action type | Routine execution allowed? | Needs design review? | Needs human approval? |
|---|---|---|---|
| Mechanical deletion, renaming, and ownership moves specified by plans 001–005 | Yes | Only on STOP condition | No |
| Choosing durable filesystem outcome types or commit-state semantics | No | Yes | No, unless behavior changes |
| Reshaping the client transaction around the accepted durability design | After plan 006 acceptance | Yes at the plan boundary | No |
| Changing Agent Skill rules, tree limits, archive format, digest, protocol, persisted data, or CLI behavior | No | Yes | Yes |

## Drift Checks Before Any Plan

- Re-read `AGENTS.md`, this index, and the selected plan.
- Run `jj st` and compare the parent/bookmark with `Planned at`.
- Re-open every cited file before editing.
- Run the plan's narrow baseline tests before changing code.
- Stop if earlier plans changed the named interface without updating this bank.

## Deeper Planning Candidates

| Plan/opportunity | Why it needs depth | Suggested next artifact |
|---|---|---|
| 006 durable filesystem seam | The common syntax is known, but the shared semantic outcomes and commit-state failures must be derived from real callers | Accepted design memo produced by plan 006 |
| 007 client project transaction | Depends on the durability outcome model and changes crash recovery | Update plan 007 after plan 006 memo acceptance |
| 008 catalog content access | Callback leasing and higher-level preview/archive operations offer different leverage | Short design memo or feature outline before implementation |
| 011 curation identity | Catalog ownership is clearly wrong, but an error-only package would be worse | Confirm the resolver/capability adapter moves as one deep module |

## Standing Policies / Decisions

| Decision or policy | Why it should not be re-litigated | Where to record or enforce it |
|---|---|---|
| Agent Skill format code remains confined to `internal/agentskill` | The package models the external specification, not ts-skills policy | `AGENTS.md` architecture section |
| No repository interface around catalog | SQLite is local-substitutable and there is one production adapter | Plans 002 and 008 |
| Thin organizational files are allowed inside a package | File size alone does not create a module or seam | This index |
| Shared mechanics need semantic leverage, not textual duplication alone | Prevents shallow `util`, `fsutil`, and `netutil` packages | Plan 006 and `AGENTS.md` update if accepted |

## Considered and Rejected

| Idea | Audit category | Reason rejected | Revisit if |
|---|---|---|---|
| Move all thin helpers into shared packages | Architecture | Creates shallow modules and couples unrelated policy | A helper becomes part of a repeated outcome-level protocol |
| Add repository interfaces for catalog or remote | Architecture | One concrete adapter already provides the right local seam | A second production adapter or real remote boundary appears |
| Split `internal/client` into subpackages because it is large | Architecture | Its external interface is already deep; transaction locality is the actual problem | A cohesive internal capability develops multiple callers |
| Deduplicate `isLoopbackHost` | Debt | Tiny copies protect different trust policies and do not justify a package | The policies converge and acquire substantial shared behavior |
| Expose more helpers for focused tests | Tests | Would turn private choreography into production surface | A real production seam is identified |
| Deepen or relocate `internal/version` | Architecture | It is a canonical linker-injected fact shared by both binaries; extra structure adds ceremony | It acquires policy or lifecycle behavior |

## Deferred

| Idea | Why deferred | Trigger to revisit |
|---|---|---|
| Additional web file splitting beyond plan 008 | File length alone does not justify structure; wait for content ownership to settle | A remaining workflow has poor locality after plan 008 |
| New caching or repository abstractions | No current adapter variation or measured need | A second production adapter or measured bottleneck appears |

## Reconciliation Log

- **2026-07-30** — Reconciled every specialist-audit item. Added plans 009–012
  for upload handoff, command-session ownership, curation identity, and final
  surface/test pruning; added the source-to-disposition ledger above.
- **2026-07-30** — Consolidated the project-wide module-design audit into
  eight ordered plans. Baseline `just test`, `just lint`, and `just vet`
  passed. No production code changed. Next executable plan: 001 or 002.
