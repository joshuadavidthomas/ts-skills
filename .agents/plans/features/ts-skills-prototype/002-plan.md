---
type: plan
repo: ts-skill-registry
branch: none
sha: b231033509f5
status: ready-for-execution
source_structure_outline: .agents/plans/features/ts-skills-prototype/001-structure-outline.md
---

# Implementation Plan: ts-skills Prototype

> **Executor instructions:** Follow this plan with no hidden session context. Use the end-state names and contracts below. Do not add compatibility aliases, generic repositories, speculative configuration, or alternate source/install modes. If a STOP condition occurs, write a handback instead of improvising.

## Planning Sources

- **Specification:** [`docs/SPEC.md`](../../../../docs/SPEC.md)
- **Accepted structure outline:** [`001-structure-outline.md`](001-structure-outline.md)
- **Agent Skills research:** [`docs/research/agentskills-spec.md`](../../../../docs/research/agentskills-spec.md)
- **Management-project research:** [`docs/research/agentskills-mgmt-projects.md`](../../../../docs/research/agentskills-mgmt-projects.md)
- **Go layout research:** [`docs/research/go-project-layout.md`](../../../../docs/research/go-project-layout.md)
- **Repository setup research:** [`docs/research/new-go-repo-setup.md`](../../../../docs/research/new-go-repo-setup.md)
- **tsnet research:** [`docs/research/tsnet-howto.md`](../../../../docs/research/tsnet-howto.md)

## Overview

Build one private Tailnet registry for manually uploaded Agent Skills. A Tailnet user uploads one ZIP or browser-selected directory, reviews the captured tree, publishes it, and selects its current publication. The CLI installs into one explicit project, writes a digest lock, and later restores that exact tree.

The implementation starts with semantic values and one consumer-owned network seam. It then proves the curate-to-install path in process, adds durable browser curation, adds network installation and project locks, hardens exact restore, and finally composes `ts-skillsd` through `tsnet`.

## Standards Concern

The main design pressure is boundary depth:

- Agent Skills parsing owns format validation and normalized tree identity.
- Registry code owns candidates, publications, current selection, and curation transitions.
- Installation owns project state, locked selections, and its `Remote` seam.
- Upload, SQLite, HTTP, archive, and Tailnet types stop at adapters.
- Interfaces belong to consumers and describe real use cases. Do not add generic `Store`, `Repository`, `Service`, `Clock`, archive, filesystem, or transaction interfaces.

## What Better Means

The prototype is complete when:

1. Equivalent ZIP and browser-directory uploads produce one tree digest.
2. Review, publication, current selection, network fetch, installation, lock, and restore all refer to the same `(SkillID, TreeDigest)` pair.
3. Changing current selection never changes an existing project lock.
4. Unsafe, corrupt, or mismatched trees fail before destination replacement.
5. Registry state survives restart.
6. Project replacement and lock update have deterministic crash recovery.
7. Tailnet identity comes from the inbound Tailnet connection.
8. No uploaded content executes during capture, review, transfer, install, or restore.
9. Git/HTTP acquisition, user-wide scope, versions, roles, revocation, uninstall, and pruning remain absent.

A regression is any duplicate identity, mutable candidate/publication content, ambiguous lock entry, unverified replacement, spoofable actor, hidden host listener, or adapter representation leaking into domain values.

## Current-State Evidence

- `docs/SPEC.md:22-57` — the prototype includes ZIP/directory upload and project installation while excluding VCS, HTTP acquisition, and user-wide scope.
- `docs/SPEC.md:59-63` — every admitted Tailnet user has the same prototype capability level.
- `docs/SPEC.md:124-140` — candidates and publications are immutable; current selection is a separate pointer.
- `docs/SPEC.md:171-180` — locks contain one exact selection per skill and reject duplicate identities.
- `docs/SPEC.md:182-228` — paths and regular-file bytes define the normalized SHA-256 tree identity; permissions do not.
- `docs/SPEC.md:230-250` — capture and installation require bounded staging, path safety, complete verification, and failure before replacement.
- `docs/SPEC.md:347-473` — the provisional Go values and `install.Remote` seam are accepted starting points.
- `001-structure-outline.md:25-31` — the accepted implementation has six phases.
- `001-structure-outline.md:291-306` — the final plan must define exact contracts and may not leave placeholder interfaces to the executor.

## Desired End State

```text
Browser ZIP or directory
        │
        ▼
  bounded staged tree
        │
        ▼
Candidate ──publish──▶ Publication ──select──▶ Current
        │                    │
        └──── immutable tree ┘
                             │
                       HTTPS over tsnet
                             │
                             ▼
                     client staged tree
                             │
                       verify complete tree
                             │
                             ▼
        explicit project destination + digest lock
```

## Scope

- One Go module.
- `ts-skills` and `ts-skillsd` executables.
- ZIP and browser-directory upload of one skill.
- Persistent candidates, publications, current pointers, provenance, and immutable trees.
- Basic server-rendered catalog, upload, review, publish, and current-selection UI.
- One read-only HTTP API for the CLI.
- One configured registry.
- Explicit project path only.
- Install current or exact digest.
- Digest-only TOML lock and exact restore.
- Tailnet-only HTTPS service with connection-derived actor identity.

## Out of Scope

- Git, other VCS, or HTTP source acquisition.
- User-wide installation or project-root search.
- Multiple registries.
- Human release versions, aliases, ranges, or dependencies.
- Separate roles, namespace ownership, rejection, revocation, or quarantine.
- Uninstall, pruning, or lock-driven deletion.
- Public mutation API or CLI publishing.
- Publisher verification, signatures, scanning, or runtime sandboxing.
- Direct execution or preservation of executable file modes.
- Rich client-side application architecture.

## Resolved Prototype Decisions

| Decision | Chosen shape |
|---|---|
| Namespace | NFKC-normalized, case-sensitive UTF-8, 1–64 Unicode scalar values; reject `/`, `\\`, controls, leading/trailing whitespace, `.` and `..` |
| ZIP root | Accept `SKILL.md` at archive root or inside exactly one top-level wrapper; reject ambiguous roots |
| Browser directory | JavaScript sends one selected root, an ordered relative-path manifest, and indexed file parts; ZIP remains the fallback |
| Upload limits | 32 MiB request, 128 MiB expanded total, 2,048 files, 16 MiB per file, 1,024 UTF-8 path bytes, depth 32 |
| Registry storage | SQLite metadata plus immutable extracted tree directories keyed by digest |
| Project destination | `<project>/.agents/skills/<canonical-skill-name>/` |
| Project lock | `<project>/.agents/ts-skills.lock` |
| Project transaction state | `<project>/.agents/.ts-skills/` |
| Config | `os.UserConfigDir()/ts-skills/config.toml`; one registry URL |
| HTTP registry URL | HTTPS, plus loopback HTTP for local development/tests; reject other cleartext origins |
| Tree transport | Rootless ZIP generated from a published tree; archive bytes have no identity |
| Local edits | Fail closed; install/restore does not overwrite a tree that differs from its lock |
| Same Agent Skill name from two namespaces | Reject the second project selection because the required unqualified destination basename collides |
| Concurrent writers | One context-aware exclusive writer per canonical project path |
| Crash recovery | Same-filesystem staging, backup, durable journal, destination swap, atomic lock replacement, next-command recovery |
| Tailnet listener | `tsnet.Server.ListenTLS("tcp", ":443")`; MagicDNS and Tailnet HTTPS are required |

