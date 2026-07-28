---
type: structure-outline
repo: ts-skill-registry
branch: none
sha: d8ede01a8663
status: accepted
source_design_discussion: docs/SPEC.md
---

# Structure Outline: ts-skills Prototype

## Review Status

- **Status:** Accepted
- **Accepted:** The six phase boundaries and their validation shape
- **Plan requirement:** The final executor plan must specify detailed type and interface contracts rather than broad file-level instructions
- **Next artifact:** Final executor plan

## Desired End State

A Tailnet user can upload one Agent Skill as a ZIP or browser-selected directory, inspect the captured tree, publish it, and select its current publication. Another Tailnet user can install that publication into an explicit project, receive a digest-only lock entry, change the registry’s current publication without changing the existing lock, and restore the exact locked tree later.

The prototype persists registry state across daemon restarts, never executes uploaded content, rejects unsafe or mismatched trees before replacement, and keeps Git/HTTP acquisition, user-wide scope, versions, roles, revocation, and uninstall outside the implementation.

## Implementation Overview

- [ ] Phase 1: Scaffold settled semantic values and invariants
- [ ] Phase 2: Prove the in-process curate-to-install path
- [ ] Phase 3: Add persistent capture and browser curation
- [ ] Phase 4: Add network installation and project locking
- [ ] Phase 5: Add exact restore and replacement safety
- [ ] Phase 6: Compose the Tailnet-hosted prototype

## Phase 1: Scaffold settled semantic values and invariants

Create the Go module and the three initial internal packages. This phase establishes only types, constructors, tree hashing, parser behavior, and the consumer-owned remote seam. It does not add fake successful commands, persistence, codecs, upload handlers, or transport types.

The executable names remain reserved by the accepted layout. Add each `main` package only when it can compose real behavior in a later phase.

### File Changes

- `go.mod` / `go.sum` — initialize the Go module with only dependencies needed by the settled Agent Skill parser and Unicode normalization.
- `internal/agentskill/name.go` — define canonical `Name`, NFKC comparison, and `ParseName`.
- `internal/agentskill/document.go` — parse standard `SKILL.md` fields while tolerating unknown top-level fields.
- `internal/agentskill/directory.go` — expose a validated parser view over `fs.FS`; return independent document values and preserve original file bytes through the filesystem view.
- `internal/agentskill/digest.go` — define `[sha256.Size]byte` `TreeDigest`, text parsing/formatting, and the accepted `ts-skills-tree-v1` hash.
- `internal/agentskill/*_test.go` — cover name normalization, unknown frontmatter, document ownership, path rejection, and fixed digest vectors.
- `internal/registry/model.go` — define `Namespace`, `SkillID`, `CandidateID`, `Candidate`, `PublicationID`, `Publication`, and `CurrentPublication` with constructor-enforced equality.
- `internal/registry/model_test.go` — cover publication uniqueness inputs, candidate/publication agreement, and current-pointer agreement.
- `internal/install/model.go` — define `Requirement`, `LockedSkill`, `FetchedTree`, and `FetchedSkill`.
- `internal/install/remote.go` — define the single consumer-owned `Remote.Fetch` seam and its postconditions.
- `internal/install/model_test.go` — cover exact versus current requirements and malformed fetched identities.

```go
type PublicationID struct {
    Skill SkillID
    Tree  agentskill.TreeDigest
}

type Remote interface {
    Fetch(context.Context, Requirement) (FetchedSkill, error)
}
```

### Validation

#### Automated

- [ ] `go test ./...` — all semantic constructors, parser behavior, and digest vectors pass.
- [ ] `go vet ./...` — the scaffold has no standard Go correctness findings.
- [ ] `gofmt -l .` returns no Go files — all new Go code is formatted.

#### Evals / Regression Checks

