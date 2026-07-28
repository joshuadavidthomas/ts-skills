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
| 2 | [016](016-single-agentskill-inspection.md) | One agentskill inspection operation | M | 001 | DONE |
| 3 | [002](002-bound-daemon-shutdown.md) | Bound daemon shutdown | M | 001 | DONE |
| 4 | [003](003-preserve-error-classes.md) | Preserve error classes at boundaries | M | — | DONE |
| 5 | [004](004-join-cleanup-errors.md) | Join cleanup errors on failure paths | M | — | DONE |
| 6 | [005](005-web-handler-error-hygiene.md) | Buffer templates before status; log unexpected errors | S | 004 | DONE |
| 7 | [006](006-cli-exit-and-diagnostics-shape.md) | Exit once at main; print diagnostics once | S | — | DONE |
| 8 | [017](017-close-identity-representation-holes.md) | Close Snapshot nil-FS + CandidateID representation holes | S | — | DONE |
| 9 | [018](018-simplify-storage-lifecycle-and-results.md) | Close-phase state; shrink publish result shapes | M | — | DONE |
| 10 | [012](012-carry-validated-trees-through-capture.md) | Carry validated trees through capture; storage agreement check | M | 001 | DONE |
| 11 | [010](010-move-catalog-transition-rules-into-registry.md) | Move catalog transition rules from storage into registry | L | — (sequence after 012, 017, 018) | DONE |
| 12 | [011](011-validate-the-recovery-model.md) | Validate recovery journal as one model; crash-realistic tests | L | — (before 008) | DONE |
| 13 | [015](015-decouple-fetch-from-writer-lock.md) | Fetch unlocked; recovery visible in failure contracts | M | — (after 011; before 008) | DONE |
| 14 | [009](009-require-curator-authority.md) | Require a curator capability at every catalog mutation | M | — | DONE |
| 15 | [013](013-put-archive-contract-in-protocol.md) | Rootless-ZIP contract into protocol; rejections through Fetch | M | — | DONE |
| 16 | [014](014-own-tsnet-credentials-and-diagnostics.md) | Explicit tsnet credentials + diagnostics ownership | M | — (after 002, 006) | DONE |
| 17 | [019](019-split-daemon-runtime-construction.md) | Split daemon runtime construction from serving | M | 002 | DONE |
| 18 | [020](020-type-the-registry-origin.md) | One refined registry-origin value | S | — | DONE |
| 19 | [007](007-apply-portable-safetree-rules.md) | Apply Windows path restrictions on every platform | S | — (needs owner sign-off, see plan) | DONE |
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

- **2026-07-28**: 007 landed — safetree now rejects Windows-reserved components, trailing dots or spaces, and case-colliding paths on every host. Linux registries will reject uploads they previously accepted when those trees cannot install on Windows. The platform dispatch files are gone; canonical keys remain validation-only, so tree digests stay unchanged. New Builder tests prove reserved names and case aliases fail on Linux. `go build ./...`, `go test -race ./internal/safetree/ -count=1`, focused portability tests, and `just check` passed. Deviation: web upload fixtures now use a Windows-portable asset name; their old `<script>` filename is invalid by this policy. Next: 008.

- **2026-07-28**: 020 landed — `client.Origin` now parses and holds the validated registry base as an opaque value. Config produces it, the CLI passes it through, and `NewRemote` accepts no raw URL. `Origin.URL` returns a copy for `net/url` consumers. Parser tests cover accepted forms and every rejected origin shape; config's output feeds the real client constructor. `go test -race ./internal/config/ ./internal/client/ ./internal/cli/ -count=1` and `just check` passed. Deviation: `internal/web/network_install_test.go` adapted its direct test-only `NewRemote` call to parse an Origin; this was required by the API boundary. The daemon retains its separate dev-listener loopback check; it is not registry-origin validation. Next: 007.

