# Consolidation: unslop the package structure

Consolidation plans for this repo, planned 2026-07-28 at revision
`2037ced944acc38456c090ff62e74c9de099318d`.

## Why this effort exists

This effort **supersedes the structural verdict** in
`plans/hardening/README.md` ("strongly idiomatic", "well-built overall").
That verdict was graded against Go folklore rules — small packages, accept
interfaces, validate constructors — instead of against the actual house
style of public tsnet applications. Compared honestly:

- **tsidp** (full OIDC IdP: PKCE, STS, dynamic client registration, funnel,
  UI): 2 packages, ~3,500 production lines, zero project-defined interfaces.
- **golink**: ~1,600 lines, effectively one package, concrete SQLite store.
- **tclip** (client + server, same shape as this project): ~1,000 lines.
- **this repo**: 12 internal packages, ~7,700 production lines
  (~16,100 with tests), for a skills registry.

The gap is structural, not featural: single-implementation interfaces
(`web.Catalog`, `install.Remote`, `registry.CatalogStore`, `registry.Tree`),
newtype ceremony (identity.go: 218 lines / 29 funcs of constructor+getter
wrappers), a hand-rolled transactional filesystem (~2,600 lines of install
machinery), and wiring-only packages (`daemon` mostly composes the others).
Full analysis with excerpts:
`~/notes/inbox/ts-skills over-decomposition — the tsidp comparison.md`
(pinned to the same revision).

The hardening plans' *behavioral* work (context threading, error classes,
curator capabilities, crash-realistic tests, tsnet credential ownership)
remains valid and is preserved through this effort. What gets removed is
boundary tax, not correctness.

## Target shape

```
cmd/ts-skills/       CLI binary, package main — cli, client, config, install
cmd/ts-skillsd/      thin main over internal/server
internal/server/     daemon + tailnet + web + upload + registry + storage
internal/agentskill/ shared vocabulary — names, digests, SKILL.md, identity, archive contract
internal/safetree/   unchanged — real invariants, genuinely reusable
internal/version/    unchanged — 5 lines, both binaries
```

12 internal packages → 4; estimated ~7,700 production lines → ~4,500.
Organizing rule going forward: **a package exists only if both binaries
import it. Files group features. A constructor exists only if it parses
untrusted input. An interface exists only with two production
implementations or a stdlib contract.**

Owner decisions recorded 2026-07-28:

1. **Crash-recoverable installs are dropped.** The journaled
   transaction/recovery machinery is replaced by stage-and-rename with
   re-run convergence (plan 002). A crash mid-install is repaired by
   running `install`/`restore` again.
2. **Live tailnet smoke test runs first** (plan 001) so every later stage
   is diffed against a known-good behavioral baseline.

## Execution order & status

(Numbers are never reused or renumbered.)

| # | Plan | Title | Effort | Risk | Depends on | Status |
|---|------|-------|--------|------|------------|--------|
| 1 | [001](001-live-tailnet-baseline.md) | Record a live-tailnet behavioral baseline | M | LOW | — | DONE (dev-only baseline; tailnet auth key unavailable) |
| 2 | [002](002-stage-and-rename-installer.md) | Replace the journaled installer with stage-and-rename | L | HIGH | 001 | DONE |
| 3 | [003](003-flatten-identity-and-move-vocabulary.md) | Flatten value ceremony; move shared identity into agentskill | L | MED | 001 | DONE |
| 4 | [004](004-merge-server-package.md) | Merge the server side into internal/server | L | MED | 002, 003 | DONE |
| 5 | [005](005-merge-client-into-cmd.md) | Merge the client side into cmd/ts-skills; dissolve protocol | L | MED | 003, 004 | TODO |
| 6 | [006](006-sweep-docs-and-final-comparison.md) | Export sweep, docs, metrics, final baseline comparison | M | LOW | 002–005 | TODO |

Status values: TODO | IN PROGRESS | DONE | BLOCKED (one-line reason) |
SUPERSEDED (one-line pointer to what replaced it)

Each plan is one jj commit (repo rule: commit per phase, never accumulate
one giant working-copy change). Each executor: read the plan fully before
starting, honor its STOP conditions, and update your row when done.

## Dependency notes

- **001 → everything**: the baseline transcript is the reference every
  later plan re-verifies against. Behavior changes without a baseline are
  unfalsifiable.