- [ ] Unknown top-level frontmatter remains readable from the captured `SKILL.md` but does not require a generic extension model.
- [ ] Identical paths and contents hash equally despite different source permissions or input order.
- [ ] Canonically equivalent Agent Skill names cannot create distinct `SkillID` values.
- [ ] No store, clock, filesystem, archive, or protocol interface appears in the scaffold.

#### Manual

- None.

## Phase 2: Prove the in-process curate-to-install path

Implement the core use cases without web, persistence, HTTP, TOML, or `tsnet`. A test-local upload starts as an `fs.FS`, is copied into owned staging storage, becomes an immutable candidate, is published, resolves through an in-process `Remote`, installs into an explicit temporary project destination, and produces one `LockedSkill`.

This phase establishes the actual module seams before adapters freeze them. The in-process registry state must remain usable by the later persistent implementation rather than becoming a disposable alternate architecture.

### File Changes

- `internal/registry/capture.go` — copy a supplied tree into registry-owned staging, validate it, compute its digest, and create a candidate.
- `internal/registry/catalog.go` — own candidate, publication, and current-pointer transitions with no candidate status enum.
- `internal/registry/tree.go` — provide read-only access to captured immutable trees without exposing their storage representation.
- `internal/install/installer.go` — fetch, check publication postconditions, stage, validate, hash, and replace one managed skill.
- `internal/install/destination.go` — represent one explicit project destination without project-root discovery.
- `internal/install/installer_test.go` — exercise the complete in-process path through a test implementation of `Remote`.

```text
staged directory
→ immutable candidate
→ publication
→ current resolution
→ fetched staged tree
→ explicit project installation
→ locked publication
```

### Validation

#### Automated

- [ ] `go test ./...` — the first end-to-end application test passes.
- [ ] `go vet ./...` — new operational code has no standard Go correctness findings.

#### Evals / Regression Checks

- [ ] Modifying the original source after capture does not alter candidate review or installed content.
- [ ] A skill containing `SKILL.md`, an asset, and a script installs as a complete tree.
- [ ] Altered fetched content fails before replacing a valid destination or changing the locked selection.
- [ ] Installation never opens an execution path for uploaded files.

#### Manual

- None.

## Phase 3: Add persistent capture and browser curation

Give `ts-skillsd` durable registry ownership and the smallest server-rendered curation flow. ZIP and browser-directory submissions converge on `registry.Capture`; neither becomes a separate domain workflow.

Resolve the persistence representation, ZIP root policy, upload limits, and review details before writing this phase’s final plan. Keep storage and web types inside their adapters; do not leak archive headers, multipart values, or database rows into the registry model.

### File Changes

- `internal/registry/<persistence files>` — persist candidates, publications, current pointers, upload provenance, and normalized tree content using the selected storage design.
- `internal/registry/persistence_test.go` — prove restart durability, publication uniqueness, and first-current behavior.
- `internal/upload/zip.go` — safely decode one ZIP into normalized staging storage.
- `internal/upload/directory.go` — decode browser multipart directory files and relative paths into the same staging form.
- `internal/upload/upload_test.go` — prove equivalent ZIP and directory submissions produce the same digest.
- `internal/web/handler.go` — browse, upload, review, publish, and set-current application handlers.
- `internal/web/templates/` — minimal server-rendered catalog, upload, and candidate-review views.
- `internal/web/handler_test.go` — use `httptest` to cover inert rendering, CSRF, identity requirements, malformed uploads, and mutation flows.

### Validation

#### Automated

- [ ] `go test ./...` — persistence and both upload forms pass their integration tests.
- [ ] `go vet ./...` — storage and web adapters pass standard checks.

#### Evals / Regression Checks

- [ ] ZIP and directory inputs with matching paths and bytes produce one tree digest.
- [ ] Traversal, absolute paths, duplicate normalized paths, links, special files, and oversized inputs fail before candidate creation.
- [ ] Restarting the registry preserves candidates, publications, current pointers, provenance, and tree content.
- [ ] Imported Markdown and filenames render as inert text rather than active HTML.

#### Manual