- **2026-07-28**: 019 landed — `Run` and `RunDev` now validate their own mode, build a complete runtime, then hand it to `serve`. The factory seam, fabricated production config in dev mode, and serving-time incomplete-runtime check are gone. Builders take normalized configs and return runtime values; shared serving owns handler gating, shutdown, draining, and cleanup unchanged. Serving tests now build real complete runtimes directly, and a `RunDev` test proves invalid loopback configuration cannot start a listener. `go test -race ./internal/daemon/ -count=1`, `just build`, `rg "runtimeFactory" internal/daemon`, and `just check` passed. Deviation: none. Next: 020.

- **2026-07-28**: 014 landed — production enrollment now resolves `TS_SKILLSD_AUTHKEY_FILE`, then `TS_AUTHKEY`, and only permits a credential-free start from an enrolled tsnet profile with a saved node key. On a first start it names unsupported tsnet credential inputs; after enrollment, daemon startup clears those inputs so tsnet cannot discover them. `ServerConfig` now carries diagnostics: daemon routes both backend and user status through its logger only when verbose, otherwise discards both. The adapter no longer changes process globals; `hostinfo.SetApp("ts-skillsd")` runs in the daemon binary. Deployment docs name the supported chain and rejected ambient inputs. Tests cover chain precedence, incomplete and enrolled state, ambient-input rules and cleanup, plus tsnet field/log mapping. `go test -race ./internal/daemon/ ./internal/tailnet/ -count=1`, `go build ./...` passed. Deviation: added `TSNET_FORCE_LOGIN` and `TS_CONTROL_URL` to the rejected inputs because each changes tsnet startup outside daemon configuration. Next: 019.

- **2026-07-28**: 013 landed — `protocol` now defines the v1 rootless ZIP contract: classic, single-disk, Store-only entries with a 256-byte per-entry metadata allowance. `TreeArchiveCeiling` derives a safe envelope from tree limits, including overflow protection; client and web use it, and web rejects an archive that exceeds it. Client rejects deflated entries alongside malformed ZIP forms. Archive rejection tests now run through `Remote.Fetch` over HTTP; the direct decode test remains only for the deterministic cancellation mapping it covers. The web download test proves the encoder's output stays below the shared ceiling and contains Store-only entries. `go test -race ./internal/protocol/ ./internal/client/ ./internal/web/ -count=1` passed. Deviation: `internal/web/web_test.go` joined the scope because the plan's stated web-conformance test required it; the plan scope now records that file. Next: 014.

- **2026-07-28**: 009 landed — `registry.Curator` now carries the actor
  authorized to change the catalog. `Capture`, `Publish`, and `SetCurrent`
  require it; Capture derives its stored provenance from `Curator.Actor()`.
  The web port asks for a curator only on mutation routes. Tailnet validates
  the peer identity before it parses the curation capability, then returns a
  curator or `registry.ErrCurationDenied`; the handler keeps denials at 403
  and failed identity resolution at 401. Dev mode returns its fixed curator.
  Tests cover curator construction, Capture provenance, denied mutation
  routes, resolver failures, and incomplete identities without a capability.
  `just check` passed. Deviations: 010 had already changed the catalog
  storage port, so this plan applied its capability signatures to the landed
  shapes. Next: 013.

- **2026-07-28**: 015 landed — Install now fetches before it acquires the project writer. Restore captures the lock bytes and each locked destination's matching state, fetches missing publications without a writer, then reacquires, rejects any changed snapshot with `ErrProjectChanged`, and stages, verifies, and installs under the writer. Project-local staging stays writer-owned because orphan recovery removes unowned `staging-*` trees. Writer acquisition records successful orphan or transaction recovery; failures after that fact match `ErrRecovered`, and CLI diagnostics name the recovered prior update instead of claiming the project stayed unchanged. Tests prove blocked Install and Restore fetches leave the writer free, lock and destination changes fail closed, partial recovery facts survive a later recovery error, and crash recovery still reports the original failure plus `ErrRecovered`. `go test -race ./internal/install/ ./internal/cli/ -count=1` passed. Deviation: `transaction.go` now returns whether recovery completed work so writer acquisition can preserve the recovery fact; recovery policy and journal phases are unchanged. Next: 009.