## Package and Dependency Direction

```text
agentskill      safetree       protocol
     ▲              ▲             ▲
     │              │             │
 registry ◀──── storage          client ─────▶ install
     ▲              │             │              ▲
     │              └─────────────┘              │
   upload ─────────▶ safetree                  cli
     ▲                                            │
     └──────── web ◀──── tailnet                cmd/ts-skills
                ▲
              daemon ◀──── storage + tailnet
                │
          cmd/ts-skillsd
```

Dependency rules:

- `agentskill`, `safetree`, and `protocol` import no project domain package.
- `registry` imports `agentskill` and `safetree`.
- `storage` imports `registry` and `agentskill` and implements registry-owned persistence ports.
- `install` imports `registry`, `agentskill`, and `safetree`; it owns `Remote`.
- `client` imports `protocol`, `registry`, `agentskill`, `safetree`, and `install`; it implements `install.Remote`.
- `upload` imports `agentskill` and `safetree`; it may parse a rootless package’s `SKILL.md` to assign its logical root, while multipart and ZIP types stop there.
- `web` owns narrow handler-facing interfaces implemented by `registry.Catalog` and the Tailnet actor resolver.
- `daemon` and `cmd/*` compose concrete implementations and contain no domain policy.

## Type and Interface Contracts

### 1. Agent Skill values

**Package:** `internal/agentskill`

```go
const Filename = "SKILL.md"

type Name struct {
    canonical string
}

type TreeDigest [sha256.Size]byte

type Frontmatter struct {
    Name          Name
    Description   string
    License       *string
    Compatibility *string
    Metadata      map[string]string
    AllowedTools  *string
}

type Document struct {
    Frontmatter
    Instructions string
}

type Directory struct {
    document Document
    files    fs.FS
}

func ParseName(string) (Name, error)
func (Name) String() string

func Parse([]byte) (Document, error)
func Load(fs.FS, string) (Directory, error)
func LoadDir(string) (Directory, error)

func ParseTreeDigest(string) (TreeDigest, error)
func (TreeDigest) String() string
func SumTree(fs.FS, string) (TreeDigest, error)

func (Directory) Document() Document
func (Directory) Open(string) (fs.File, error)
func (Directory) FS() fs.FS
```

Contracts:

- `Name` stores the NFKC canonical form. Its zero value is invalid.
- `ParseName` follows the open Agent Skills Unicode lowercase-letter/number/hyphen rule and length bound. It rejects leading, trailing, or consecutive hyphens.
- `Load` parses `SKILL.md`, validates the logical directory basename when one exists, and roots the returned filesystem at the skill contents.
- Rootless ZIP packages are allowed only at the upload boundary; upload assigns their parsed canonical name as the logical root before `Load` returns a `Directory`.
- `Parse` accepts unknown top-level fields but exposes only standard fields.
- `Directory.Document` clones `Metadata` and optional values. Callers cannot mutate internal parsed state.
- `Directory.FS` is read-only by contract but may reflect a mutable source. It is a parser view, not candidate ownership.
- `TreeDigest` accepts every 32-byte value. Text parsing accepts only `sha256:` plus 64 lowercase hexadecimal digits.
- `SumTree` implements the exact framing in `docs/SPEC.md:212-228`, rejects unsupported paths/files, and ignores mode/timestamp/owner/empty-directory data.

Errors:

```go
var (
    ErrInvalidName       = errors.New("invalid Agent Skill name")
    ErrInvalidDocument   = errors.New("invalid SKILL.md")
    ErrInvalidTree       = errors.New("invalid Agent Skill tree")
    ErrInvalidTreeDigest = errors.New("invalid tree digest")
)

type ValidationError struct {
    Field   string
    Problem string
    cause   error
}

func (e *ValidationError) Error() string
func (e *ValidationError) Unwrap() error
```

Package-private constructors restrict `cause` to the declared validation sentinels. Filesystem errors wrap and preserve `fs.ErrNotExist`, `fs.ErrPermission`, and other underlying causes.

### 2. Shared bounded staging

**Package:** `internal/safetree`

```go
type Limits struct {
    MaxFiles         int
    MaxPathBytes     int
    MaxDepth         int
    MaxFileBytes     int64
    MaxExpandedBytes int64
}

func PrototypeLimits() Limits
func ValidateLimits(Limits) error

type Builder struct { /* private */ }

func NewBuilder(parent string, limits Limits) (*Builder, error)
func (b *Builder) AddFile(
    context.Context,
    string,
    int64,
    io.Reader,
) error
func (b *Builder) Finish() (*Snapshot, error)
func (b *Builder) Close() error

type Snapshot struct { /* private */ }

func StageFS(
    context.Context,
    parent string,
    source fs.FS,
    root string,
    limits Limits,
) (*Snapshot, error)

func (s *Snapshot) FS() fs.FS
func (s *Snapshot) Close() error
```

Contracts:

- `Builder.AddFile` validates the path before filesystem writes, uses actual copied bytes for limits, and tracks exact, case-sensitive, and file/directory-prefix collisions.
- Paths use valid UTF-8, `/`, no backslash, no empty/`.`/`..` components, and the configured depth/byte limits.
- Files are created non-executable; directories are traversable. Links and special files never enter a snapshot.
- `Finish` transfers staging ownership to `Snapshot`. After success, closing the builder cannot remove the snapshot.
- `Snapshot.Close` removes owned staging and is mandatory.
- `StageFS` walks only regular files beneath `root`, preserves that root as the first path component in the snapshot, copies entries through `Builder`, and closes partial state on error. `Snapshot.FS()` is rooted above that component, so callers continue to use the same `root` value.
- ZIP, multipart, registry, protocol, and installation semantics do not appear in this package.

Errors:

```go
var (
    ErrInvalidPath  = errors.New("invalid tree path")
    ErrLimitExceeded = errors.New("tree limit exceeded")
)

type LimitError struct {
    Limit  string
    Max    int64
    Actual int64
}

func (e *LimitError) Error() string
func (e *LimitError) Unwrap() error // ErrLimitExceeded
```

`upload` propagates `LimitError` unchanged and wraps malformed archive/multipart shape with `ErrMalformedUpload`. Web maps `ErrLimitExceeded` to protocol `too_large`/HTTP 413 and malformed or unsafe input to `invalid_request`/400; it never matches error strings.

### 3. Registry identities and facts

**Package:** `internal/registry`

```go
type Namespace struct{ canonical string }
type CandidateID [16]byte

type Actor struct {
    id      string
    display string
}

type SkillID struct {
    namespace Namespace
    name      agentskill.Name
}

type PublicationID struct {
    skill SkillID
    tree  agentskill.TreeDigest
}

type UploadKind uint8

const (
    UploadZIP UploadKind = iota + 1
    UploadDirectory
)

type UploadSource struct {
    kind  UploadKind
    label string
}

type Provenance struct {
    source      UploadSource
    submittedBy Actor
    submittedAt time.Time
}
```

Constructors and accessors:

```go
func ParseNamespace(string) (Namespace, error)
func (Namespace) String() string

func NewSkillID(Namespace, agentskill.Name) (SkillID, error)
func ParseSkillID(string) (SkillID, error)
func (SkillID) Namespace() Namespace
func (SkillID) Name() agentskill.Name
func (SkillID) String() string

func NewPublicationID(SkillID, agentskill.TreeDigest) (PublicationID, error)
func (PublicationID) Skill() SkillID
func (PublicationID) Tree() agentskill.TreeDigest

func NewCandidateID() (CandidateID, error)
func ParseCandidateID(string) (CandidateID, error)
func (CandidateID) String() string

func NewActor(id, display string) (Actor, error)
func (Actor) ID() string
func (Actor) Display() string

func NewUploadSource(UploadKind, string) (UploadSource, error)
func (UploadSource) Kind() UploadKind
func (UploadSource) Label() string

func NewProvenance(UploadSource, Actor, time.Time) (Provenance, error)
func (Provenance) Source() UploadSource
func (Provenance) SubmittedBy() Actor
func (Provenance) SubmittedAt() time.Time
```

`Namespace`, `SkillID`, `PublicationID`, `Actor`, `UploadSource`, and `Provenance` zero values are invalid. `CandidateID` is generated with `crypto/rand` and rendered as 32 lowercase hexadecimal characters. Actor/source strings require valid UTF-8, reject controls/NUL, and have explicit 256-byte bounds. Times are nonzero, UTC, and stripped of monotonic data.

Entities:

```go
type Candidate struct {
    id         CandidateID
    skill      SkillID
    tree       agentskill.TreeDigest
    provenance Provenance
}

type Publication struct {
    id          PublicationID
    candidate   CandidateID
    publishedBy Actor
    publishedAt time.Time
}

type CurrentPublication struct {
    publication PublicationID
    selectedBy  Actor
    selectedAt  time.Time
}

type PublishResult struct {
    publication   Publication
    created       bool
    becameCurrent bool
}

type SkillSummary struct {
    skill   SkillID
    current PublicationID
}

func NewCandidate(CandidateID, SkillID, agentskill.TreeDigest, Provenance) (Candidate, error)
func (Candidate) ID() CandidateID
func (Candidate) Skill() SkillID
func (Candidate) Tree() agentskill.TreeDigest
func (Candidate) Provenance() Provenance

func NewPublication(PublicationID, CandidateID, Actor, time.Time) (Publication, error)
func (Publication) ID() PublicationID
func (Publication) Candidate() CandidateID
func (Publication) PublishedBy() Actor
func (Publication) PublishedAt() time.Time

func NewCurrentPublication(PublicationID, Actor, time.Time) (CurrentPublication, error)
func (CurrentPublication) Publication() PublicationID
func (CurrentPublication) SelectedBy() Actor
func (CurrentPublication) SelectedAt() time.Time

func NewPublishResult(Publication, bool, bool) (PublishResult, error)
func (PublishResult) Publication() Publication
func (PublishResult) Created() bool
func (PublishResult) BecameCurrent() bool

func NewSkillSummary(SkillID, PublicationID) (SkillSummary, error)
func (SkillSummary) Skill() SkillID
func (SkillSummary) Current() PublicationID
```

Constructors validate constituent values. `NewPublishResult` rejects `becameCurrent=true` when `created=false`; repeated publication cannot become the first current publication. `NewSkillSummary` requires the current publication to name the same skill. The persistence operation, not a value constructor, proves that a publication candidate has the same skill and digest.

Registry errors:

```go
var (
    ErrNotFound     = errors.New("registry value not found")
    ErrConflict     = errors.New("registry conflict")
    ErrTreeMismatch = errors.New("stored tree does not match its digest")
)
```

### 4. Registry catalog and persistence ports

```go
type Tree interface {
    fs.FS
    io.Closer
}

type CaptureRequest struct {
    Namespace  Namespace
    Source     fs.FS
    Root       string
    Provenance Provenance
}

type CatalogRecords interface {
    RecordCandidate(context.Context, Candidate, agentskill.Directory) error
    Candidate(context.Context, CandidateID) (Candidate, error)
    OpenCandidateTree(context.Context, CandidateID) (Tree, error)
    PublishCandidate(context.Context, CandidateID, Actor, time.Time) (PublishResult, error)
    SelectCurrent(context.Context, PublicationID, Actor, time.Time) (CurrentPublication, error)
    ListPublishedSkills(context.Context) ([]SkillSummary, error)
    ResolveCurrent(context.Context, SkillID) (Publication, error)
    Publication(context.Context, PublicationID) (Publication, error)
    OpenPublicationTree(context.Context, PublicationID) (Tree, error)
}

type Catalog struct { /* private */ }

func NewCatalog(
    CatalogRecords,
    stagingParent string,
    limits safetree.Limits,
) (*Catalog, error)

func (c *Catalog) Capture(context.Context, CaptureRequest) (Candidate, error)
func (c *Catalog) Candidate(context.Context, CandidateID) (Candidate, error)
func (c *Catalog) OpenCandidateTree(context.Context, CandidateID) (Tree, error)
func (c *Catalog) Publish(context.Context, CandidateID, Actor, time.Time) (PublishResult, error)
func (c *Catalog) SetCurrent(context.Context, PublicationID, Actor, time.Time) (CurrentPublication, error)
func (c *Catalog) ListSkills(context.Context) ([]SkillSummary, error)
func (c *Catalog) ResolveCurrent(context.Context, SkillID) (Publication, error)
func (c *Catalog) Publication(context.Context, PublicationID) (Publication, error)
func (c *Catalog) OpenPublicationTree(context.Context, PublicationID) (Tree, error)
```

Contracts:

- `CatalogRecords` is owned by `registry` and groups the operations that must share one candidate/publication state owner. Do not split it into independently supplied backends or expand it into generic CRUD.
- Phase 2 uses a test-local memory implementation. Phase 3 uses one `*storage.Catalog`.
- `Capture` copies `Source/Root` into owned staging before parse/hash/record. It derives the Agent Skill name from `SKILL.md`; callers supply only the namespace.
- Mutating the caller’s source after `Capture` returns cannot change candidate review or publication bytes.
- `PublishCandidate` atomically inserts the unique `(SkillID, TreeDigest)` publication and inserts current only when none exists. Repeated publication of equivalent content returns the original publication with `created=false`.
- `SelectCurrent` rejects unpublished identities and atomically replaces only that skill’s pointer.
- Every returned `Tree` remains stable until `Close`; callers close it exactly once.

### 5. Persistent registry adapter

**Package:** `internal/storage`

```go
type Catalog struct { /* database and immutable tree store */ }

var ErrTreesOpen = errors.New("registry trees remain open")

func OpenCatalog(context.Context, string) (*Catalog, error)
func (c *Catalog) Close() error
```

`OpenCatalog` takes one state directory and implements `registry.CatalogRecords`. Use `database/sql` with a pinned pure-Go SQLite driver. Enable foreign keys, WAL, `synchronous=FULL`, and a busy timeout on every connection. Use `PRAGMA user_version = 1`; reject any other nonzero schema rather than inventing migrations.

Metadata tables:

```text
candidates(
  id BLOB PRIMARY KEY CHECK(length(id) = 16),
  namespace TEXT NOT NULL,
  name TEXT NOT NULL,
  tree_digest BLOB NOT NULL CHECK(length(tree_digest) = 32),
  source_kind INTEGER NOT NULL,
  source_label TEXT NOT NULL,
  submitted_actor_id TEXT NOT NULL,
  submitted_actor_display TEXT NOT NULL,
  submitted_at_ns INTEGER NOT NULL,
  UNIQUE(id, namespace, name, tree_digest)
)

publications(
  namespace TEXT NOT NULL,
  name TEXT NOT NULL,
  tree_digest BLOB NOT NULL CHECK(length(tree_digest) = 32),
  candidate_id BLOB NOT NULL,
  published_actor_id TEXT NOT NULL,
  published_actor_display TEXT NOT NULL,
  published_at_ns INTEGER NOT NULL,
  PRIMARY KEY(namespace, name, tree_digest),
  FOREIGN KEY(candidate_id, namespace, name, tree_digest)
    REFERENCES candidates(id, namespace, name, tree_digest)
)

current_publications(
  namespace TEXT NOT NULL,
  name TEXT NOT NULL,
  tree_digest BLOB NOT NULL,
  selected_actor_id TEXT NOT NULL,
  selected_actor_display TEXT NOT NULL,
  selected_at_ns INTEGER NOT NULL,
  PRIMARY KEY(namespace, name),
  FOREIGN KEY(namespace, name, tree_digest)
    REFERENCES publications(namespace, name, tree_digest)
)
```

Tree layout:

```text
<state>/registry.sqlite
<state>/trees/sha256/<first-two-hex>/<remaining-hex>/...
<state>/tmp/...
<state>/csrf.key
<state>/tsnet/...
```

`OpenCatalog` acquires an exclusive lifetime lock at `<state>/registry.lock`; a second process using the same state directory fails. Callers own every returned `registry.Tree` and close it exactly once. `Catalog` reference-counts open tree views; `Close` returns `ErrTreesOpen` and leaves the catalog open when any remain rather than invalidating or double-closing them. After all views close, `Close` closes SQLite and releases the lifetime lock. Daemon shutdown waits for handlers before closing storage. Within one process, a private digest-keyed mutex serializes materialization of the same tree. Do not expose either lock as a domain interface.

Tree write order under that digest lock:

1. Copy the supplied validated directory into `<state>/tmp` with normalized non-executable modes.
2. Recompute its digest and reject a candidate/digest mismatch.
3. Fsync every staged regular file and then every staged directory bottom-up.
4. Create missing digest-shard directories and fsync each new directory and its parent.
5. If the final digest directory exists, recompute and verify it, fsync its files/directories bottom-up, and fsync its shard parent before reuse.
6. Otherwise atomically rename the staged tree into its digest path and fsync the shard directory and each newly created ancestor.
7. Release the digest lock only after the durability barrier completes.
8. Insert candidate metadata after the immutable tree is durable.

A concurrent goroutine that loses the materialization race re-enters step 5 after the winner’s durability barrier. A crash may leave an unreferenced immutable tree. Metadata must never point to a missing tree. V1 has no garbage collection, so orphan retention is acceptable. Tests inject failures between every filesystem step and prove that reopening storage never returns metadata for an absent or incomplete tree.

`PublishCandidate` performs candidate lookup, publication insert/reuse, and first-current insert in one SQLite transaction. `SelectCurrent` verifies publication existence and updates the pointer in one transaction.

### 6. Upload adapters

**Package:** `internal/upload`

```go
var ErrMalformedUpload = errors.New("malformed skill upload")

type Submission struct { /* owned snapshot and logical root */ }

func (s *Submission) FS() fs.FS
func (s *Submission) Root() string
func (s *Submission) Label() string
func (s *Submission) Close() error

func StageZIP(
    context.Context,
    parent string,
    src io.Reader,
    filename string,
    limits safetree.Limits,
) (*Submission, error)

func StageBrowserDirectory(
    context.Context,
    parent string,
    multipartBody *multipart.Reader,
    limits safetree.Limits,
) (*Submission, error)
```

ZIP behavior:

- Spool `src` into bounded owned staging before constructing `zip.Reader`; the 32 MiB request limit applies to bytes actually read.
- `filename` is display provenance only: reduce it to a valid UTF-8 basename, reject controls/NUL, and use `upload.zip` when empty.
- Accept either root `SKILL.md` or exactly one top-level wrapper containing `SKILL.md`.
- Reject multiple possible roots, encrypted entries, links, devices, absolute paths, backslashes, duplicates, prefix collisions, and limit violations.
- Ignore explicit directory entries and source modes.
- For a rootless package, parse the root `SKILL.md` name and assign that canonical name as the logical root before returning.

Browser-directory request contract:

- The JSON `manifest` is the first multipart part and contains an array of `{index, path, size}` values.
- Indexes are unique and contiguous from zero.
- File parts follow in manifest order and use names `file-0`, `file-1`, and so on; each manifest entry has exactly one part and no extra parts are accepted.
- Every path begins with one selected root component; exactly one root is allowed.
- Do not use multipart filenames as relative paths.
- `Submission.FS()` preserves the selected or synthesized logical root and `Submission.Root()` returns that same component. Rootless ZIP staging writes its files beneath the parsed canonical Agent Skill name before returning.
- `Submission.Label()` returns the sanitized ZIP filename or selected browser root for provenance.

Both adapters feed `safetree.Builder` and return the same `Submission`. They do not create candidates. The web handler defers `Submission.Close`; `Catalog.Capture` completes its own copy before returning.

### 7. Web curation and identity interfaces

**Package:** `internal/web`

```go
type Catalog interface {
    Capture(context.Context, registry.CaptureRequest) (registry.Candidate, error)
    Candidate(context.Context, registry.CandidateID) (registry.Candidate, error)
    OpenCandidateTree(context.Context, registry.CandidateID) (registry.Tree, error)
    Publish(context.Context, registry.CandidateID, registry.Actor, time.Time) (registry.PublishResult, error)
    SetCurrent(context.Context, registry.PublicationID, registry.Actor, time.Time) (registry.CurrentPublication, error)
    ListSkills(context.Context) ([]registry.SkillSummary, error)
    ResolveCurrent(context.Context, registry.SkillID) (registry.Publication, error)
    Publication(context.Context, registry.PublicationID) (registry.Publication, error)
    OpenPublicationTree(context.Context, registry.PublicationID) (registry.Tree, error)
}

type ActorResolver interface {
    Actor(*http.Request) (registry.Actor, error)
}

type CSRFKey [32]byte

func NewCSRFKey([]byte) (CSRFKey, error)

type Options struct {
    StagingParent string
    Limits        safetree.Limits
    CSRFKey       CSRFKey
    SecureCookies bool
}

func NewHandler(
    Catalog,
    ActorResolver,
    Options,
) (http.Handler, error)
```

Production implementations:

- `*registry.Catalog` implements the one web-owned `Catalog` interface so handlers cannot read and mutate different registry states.
- `*tailnet.ActorResolver` implements actor resolution.
- Handler tests use a fixed actor resolver and test-local catalogs.

Routes:

```text
GET  /                              published catalog
GET  /upload                        upload form
POST /candidates                    ZIP or browser-directory upload
GET  /candidates/{candidate-id}     inert review page
POST /candidates/{candidate-id}/publish
POST /current                       set current from form publication identity
GET  /api/v1/skills/{namespace}/{name}/current
GET  /api/v1/skills/{namespace}/{name}/publications/{digest}/tree.zip
```

`POST /candidates` always uses `multipart/form-data` and the handler applies `http.MaxBytesReader` before parsing. Parts are ordered:

1. `namespace`: one bounded UTF-8 text part parsed by `registry.ParseNamespace`.
2. `kind`: exactly `zip` or `directory`.
3. For `zip`, one `archive` file part and no other parts. Pass its stream and sanitized multipart filename to `upload.StageZIP`.
4. For `directory`, the `manifest` and indexed file parts defined above, with no other parts. Pass the remaining multipart reader to `upload.StageBrowserDirectory`.

After staging, the handler maps `kind` and `Submission.Label()` to `registry.UploadSource`, combines the Tailnet actor and current UTC time into `Provenance`, and calls `Catalog.Capture` with the parsed namespace, submission filesystem, and submission root. Missing, repeated, reordered, or extra parts are malformed requests.

Use `html/template`; show `SKILL.md` in escaped `<pre>` text and list every file. Never convert imported data to trusted HTML. Protect mutations with a pinned CSRF middleware. The daemon loads or creates one 32-byte random key at `<state>/csrf.key`, writes it mode `0600`, constructs `CSRFKey`, and passes it through `Options`; `NewHandler` rejects the all-zero omitted value. Production cookies are Secure and SameSite Lax; tests use a fixed nonzero key and may set `SecureCookies=false`.

### 8. Installation values and remote seam

**Package:** `internal/install`

```go
type Requirement struct {
    skill  registry.SkillID
    digest agentskill.TreeDigest
    exact  bool
}

func Current(registry.SkillID) (Requirement, error)
func Exact(registry.SkillID, agentskill.TreeDigest) (Requirement, error)
func (Requirement) Skill() registry.SkillID
func (Requirement) ExactDigest() (agentskill.TreeDigest, bool)

type LockedSkill struct {
    publication registry.PublicationID
}

func NewLockedSkill(registry.PublicationID) (LockedSkill, error)
func (LockedSkill) Publication() registry.PublicationID

type FetchedTree interface {
    fs.FS
    io.Closer
}

type FetchedSkill struct {
    publication registry.PublicationID
    tree        FetchedTree
}

func NewFetchedSkill(registry.PublicationID, FetchedTree) (FetchedSkill, error)
func (FetchedSkill) Publication() registry.PublicationID
func (FetchedSkill) Tree() FetchedTree

type Remote interface {
    Fetch(context.Context, Requirement) (FetchedSkill, error)
}
```

`Current` and `Exact` remove pointer/nil ambiguity. A successful fetch guarantees the requested skill, the requested digest when exact, a tree rooted at skill contents, and stable bytes until `Close`. The installer independently checks those claims.

`FetchedTree.Close` is mandatory. The installer closes fetched staging after copying and verification and before destination mutation. Production uses `*client.Remote`; tests use test-local scripted or in-process remotes.

### 9. Project, lock, and installer

```go
type Project struct {
    root string
}

func OpenProject(string) (Project, error)
func (Project) Root() string
func (Project) SkillsDir() string
func (Project) LockPath() string
func (Project) StateDir() string

type Lock struct {
    entries map[registry.SkillID]LockedSkill
}

func NewLock([]LockedSkill) (Lock, error)
func (Lock) Skills() []LockedSkill
func (Lock) Lookup(registry.SkillID) (LockedSkill, bool)
func (Lock) With(LockedSkill) (Lock, error)

func DecodeLock(io.Reader) (Lock, error)
func EncodeLock(io.Writer, Lock) error

type Installer struct {
    remote Remote
}

func NewInstaller(Remote) (*Installer, error)
func (i *Installer) Install(context.Context, Project, Requirement) (LockedSkill, error)
func (i *Installer) Restore(context.Context, Project) error
```

`OpenProject` requires an explicit existing directory, resolves the project root once, returns an absolute canonical root, and never searches parents. Before creating or using `.agents`, `skills`, transaction state, a skill destination, staging, or backup, reject every symlink or Windows reparse-point component with platform-specific private helpers. Existing managed destinations must be real directories. Verify staging, backup, destination, and lock paths are on the same filesystem before journal preparation. These checks make all processes able to mutate one destination contend on the same writer lock; if that cannot be proved, return an error before mutation.

Destination mapping:

```text
<project>/.agents/skills/<canonical-Agent-Skill-name>/
```

Namespace remains in registry and lock identity. Since Agent Skill discovery uses the unqualified immediate directory name, `Lock` rejects two entries with the same `agentskill.Name` even when namespaces differ. This prevents one physical destination from representing two publications.

Lock TOML:

```toml
schema = 1

[[skills]]
skill = "team/pdf-processing"
digest = "sha256:..."
```

Decode into a private slice, validate strict known fields and `schema = 1`, then construct the map. Reject duplicate `SkillID`, duplicate unqualified Agent Skill names, malformed digest text, and unsorted/duplicate ambiguity. `Skills` returns a newly allocated slice sorted by canonical `SkillID`. `With` replaces one matching `SkillID` but rejects a collision on unqualified name. The zero `Lock` is valid and empty.

Only `fs.ErrNotExist` at the lock path means a fresh empty project lock. An existing empty, malformed, unsupported-schema, unreadable, or permission-denied lock fails before destination mutation. First lock creation uses the same journal transaction as replacement. Only the project transaction replaces the lock file; do not expose a general `WriteLock` helper.

Install errors:

```go
var (
    ErrBusy                 = errors.New("project is being modified by another ts-skills process")
    ErrUnmanagedDestination = errors.New("destination exists but is not managed by this project lock")
    ErrLocalChanges         = errors.New("managed skill has local changes")
    ErrIdentityMismatch     = errors.New("registry returned another publication")
    ErrDigestMismatch       = errors.New("fetched tree digest does not match publication")
    ErrRecoveryRequired     = errors.New("project transaction requires recovery")
)
```

### 10. Project transaction

Keep these types package-private:

```go
type verifiedTree struct {
    publication registry.PublicationID
    path        string
    owned       bool
}

func (v *verifiedTree) close() error
func (v *verifiedTree) transfer() (string, error)

type projectWriter struct { /* held lock and owned pre-journal staging */ }

func (p Project) acquireWriter(context.Context) (*projectWriter, error)
func (w *projectWriter) recover() error
func (w *projectWriter) stageAndVerify(context.Context, Requirement, FetchedSkill) (*verifiedTree, error)
func (w *projectWriter) install(context.Context, *verifiedTree, Lock) error
func (w *projectWriter) close() error
```

Use `<project>/.agents/.ts-skills/write.lock` with the pinned cross-platform file-lock library. Wait with context cancellation; a canceled wait returns `ErrBusy` wrapping the context cause. Immediately after successful acquisition, the caller defers `projectWriter.close`. Hold the writer through preflight, fetch, verification, destination replacement, and lock update.

The caller immediately defers `verifiedTree.close` after each successful stage. `close` removes an owned unconsumed tree and is idempotent. `install` calls `transfer` only after the durable journal owns the path; transfer clears `owned`. During restore, retain every verified tree and its deferred cleanup until all entries pass preflight and fetching. `projectWriter.close` removes all tracked pre-journal staging before releasing the writer lock.

Before mutation:

- No lock entry and destination exists: `ErrUnmanagedDestination`.
- Lock entry and destination hashes differently: `ErrLocalChanges`.
- Lock entry and destination missing: repair/update is allowed.
- Destination matches the old lock: replacement is allowed.