- [ ] In a browser test session, upload one ZIP and one directory and inspect their review pages.

## Phase 4: Add network installation and project locking

Define the smallest read-only daemon protocol needed by `install.Remote`, then implement the CLI’s registry configuration, explicit-project install command, project destination, and TOML lock write. The HTTP client hides wire/archive details and returns a staged `FetchedTree`.

Resolve registry URL configuration, project destination, lock location, TOML shape, and transport representation before the final plan for this phase.

### File Changes

- `internal/protocol/` — request and response values for current/exact resolution and tree transfer; no mutation API.
- `internal/web/api.go` — read-only handlers needed by the CLI.
- `internal/client/remote.go` — implement `install.Remote` over the selected protocol and return a staged tree.
- `internal/config/config.go` — read one registry location from the chosen TOML path.
- `internal/install/lock.go` — encode one digest per `SkillID`, reject duplicates, and write atomically.
- `internal/install/project.go` — map the explicit project argument to the chosen Agent Skills and lock paths.
- `internal/install/installer.go` — connect successful replacement to lock update.
- `internal/cli/` — parse install arguments and report actionable failures.
- `cmd/ts-skills/main.go` — compose the first real CLI behavior.
- protocol, client, config, lock, project, and command tests alongside these files.

### Validation

#### Automated

- [ ] `go test ./...` — current and exact install work through `httptest` from protocol handler to project files and lock.
- [ ] `go vet ./...` — client, protocol, codec, and command code pass standard checks.
- [ ] `go build ./cmd/ts-skills` — the real CLI entry point builds.

#### Evals / Regression Checks

- [ ] Exact requests reject a response with another skill or digest even when content bytes otherwise hash successfully.
- [ ] A lock never contains two entries for one `SkillID`.
- [ ] Archive or wire encoding changes do not change publication identity.
- [ ] Existing unrelated destination content fails closed rather than being adopted or overwritten.

#### Manual

- [ ] Run `ts-skills install` against a local test daemon with an explicit project and inspect the installed tree and TOML lock.

## Phase 5: Add exact restore and replacement safety

Complete the project workflow by restoring exact locked publications and protecting the relationship between destination content and the project lock. Keep uninstall, pruning, and removed-entry deletion outside this phase.

Resolve interrupted replacement, concurrent writers, and local-edit policy before the final plan. The chosen mechanism must preserve a recoverable prior state when either destination or lock update fails.

### File Changes

- `internal/install/restore.go` — read locked selections, fetch exact publications, and restore missing or stale managed trees.
- `internal/install/transaction.go` — implement the selected staging, replacement, lock-update, rollback, and writer-exclusion protocol.
- `internal/cli/restore.go` — expose restore for one explicit project.
- `cmd/ts-skills/main.go` — compose install and restore commands through the same project/config boundary.
- install and CLI tests for current changes, stale trees, local edits, corruption, interruption, and concurrent writers.

### Validation

#### Automated

- [ ] `go test ./...` — exact restore and failure-injection cases preserve destination/lock agreement.
- [ ] `go vet ./...` — restore and transaction code pass standard checks.
- [ ] `go build ./cmd/ts-skills` — the complete prototype CLI builds.

#### Evals / Regression Checks

- [ ] Publishing and selecting a new current tree does not alter an existing project lock.
- [ ] Restore fetches the old locked tree after current selection changes.
- [ ] Digest mismatch, network interruption, or replacement failure leaves the previous valid installation recoverable.
- [ ] Restore does not remove unrelated or unlocked content.

#### Manual

- [ ] Install one publication, change current, restore the original lock, and compare the resulting tree digest.

## Phase 6: Compose the Tailnet-hosted prototype

Compose persistence, web curation, the read-only CLI API, and Tailnet-derived identity in `ts-skillsd`. Bind through `tsnet`, use one durable service identity, and avoid an ordinary host listener in the deployed path.

Resolve enrollment, state directory, DNS name, and HTTPS configuration before the final plan. Keep every admitted Tailnet user on the same application capability level.