- **2026-07-28**: 010 landed — `registry.Catalog` now owns publication and
  current-selection decisions. Its narrowed `CatalogStore` port records
  candidates, publications, selections, and reads; `PersistPublication`
  atomically writes a new publication and the registry-selected initial
  current entry. SQLite keeps uniqueness and transaction guarantees, including
  the concurrent first-publication race. `SetCurrent` checks that the target
  publication exists before it asks storage to persist the selection. The
  in-memory policy fake is gone: registry integration tests use SQLite, force
  two publishers to observe no current selection before either write, and
  prove one current publication survives. Storage tests cover restart facts
  and roll back a publication when its initial selection fails. `just test`
  passed. Deviations: none. Next: 011.

- **2026-07-28**: 012 landed — `upload.StageBrowserDirectory` now owns the
  untouched multipart reader after web reads `namespace`, including the
  manifest-position check. `Submission.Snapshot` lends the validated staging
  tree while Submission retains close ownership; web passes it directly to
  `registry.Capture`, which inspects it without a second `StageFS` copy.
  Registry tests prove Capture preserves borrowed-snapshot ownership and
  derives the candidate digest from that staged tree; the web upload test
  matches stored and submitted digests. Storage now reloads the
  copied `SKILL.md` before tree installation and rejects a candidate whose
  skill name differs, leaving neither candidate metadata nor a digest tree.
  Upload's multipart-order test moved out of web. `StageFS` remains only for
  its own test coverage and registry test setup; no production capture path
  stages twice. `just check` passed. Deviations: none. Next: 010.

- **2026-07-28**: 018 landed — `storage.Catalog`'s four close booleans are
  now one `closePhase` (`catalogOpen → catalogDatabaseOpen → catalogLockHeld
  → catalogClosed`) whose comment states the legal progression; `Close`
  walks it with fallthrough so a retried close resumes at the surviving
  resource, and `withOpenState` rejects anything past `catalogOpen`. The
  flag-combination close tests became behavior assertions: operations pass
  after `ErrTreesOpen`, are rejected after close begins, resumption is
  proven through the injected-closer call counts plus lifetime-lock
  conflicts, and a closed catalog rejects operations while reopening a new
  `OpenCatalog` succeeds. `PublishCandidate`/`Publish` return
  `(Publication, error)`, `SelectCurrent`/`SetCurrent` return `error`, and
  `PublishResult` is deleted — no production consumer existed (web
  discards both results). `CurrentPublication` stays as the stored
  audit-row shape: `SelectCurrent` still validates selections through
  `NewCurrentPublication` before persisting `selected_*` columns, which
  remain as stored audit data. Tests now read the catalog —
  `ResolveCurrent`, `Publication`, repeated-publish equality — instead of
  transition flags. Deviations from the plan text: `registry/model_test.go`
  was touched to drop the dead `becameCurrent ⇒ created` invariant, one
  file beyond the in-scope list; no other deviations. Next: 012.

- **2026-07-28**: 017 landed — `Snapshot.FS` returns an unexported
  `closedFS{}` (opens fail `fs.ErrClosed`) for nil, zero-value, and
  successfully closed snapshots; live snapshots unchanged. `Close` also
  treats the zero value (`path == ""`) as already closed, extending the
  nil-safe convention to the zero value as the plan's maintenance notes
  direct. `CandidateID` is now `struct{ id [16]byte }` with `IsZero`,
  `Bytes`, and `CandidateIDFromBytes([16]byte)` (zero-rejecting, documented
  as the storage-only persistence seam); the two `id == (CandidateID{})`
  checks in entities.go moved to `IsZero()`. Storage's
  `candidateIDBlob`/`candidateIDFromBlob` map bytes↔value directly — no
  more BLOB→hex→parser round-trip. New tests: safetree nil/zero/closed FS
  forms (the two pre-existing post-Close `fs.ErrNotExist` assertions in
  `TestFinishTransfersOwnership` and
  `TestSnapshotCloseRetainsStagingAfterRemovalFailure` were re-pointed at
  `fs.ErrClosed` — same ownership claim, new matchable class),
  registry zero-rejection across `ParseCandidateID` and
  `CandidateIDFromBytes` plus text/bytes round-trips, and a storage BLOB
  round-trip rejecting short and zero blobs. Deviations from the plan text:
  none beyond the zero-value Close guard noted above. Next: 018.