Each operation lives beneath `<project>/.agents/.ts-skills/operations/<operation-id>/`. Destination, backup, staging, old-lock snapshot, new-lock file, and journal paths are derived from the validated project, skill name, and random operation ID; recovery never trusts arbitrary persisted paths.

Transaction journal:

```json
{
  "schema": 1,
  "operation": "<random-hex>",
  "skill": "team/pdf-processing",
  "old_digest": "sha256:... or empty",
  "new_digest": "sha256:...",
  "had_lock": true,
  "had_destination": true,
  "phase": "prepared|backup-created|destination-swapped|lock-committed"
}
```

Write every journal revision through a temporary file, fsync it, atomically rename it, and fsync the operation directory. Keep an exact old-lock snapshot when `had_lock=true` and a complete new-lock file in the operation directory.

Sequence:

1. Stage and verify the new complete tree under the operation directory.
2. Build the entire new lock; copy and fsync the old lock when present; write and fsync the new lock.
3. Write the durable `prepared` journal.
4. Rename the old destination to `backup` when present, fsync the parent, then persist `backup-created`.
5. Transfer and rename verified staging to destination, fsync it and its parent, then persist `destination-swapped`.
6. Atomically replace/fsync the project lock with the staged new lock, fsync the lock parent, then persist `lock-committed`.
7. Remove backup and remaining operation files, then remove the operation directory and fsync its parent.

On writer acquisition, remove any operation directory that has no durable journal: destination and lock mutation cannot begin before the `prepared` journal is durable. Fsync the operations parent after removal. During successful cleanup, remove backup/staging/lock snapshots first, remove the journal last, fsync the operation directory, then remove it and fsync the operations parent. This ordering ensures a journal-less directory never contains required rollback evidence.

Recovery treats journal phase as a hint and decides from the actual old/new lock bytes and old/new destination digests. It first rejects any destination, backup, staging, or lock content matching neither recorded old nor new state. Then apply this idempotent table:

| Actual lock | Actual destination / backup / staging | Recovery |
|---|---|---|
| Old lock, or absent when `had_lock=false` | Old destination present, or absent when `had_destination=false` | Remove new staging and operation state; keep old state |
| Old lock | Destination missing and backup matches old | Restore backup to destination; remove new staging and operation state |
| Old lock | Destination matches new and backup matches old | Remove the new destination, restore backup, then remove operation state |
| Old/absent lock with `had_destination=false` | Destination matches new | Remove new destination and operation state |
| New lock | Destination matches new | Treat as committed; remove backup/staging/operation state even if the journal still says `prepared`, `backup-created`, or `destination-swapped` |
| New lock | Destination missing and staging matches new | Move staging to destination, fsync, then clean backup/operation state |
| New lock | Neither destination nor staging matches new, but complete old-lock snapshot and old backup/destination remain | Atomically restore the old lock and old destination, then clean operation state |
| Any other combination | Any | Return `ErrRecoveryRequired` without deleting or overwriting evidence |

When old and new lock bytes are equal during missing-destination restore, classify the lock as new before applying the table: a matching new destination is committed, and a missing destination with matching staging rolls forward by installing staging. If both destination and staging are missing, remove the operation state and leave the locked destination missing so a later restore can fetch it again. Recovery actions themselves use the same durable rename/fsync rules and may be retried after interruption. Tests inject a failure before and after every rename, fsync barrier, lock replacement, journal replacement, and recovery action.

After lock commit, cleanup failure does not turn the successful install into failure; leave the committed operation for next-command cleanup. Package tests use a private injected operation table for deterministic failures. Do not export a filesystem or transaction interface.

`Restore` acquires one writer and defers its close. It reads the lock, preflights every managed destination for unmanaged paths or local changes, then fetches and verifies every missing/stale exact publication before any mutation. Any preflight, fetch, close, or verification failure cleans all verified staging and changes no destination. After full preflight, it repairs missing entries one at a time through the journal. Matching entries are no-ops; unlocked paths remain untouched. Restore need not be atomic across skills, but every completed repair agrees with the unchanged lock.

### 11. Protocol and client mapping

**Package:** `internal/protocol`

```go
const Version = "v1"

type CurrentResponse struct {
    Namespace string `json:"namespace"`
    Name      string `json:"name"`
    Digest    string `json:"digest"`
}

type ErrorResponse struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}

const (
    CodeNotFound       = "not_found"
    CodeInvalidRequest = "invalid_request"
    CodeTooLarge       = "too_large"
    CodeInternal       = "internal"
)
```

Map `not_found` to HTTP 404 and `registry.ErrNotFound`, `invalid_request` to 400, `too_large` to 413, and `internal` to 500. Do not serialize internal causes or filesystem paths. Unknown codes/status combinations are protocol errors. Protocol structs contain strings only and import no domain package. Web handlers map domain → protocol. Client code maps protocol → `ParseNamespace`, `ParseName`, `ParseTreeDigest`, and `NewPublicationID`.

A current fetch performs:

1. `GET .../current` to resolve one immutable publication.
2. `GET .../publications/{digest}/tree.zip` for that exact publication.

A current-pointer change between calls is harmless. Exact fetch skips step 1. The tree response is rootless `application/zip`, regular files only, with sorted paths and normalized metadata. Archive order/modes/timestamps do not affect identity.

**Package:** `internal/client`

```go
type Remote struct { /* private */ }

func NewRemote(
    *url.URL,
    *http.Client,
    stagingParent string,
    limits safetree.Limits,
) (*Remote, error)

func (r *Remote) Fetch(context.Context, install.Requirement) (install.FetchedSkill, error)
```

`NewRemote` accepts HTTPS or loopback HTTP only. The base URL must be an origin: no userinfo, query, fragment, or path other than `/`. It requires a non-nil client with a positive timeout, shallow-copies the client, clones the validated base URL, and sets `CheckRedirect` on its private copy to reject every redirect; it never mutates caller-owned values. Reject malformed JSON, unknown error codes, noncanonical identities, unexpected status/content type, unsafe ZIP entries, truncation, and mismatched response identity. Decode the rootless ZIP through `safetree.Builder`, load/hash it, then return an owned closeable staged tree.

### 12. CLI configuration and commands

**Package:** `internal/config`

```go
type Config struct {
    Registry *url.URL
}

func DefaultPath() (string, error)
func Load(string) (Config, error)
```

TOML contains one strict field:

```toml
registry = "https://ts-skillsd.example-tailnet.ts.net"
```

Use `os.UserConfigDir()/ts-skills/config.toml`. Reject unknown fields, missing registry, URL userinfo/query/fragment, non-HTTPS non-loopback URLs, and trailing path other than `/`.

**Package:** `internal/cli`

```go
func Run(
    context.Context,
    []string,
    io.Reader,
    io.Writer,
    io.Writer,
) error
```

Commands:

```text
ts-skills install --project <dir> [--digest sha256:...] <namespace>/<name>
ts-skills restore --project <dir>
```

Both commands accept optional `--config <path>` for tests and nondefault config placement. Do not add implicit project discovery, global scope, source acquisition, uninstall, or force flags. Map sentinels to specific user-facing errors; `cmd/ts-skills/main.go` handles signals and exits nonzero on returned errors.

### 13. Tailnet actor and daemon lifecycle

**Package:** `internal/tailnet`