### File Changes

- `internal/daemon/app.go` — compose registry storage, upload handlers, web frontend, CLI API, and identity adapter.
- `internal/tailnet/server.go` — own `tsnet.Server` setup, listener lifecycle, identity lookup, and shutdown.
- `internal/web/identity.go` — pass Tailnet-derived actors to upload and publication operations.
- `internal/daemon/config.go` — define only configuration required by the selected deployment.
- `cmd/ts-skillsd/main.go` — compose the first real daemon behavior and return nonzero on startup or serving failure.
- daemon, Tailnet-adapter, identity, and shutdown tests alongside these files.
- deployment documentation for the one selected prototype environment.

### Validation

#### Automated

- [ ] `go test ./...` — daemon composition, identity propagation, and shutdown tests pass without Tailnet credentials.
- [ ] `go vet ./...` — daemon and Tailnet code pass standard checks.
- [ ] `go build ./cmd/ts-skills ./cmd/ts-skillsd` — both executables build.

#### Evals / Regression Checks

- [ ] Caller identity comes from the Tailnet connection and cannot be replaced by an HTTP header.
- [ ] The deployed daemon exposes no unintended ordinary host listener.
- [ ] Restarting the daemon preserves published and locked content availability.
- [ ] No role, VCS/HTTP import, global scope, version, revocation, or uninstall subsystem appears while composing deployment.

#### Manual

- [ ] From one Tailnet machine, upload and publish matching ZIP and directory inputs.
- [ ] From another Tailnet machine, install the current publication into an explicit project.
- [ ] Publish and select changed content, then prove the first project still restores its old locked digest.
- [ ] Corrupt a test transfer and verify that the valid installed tree and lock remain unchanged.

## Open Questions

These questions are retained from the spec and must be resolved before the named phase receives a final executor plan.

| Question | Needed before |
|---|---|
| What namespace syntax do web and protocol inputs accept? | Phase 3 |
| Which persistence design stores metadata and immutable trees? | Phase 3 |
| Which ZIP root shapes are accepted? | Phase 3 |
| Which concrete upload limits apply? | Phase 3 |
| Which browsers must support directory selection? | Phase 3 |
| Where do CLI configuration and project locks live? | Phase 4 |
| What TOML schemas do configuration and locks use? | Phase 4 |
| Which Agent Skills path exists beneath the explicit project? | Phase 4 |
| Which transport representation carries a tree? | Phase 4 |
| How are destination replacement and lock update recovered after interruption? | Phase 5 |
| How do concurrent writers behave? | Phase 5 |
| What does restore do with local edits? | Phase 5 |
| Which enrollment, state, DNS, and HTTPS settings define the Tailnet deployment? | Phase 6 |

## Final Plan Detail Requirements

The final executor plan must make the type and interface design implementable without hidden conversation context. For every phase, it must include:

- exact proposed signatures for exported and cross-package types, constructors, methods, functions, and interfaces;
- field ownership, value equality, valid zero values, normalization, and mutation rules;
- constructor invariants and the errors returned when those invariants fail;
- interface ownership by the consuming package and the reason each method exists;
- method preconditions, successful postconditions, resource lifetime, and `Close` responsibility;
- package dependency direction and source → internal → target mappings at each adapter boundary;
- which concrete type implements each interface in production and tests;
- persistence and protocol mappings that do not leak database, archive, multipart, or wire representations into domain values;
- focused contract tests, malformed-value tests, and failure-path tests for each boundary;
- removal or postponement of any placeholder type whose meaning still depends on an unresolved question.

Code sketches must use the intended end-state names and shapes. The plan must not leave an executor to invent `Store`, `Service`, `Repository`, transport, transaction, or error interfaces from generic labels.

## Stop Gate

The structure outline is accepted. Stop here before the final executor plan; that plan must satisfy the detail requirements above and resolve or explicitly gate every phase-blocking open question.