- **2026-07-28**: 006 landed — `cli.parseFlags` wraps non-help parse
  failures in an unexported `reportedError{}` (the FlagSet already printed
  cause+usage); `flag.ErrHelp` passes through so each subcommand returns
  nil for `-h`. `cli.AlreadyReported(err)` is the exported predicate;
  `cmd/ts-skills/main.go` exits 1 silently when it matches, prints once
  otherwise. `cmd/ts-skillsd/main.go` is now a thin `main` over
  `run(os.Args[1:])` with straight-line early returns; the dev-mode
  stderr banner and version fast-path stay byte-identical, and `stop()`
  always runs because all defers live inside `run`. A package comment in
  cli.go states the policy per the maintenance note. New tests cover
  `-h` exiting nil with usage on stderr for both subcommands, unknown
  flags satisfying `AlreadyReported` with the error text printed exactly
  once, and `AlreadyReported` accepting a wrapped marker while rejecting
  plain errors. Deviations from the plan text: (1) the plan sketched
  wrapping parse errors inline at each subcommand; the wrapped
  construction lives in one `parseFlags` helper since the pattern is
  identical across subcommands; (2) one test assertion was corrected
  mid-flight — the FlagSet itself echoes the error text to stderr, so
  "not present in stderr" could never hold; the test now counts
  occurrences and asserts exactly one print. Next: 017.

- **2026-07-28**: 005 landed — `render`/`renderError` now execute templates
  into a buffer and commit status only after success (shared `writePage`
  helper); a failed page template falls back to the buffered 500 error page,
  and a failed error template degrades to `http.Error` plaintext. The
  default branch of `handleError` logs `web request failed` with method,
  path, and cause while the response stays generic. Post-commit failures are
  logged at Warn: buffered-body writes, the upload early-return submission
  close, the publication archive close/remove, and the two page-handler
  `tree.Close()` defers. `Options.Logger` (*slog.Logger, nil defaults to
  `slog.Default()`) carries the logger. New tests cover the buffered 500
  fallback, the plaintext double-failure fallback, default-branch logging
  (message, method, path; generic response asserted leak-free), and the nil
  logger's fall-through to `slog.Default()` proven through a full
  `NewHandler`-built request. Deviations from the plan text: (1)
  `handleError` gained an `*http.Request` parameter so the default branch
  can log method/path as the plan suggested; the 23 call sites were
  rewritten with a one-shot replacement script, deleted after use per the
  repo's scripted-edit rule; (2) the two page-handler `tree.Close()` defers
  004's log said carried `TODO(plan 005)` markers were in fact unmarked —
  logged under the same post-commit rule anyway; (3) `daemon.go` needed no
  edits — no daemon logger exists to wire, so both construction sites
  (production and dev) ride the nil default. Next: 006.