```go
type ActorResolver struct {
    local *local.Client
}

func NewActorResolver(*local.Client) (*ActorResolver, error)
func (r *ActorResolver) Actor(*http.Request) (registry.Actor, error)

type ServerConfig struct {
    Hostname      string
    StateDir      string
    AuthKey       string
    AdvertiseTags []string
}

type Server struct { /* tsnet server and listener */ }

func ListenTLS(context.Context, ServerConfig) (*Server, error)
func (s *Server) Listener() net.Listener
func (s *Server) LocalClient() (*local.Client, error)
func (s *Server) Close() error
```

`Actor` calls:

```go
who, err := localClient.WhoIs(r.Context(), r.RemoteAddr)
```

Pass unmodified `r.RemoteAddr`; ignore `X-Forwarded-For` and all identity headers. For a human peer, use a stable user ID as `Actor.id` and login name as display. For a tagged peer, use stable node ID and node name/tags. Reject a response without usable node/user identity.

Use Tailscale v1.102.0 or a deliberately reviewed newer pin. The current checked signatures are:

```go
func (s *tsnet.Server) LocalClient() (*local.Client, error)
func (lc *local.Client) WhoIs(context.Context, string) (*apitype.WhoIsResponse, error)
```

`ListenTLS` creates one `tsnet.Server`, sets all exported fields before startup, calls `Start`, waits for `Up` with a two-minute timeout, then calls `ListenTLS("tcp", ":443")`. It requires MagicDNS and Tailnet HTTPS. After `Start` succeeds, every later error path closes the tsnet server before returning. The deployed path opens no ordinary host listener.

Daemon environment:

```text
TS_SKILLSD_STATE_DIR       required
TS_SKILLSD_HOSTNAME        default ts-skillsd
TS_SKILLSD_AUTHKEY_FILE    optional after first enrollment
TS_SKILLSD_TAG             optional single advertised tag
```

The auth-key file is read once and trimmed; never log its contents. Registry storage uses the state directory root; tsnet uses `<state>/tsnet`.

**Package:** `internal/daemon`

```go
type Config struct {
    StateDir string
    Hostname string
    AuthKey  string
    Tag      string
}

func Run(context.Context, Config) error

type runtime struct {
    listener net.Listener
    handler  http.Handler
    close    func() error
}

type runtimeFactory func(context.Context, Config) (*runtime, error)

func run(context.Context, Config, runtimeFactory) error
func buildRuntime(context.Context, Config) (*runtime, error)
```

`Run` calls `run(ctx, cfg, buildRuntime)`. `buildRuntime` opens storage, loads or creates the persistent CSRF key, creates the Tailnet server, obtains its local client, constructs actor resolution and web handlers, and returns one runtime whose `close` owns Tailnet-then-storage cleanup. Every partial-construction error closes resources already acquired. Package tests inject a private `runtimeFactory` with a fake listener, handler, and close recorder, so startup, serve failure, cancellation, and shutdown order require no Tailnet credentials. This seam remains package-private and does not become another application mode.

`run` serves one `http.Server` on the runtime listener. Set `ReadHeaderTimeout` and request-body limits. Shutdown stops accepting requests, gracefully waits for handlers and their opened trees, closes the HTTP server/listener and tsnet server, then closes registry storage and releases its lifetime lock. `cmd/ts-skillsd/main.go` owns signal handling and process exit only.

## Implementation Routing

Execute this plan directly as six phases. Within each phase, make the named checkpoint changes small and reviewable. Do not start the next phase until the phase-level acceptance test passes.

## Phases

### Phase 1 — Semantic scaffold

#### Changes Required

1. Initialize `go.mod`; pin only YAML and Unicode-normalization dependencies.
2. Implement `internal/agentskill` values, parser, validated directory view, digest text, and framed tree hash.
3. Add fixed expected digest vectors for one file, multiple input orders, a multibyte path, and permission independence.
4. Implement registry identity/entity values and constructors without catalog or persistence.
5. Implement install requirement/locked/fetched values and `Remote` without client transport.
6. Do not create fake `main` commands. The binaries appear when real behavior exists.

#### Success Criteria

##### Automated Verification

- [ ] `go test ./internal/agentskill ./internal/registry ./internal/install`
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `gofmt -l .` prints nothing.

##### Evals / Regression Checks

- [ ] Unknown top-level frontmatter survives in the original `SKILL.md` view.
- [ ] `Directory.Document()` returns no mutable aliases.
- [ ] NFKC-equivalent Agent Skill names cannot become distinct identities.
- [ ] No storage, clock, archive, protocol, or generic filesystem interface exists.

### Phase 2 — In-process curate-to-install path

#### Changes Required

1. Add `safetree` builder, snapshot, and `StageFS` with prototype limits.
2. Add registry persistence ports, concrete `Catalog`, and test-local memory records.
3. Implement capture into owned staging, candidate creation, immutable tree reads, publication, first current, explicit current, and current/exact resolution.
4. Implement a test-local in-process `install.Remote` over the catalog.
5. Implement explicit project mapping and verified install staging, returning one `LockedSkill` without a TOML codec.
6. Add one end-to-end application test with `SKILL.md`, asset, and script.

#### Success Criteria

##### Automated Verification

- [ ] `go test ./...`
- [ ] `go vet ./...`

##### Evals / Regression Checks

- [ ] Mutating the source after capture changes neither review bytes nor installed bytes.
- [ ] Altered fetched content leaves a previous valid destination unchanged and produces no locked result.
- [ ] No web, SQLite, ZIP, HTTP, config, or lock-codec type enters the use-case contracts.

### Phase 3 — Persistent browser curation

#### Changes Required

1. Pin the SQLite, CSRF, and cross-platform file-lock dependencies after checking their current Go-version requirements and licenses.
2. Implement `storage.Catalog`, schema creation, immutable tree directories, restart lifecycle, and `registry.CatalogRecords`.
3. Implement ZIP and browser-directory staging through `safetree` with the fixed limits/root rules.
4. Implement catalog, upload, candidate review, publish, and current-selection handlers/templates.
5. Persist upload provenance and actor values.
6. Add restart and equivalent-upload integration tests through `httptest`.

#### Success Criteria

##### Automated Verification

- [ ] `go test ./internal/storage ./internal/upload ./internal/web ./internal/registry`
- [ ] `go test ./...`
- [ ] `go vet ./...`

##### Evals / Regression Checks

- [ ] Equivalent ZIP and directory uploads produce one digest.
- [ ] Unsafe paths, links, collisions, and every limit fail before candidate insertion.
- [ ] Candidate metadata never survives without its complete immutable tree.
- [ ] Imported content always renders escaped.
- [ ] Close/reopen preserves all registry facts and bytes.

##### Manual Verification

- [ ] Use an `httptest` or temporary local harness to upload one ZIP and directory and inspect both review pages.

### Phase 4 — Network install and project lock

#### Changes Required

1. Implement raw protocol values and read-only current/tree routes.
2. Generate rootless ZIP from published trees only.
3. Implement `client.Remote`, safe staged download, identity mapping, and redirect/size/content checks.
4. Implement strict config and lock TOML codecs, canonical sort, duplicate SkillID rejection, and duplicate unqualified-name rejection.
5. Implement explicit project paths and connect verified install to atomic lock replacement.
6. Implement `cli.Run`, install command, and `cmd/ts-skills`.