- **002 before 004**: the install machinery must be *deleted* inside its
  current package boundary, not moved into the merged layout and deleted
  there — moving ~2,600 lines twice doubles review surface for nothing.
  (002's output stays in `internal/install` until 005 moves the survivor.)
- **003 before 004/005**: shared identity must land in `agentskill` first,
  or both merges carry a registry dependency into places it must not
  survive (client code importing server code).
- **004 before 005**: the end-to-end network-install test moves from
  `internal/web` into `cmd/ts-skills` and needs `internal/server` to exist
  to import.
- **006 last**: docs and metrics describe the landed shape; the final
  smoke run compares against 001's transcript.

## Absorbed deferred items

These `plans/hardening/README.md` deferred/considered items are resolved
by this effort rather than tracked separately:

- **`web.Catalog` mirror interface** — deleted in 004.
- **`cli.Run` unused `stdin` parameter** — deleted in 005.
- **`install` exported lock model/codec + `Project.StateDir`** —
  privatized/deleted across 002 and 005.
- **Duplicate `filesystem_linux.go`/`filesystem_bsd.go`** — collapsed in
  002 (most of the fsync ladder is deleted outright).
- **Splitting `agentskill_test.go`**: still rejected; agentskill grows in
  003 and 005 but stays one cohesive package.

## Considered and rejected

(So nobody re-plans these.)

- **Keeping the 12-package layout and only deleting interfaces**: the
  packages *are* the interfaces' reason to exist; removing one without the
  other leaves either import cycles or the same plumbing under new names.
- **`internal/client` as an importable package instead of `cmd/ts-skills`
  package main**: the binary is the only consumer; golink and tclip put
  application code in the command package. The one cross-cutting consumer
  (the e2e network-install test) moves into the main package, which may
  import `internal/server` freely.
- **Merging `safetree` into `agentskill`**: safetree is a real unit — its
  own invariants (portable path rules, snapshot ownership), its own tests,
  no knowledge of skills. It stays.
- **Deleting *all* validated types (`agentskill.Name`, `TreeDigest`,
  `SkillID`, `CandidateID`, `client.Origin`)**: these parse untrusted
  input (uploads, DB rows, lock files, config) — exactly the constructors
  the house rule keeps. Only ceremony around *trusted internal* values goes.
- **Replacing SQLite with golink-style JSON snapshots**: SQLite already
  guards the concurrent first-publication race with real transactions
  (hardening 010); storage is not where the bloat is.
- **A `pkg/` or exported API surface**: nothing outside this module
  consumes any of it. Everything stays `internal/` or `cmd/`.
- **Renaming the module** away from `ts-skills`: out of scope; cosmetic.

## Deferred

(Real, but not planned — one line each.)

- **`internal/version` → `runtime/debug` buildinfo**: five lines either
  way; decide when release tooling is next touched.
- **`tailnet_test.go` `roundTripFunc` fake → `httptest.Server`**: carry the
  test as-is through 004; rework when the tailnet client next changes.
- **Daemon lifecycle logging** (serve start/stop, drain events): still
  wants its own logger-ownership decision; unchanged by the merges.
- **Windows CI**: install swap semantics in 002 use rename over live
  trees; Linux/macOS verified by tests, Windows only by the path rules.
  Add a Windows runner when the project earns one.

## Reconciliation log

Newest first. Date, what happened, commit, deviations, next executable plan.

- **2026-07-29**: Plan 004 merged daemon, Tailnet, catalog, SQLite, upload, and HTTP code into `internal/server`. The server now has one concrete catalog and no project-defined interfaces; `cmd/ts-skillsd` imports only its four public entry types and functions. The dev-mode golden path matched the baseline: publication digest `sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b`, archive hash `36b85731be73e6819fa01f56375eea27b44b7836c793d17bd9de712de2af0696`, lock hash `6a054deb252285fd13028035f4f77bbc73337977fd5e354e1d28c1d84c690446`, restored tree, and missing-skill error all matched. `go build ./...` and `just test` passed. Next: 005.
- **2026-07-29**: Plan 003 moved validated namespace, skill, publication, and candidate identities into `agentskill`; catalog and installer records are now plain exported-field structs. The dev-mode golden path kept the baseline publication digest and lock hash. Next: 004.
- **2026-07-29**: Plan 002 replaced the journal and recovery ladder with same-directory stage-and-rename. The requested publication or lock is authoritative, so install and restore replace selected destinations that disagree with it; `ErrLocalChanges`, manual recovery, and their diagnostics were removed. Litter uses reserved dot-prefixed names, which cannot be Agent Skill names. `go test ./...` passed. Next: 003.
- **2026-07-29**: Plan 001 captured the dev-mode golden path at `006e2bf8`; transcript: `baseline/transcript.md`. No tailnet auth key was available, so section 3 remains required before plan 006 can claim tailnet parity. Next: 002.
- **2026-07-28**: Effort filed — plans 001–006 written from the tsidp
  comparison (see note pinned to `2037ced9…`); no code changes yet.
  Next: 001.