- **2026-07-28**: 004 landed — cleanup errors are joined on failure paths
  across install (writer lock/recovery, staged-tree setup and copy,
  snapshot/verified-tree closes now wrapped under their own labels),
  storage (lock-conflict close, staged copy input close), web (submission
  close before the redirect, ZIP-writer closes joined in `rootlessZIP`),
  and cli (`commandInstaller` cleanup returns an error; `runInstall` /
  `runRestore` join it via named-return defers). Catalog rollbacks always
  run with a `sql.ErrTxDone` filter (new `rollbackTransaction` helper +
  `rollbackTx` test seam), and `NewPublishResult` construction moved ahead
  of `tx.Commit()`. New tests: install identity-mismatch path joins the
  fetched-tree close failure; storage rollback failure is joined while
  post-commit `sql.ErrTxDone` is not; cli constructor failure joins the
  staging-removal failure. Deviations from the plan text: (1) two
  pre-existing discards the current-state list missed were joined under
  the same rule — `rootlessZIP`'s `writer.Close()` on its four error
  paths, and `records.go`'s deferred `rows.Close()` in
  `ListPublishedSkills` (one file beyond the in-scope list, required by
  the step-6 audit); (2) `publicationTree`'s `tree.Close()` was hoisted
  before the response rather than left discarded — the ZIP is fully built
  first, so the close error is still reportable; the archive close/remove
  stays discarded post-commit with a `TODO(plan 005)` marker, as do the
  two page-handler `tree.Close()` defers and the upload handler's
  early-return close (all demonstrably post-commit); (3) test-file
  discards remain per the deferred test-error-checking sweep. Next: 005.

- **2026-07-28**: 003 landed — `StageBrowserDirectory` and `decodeZIP` now
  split AddFile outcomes into four classes: limit, cancellation
  (`context.Canceled`/`DeadlineExceeded`, passed through), invalid path
  (malformed/protocol wrap as before), and everything else (default
  operational wrap, which web's `handleError` routes to 500). Added
  `protocol.ErrInvalidRequest`/`protocol.ErrInternal` sentinels;
  `responseError` maps `invalid_request`/`internal` onto them carrying the
  server-sent prose, and `cli.commandError` gained matching branches.
  Storage `ErrNotFound` returns now wrap the requested candidate /
  publication / skill identity. Test note: the plan's pointed-to
  safetree ctx-injection exemplar no longer exists after 001, and a
  pre-canceled ctx can never reach `decodeZIP` through `fetchTree` (the
  HTTP get fails first), so the cancellation tests call
  `StageBrowserDirectory`/`decodeZIP` directly — the same pattern as
  `TestDecodeZIPPreflightsEntryCount`. Next: 004.

- **2026-07-28**: 002 landed — `runWithHandlerGate` derives a cancellable
  `serverCtx` from the run context, wires it as `http.Server.BaseContext`,
  and cancels it after `shutdownHTTP` returns, so request contexts die with
  the daemon either way (child-of-ctx cancellation actually lands first).
  The bare `handlers.wait()` became a bounded wait derived from the existing
  HTTP shutdown timeout; expiry joins a `handler drain exceeded <bound>
  error into the run result instead of hanging. The old
  `TestRunWaitsForHandlersAfterForcedHTTPShutdown` fixture, which asserted
  the unbounded semantics this plan removed, was reworked into
  `TestRunBoundsDrainWhenHandlerIgnoresShutdown` per the plan's test plan;
  new `TestRunDrainsHandlerObservingRequestContext` covers the
  context-observing handler that drains inside the graceful window. One
  deviation from the plan text: step 2's "existing tests pass unchanged"
  could not hold because that one fixture encoded the pre-plan semantics —
  the plan's own test section sanctioned extending it. Next: 003.

- **2026-07-28**: 016 landed — `agentskill.Inspect` composes `Load` +
  ctx-aware `SumTree` into one `Inspection` value exposing `Directory`,
  `Document`, `Digest`, `FS`, and a `RequireName` helper that wraps
  `ErrInvalidTree`. Registry capture, client download-accept, and install
  `stageAndVerify` each collapsed their Load/SumTree/name-check sequence
  into one `Inspect` call; error mapping at each seam unchanged. New
  package tests cover binding, unsafe entries, RequireName mismatch, and
  ctx cancellation. Next: 002.

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