Phase 4 may use the final journal transaction directly. If it initially uses only atomic destination/lock writes, Phase 5 must remove that path rather than retain two install implementations.

#### Success Criteria

##### Automated Verification

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build ./cmd/ts-skills`

##### Evals / Regression Checks

- [ ] Current and exact install work through `httptest` into an explicit project.
- [ ] A mismatched response changes neither destination nor lock.
- [ ] Lock decode rejects duplicate full identities and duplicate unqualified names.
- [ ] No wire/archive type crosses `install.Remote`.

##### Manual Verification

- [ ] Run `ts-skills install` against a temporary local test server and inspect the tree and lock.

### Phase 5 — Exact restore and recoverable project transactions

#### Changes Required

1. Reuse the cross-platform file-lock dependency selected in Phase 3 and implement canonical project writer exclusion.
2. Implement and test symlink/reparse rejection for every managed project path.
3. Implement journaled replacement, recovery, failure injection, and cleanup.
4. Move install onto the final transaction path and delete any Phase 4 replacement path.
5. Implement exact restore, local-edit refusal, and missing-tree repair.
6. Add restore command to `cli.Run` and `cmd/ts-skills`.

#### Success Criteria

##### Automated Verification

- [ ] Failure-injection tests cover every journal phase.
- [ ] Concurrency tests prove one writer enters mutation.
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build ./cmd/ts-skills`

##### Evals / Regression Checks

- [ ] Install A, select B as current, and restore locked A.
- [ ] Every injected interruption recovers to matching destination and lock.
- [ ] Local edits fail closed.
- [ ] Restore never removes unrelated or unlocked content.

### Phase 6 — Tailnet-hosted composition

#### Changes Required

1. Pin Tailscale v1.102.0 or review and record any newer selected version.
2. Implement `tailnet.ActorResolver` using `LocalClient.WhoIs(r.Context(), r.RemoteAddr)`.
3. Implement Tailnet TLS listener lifecycle and daemon configuration.
4. Compose storage, CSRF key, web curation, read-only API, identity, and graceful shutdown in `daemon.Run`.
5. Implement `cmd/ts-skillsd`.
6. Document first enrollment, state directory, MagicDNS/HTTPS requirement, ACL reachability, and the two-machine smoke test.

#### Success Criteria

##### Automated Verification

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build ./cmd/ts-skills ./cmd/ts-skillsd`
- [ ] Identity tests prove HTTP headers cannot replace connection-derived actors.
- [ ] Daemon lifecycle tests require no live Tailnet credentials.

##### Evals / Regression Checks

- [ ] The deployed path creates no ordinary host listener.
- [ ] Daemon restart preserves publications and tree downloads.
- [ ] Composition adds none of the deferred subsystems.

##### Manual Verification

- [ ] Machine A uploads and publishes matching ZIP/directory content.
- [ ] Machine B installs current into an explicit project.
- [ ] A spoofed identity header does not change the recorded actor.
- [ ] After current changes, Machine B restores its old digest.
- [ ] A corrupt test transfer leaves Machine B’s valid tree and lock unchanged.

## Autonomy Boundary

- **Routine execution may include:** file/package creation named here, private helper extraction, test fixtures, SQL statements matching the fixed schema, exact route/codec implementation, dependency pinning after license and Go-version checks, and error wording consistent with the defined sentinels.
- **Design review required:** changing a public/internal contract listed here, adding another interface, changing namespace or tree identity rules, changing project paths or lock shape, replacing SQLite/tree-directory storage, changing transaction states, or introducing another listener/source/scope.
- **Human approval required:** Tailnet ACL/tag changes, MagicDNS/HTTPS enablement, auth-key creation, deployment secrets, and the final real Tailnet smoke test.

## Drift Checks

- [ ] Re-open `docs/SPEC.md` and confirm prototype scope has not changed.
- [ ] Re-open the accepted outline and preserve its six phases.
- [ ] Confirm the open Agent Skills specification still uses the documented name/frontmatter rules before parser implementation.
- [ ] Confirm selected dependency versions support the repository’s Go version and licenses.
- [ ] Confirm Tailscale `Server.LocalClient`, `Client.WhoIs`, and `ListenTLS` signatures against the pinned version.
- [ ] Confirm `os.UserConfigDir` behavior on supported development platforms.
- [ ] Confirm filesystem rename, file/directory fsync, symlink/reparse detection, and writer-lock behavior on every platform claimed by the prototype.
- [ ] Confirm validation commands still exist after repository tooling is added.

## STOP Conditions

Stop and write a handback if:

- The current Agent Skills name/frontmatter rules differ materially from the research and require a product choice.
- ZIP and browser-directory inputs cannot be normalized into the same path/content tree without adding source-specific identity.
- The selected server filesystem cannot represent accepted UTF-8 paths without normalization or case collisions.
- SQLite metadata can become visible before its referenced tree is durable.
- A publication/current transition cannot be made atomic under the fixed schema.
- The CLI would need to expose archive or protocol values through `install.Remote`.
- Two namespaces with the same unqualified Agent Skill name need simultaneous installation; the prototype intentionally rejects that physical collision.
- The supported project filesystem cannot provide same-filesystem rename, file/directory durability barriers, symlink/reparse rejection, and a reliable writer lock.
- Failure injection finds a journal state with no deterministic recovery.
- Tailnet HTTPS or connection-derived WhoIs identity is unavailable in the deployment tailnet.
- Any phase appears to require Git/HTTP acquisition, global scope, versions, roles, revocation, uninstall, or compatibility with an obsolete shape.

## Rejected Approaches

- Registry-assigned semantic versions — digests already provide exact identity and version policy adds no prototype proof.
- Archive-byte identity — ZIP and directory upload must converge on path/content identity.
- Executable-bit identity — browser directory selection cannot provide POSIX mode data.
- Nested namespace installation paths — Agent Skill discovery expects the unqualified skill directory directly under the skills location.
- Generic storage/repository interfaces — the catalog needs specific candidate/publication operations and transactional invariants.
- Protocol types in domain packages — raw strings and HTTP concerns belong at the wire edge.
- One-step “download current” tree route — resolving current once and then downloading exact avoids moving-pointer races.
- Direct lock decode into a map — it hides duplicate entries.
- Metadata-first persistence — it can publish candidates whose tree is missing after a crash.
- In-place destination copying — it exposes partial trees and destroys the previous valid state.
- Silent local-edit overwrite or force mode — the prototype fails closed.
- Ordinary host listener plus trusted identity headers — identity must be derived where the Tailnet connection terminates.

## Executor Notes

- Use `jj`, not `git`, for repository operations.
- Keep every imported file inert. No shell, interpreter, package manager, hook, MCP, or skill instruction may run.
- Add interfaces only where this plan names them. Private test seams for deterministic transaction failures are allowed; they must not become cross-package abstractions.
- Close every `Snapshot`, `Submission`, registry `Tree`, `FetchedTree`, `verifiedTree`, and `projectWriter` at the owning call site immediately after acquisition with `defer` where the lifetime permits. Transfer ownership only through the explicit `Finish`/`transfer`/journal contracts.
- Keep candidate/publication/current facts immutable. Mutability belongs only to the current pointer and project transaction state.
- Write errors with operation context while preserving the sentinels and underlying filesystem/network causes.
- Delete transitional implementations when the final phase-specific path replaces them. Do not leave dual storage, lock, install, or listener modes.
