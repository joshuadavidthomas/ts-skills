# ts-skills prototype specification

Status: Implemented prototype v1

This document defines the v1 product boundary. The accepted executor plan at `.agents/plans/features/ts-skills-prototype/002-plan.md` records the storage, protocol, UI, transaction, and deployment choices made during implementation.

## Summary

`ts-skills` is a private Agent Skills registry for a team on a Tailnet.

A team member uses a basic web frontend to upload one skill as a browser-selected directory. The registry captures the complete tree, validates it, computes its digest, and presents it for review. Publishing makes that exact tree available to the team.

The `ts-skills` CLI installs a published skill into one explicit project and records its exact tree digest in a project lock. Restore uses that digest rather than the registry’s moving current selection.

The project ships two Go executables:

```text
ts-skills     # installation CLI
ts-skillsd    # Tailnet registry and web frontend
```

## Prototype boundary

### Included

The prototype includes:

- one central `ts-skillsd` service on the Tailnet;
- persistent registry state across daemon restarts;
- directory selection and upload through the web frontend;
- one Agent Skill per upload;
- immutable candidates for review;
- immutable publications identified by skill and tree digest;
- one explicit current publication per skill;
- project-scoped installation into an explicit project directory;
- exact digest locks and restore;
- complete-tree verification before installation;
- one configured registry per CLI installation.

### Excluded

The prototype does not include:

- Git or other VCS acquisition;
- HTTP source acquisition;
- private source credentials;
- repository discovery or selection among several skills;
- user-wide installation;
- project-root discovery;
- separate submitter and approver roles;
- rejection, revocation, or quarantine workflows;
- uninstall or lock-driven deletion;
- registry-assigned versions or version ranges;
- multiple configured registries.

Directory-only upload is a deliberate v1 narrowing from the original ZIP-and-directory scope. A curator with a ZIP extracts it locally, then selects the extracted skill directory in the web frontend. A curator may still obtain a third-party skill from Git or HTTP manually; automating that acquisition can follow once the curate-and-install path works.

## Access model

Tailnet policy decides who may reach the prototype. Reachability grants read access to the catalog, candidate reviews, and published trees. Uploading, publishing, and changing the current publication require the `joshuadavidthomas.com/cap/ts-skills` application capability with `{"curate": true}`, granted through Tailscale ACL grants.

The prototype has no namespace ownership policy or role hierarchy beyond this curation permission. The daemon derives identity and application capabilities only from WhoIs and records the Tailnet identity responsible for uploads and publications; it never trusts caller-supplied identity headers.

## Core flows

### Upload and publish

1. A team member opens the registry web frontend.
2. They choose one local skill directory.
3. The browser uploads the selected files and their relative paths.
4. The daemon validates and stages the complete tree.
5. The daemon computes its normalized tree digest and creates an immutable candidate.
6. The review screen shows the skill identity, `SKILL.md`, complete file list, digest, and upload provenance.
7. The team member publishes the candidate.
8. The first publication for a skill becomes current automatically.
9. Later publications require an explicit action to become current.

Repeated directory uploads that contain the same normalized paths and bytes produce the same tree digest.

### Install

1. A user configures `ts-skills` with the registry location.
2. They provide an explicit project directory and request a namespaced skill.
3. Without a digest, the registry resolves the skill’s current publication.
4. With a digest, the registry resolves that exact publication.
5. The CLI downloads and safely unpacks the complete tree into staging.
6. The CLI recomputes the tree digest and checks the returned skill identity.
7. After validation succeeds, the CLI replaces the managed project copy.
8. The CLI records the namespaced identity and tree digest in the project lock.

### Restore

1. The user provides the project directory.
2. The CLI reads that project’s lock.
3. It requests each namespaced skill by exact digest.
4. It stages, validates, and verifies each tree.
5. It restores missing or stale managed content without overwriting unrelated content.

Installation uses one writer lock per canonical project. It stages a replacement beside its destination; rerunning install or restore clears crash litter and replaces any selected destination that disagrees with its lock.

## Product requirements

### Agent Skills compatibility

The registry manages a complete Agent Skill directory rooted at `SKILL.md`. It preserves scripts, references, assets, and arbitrary supporting files rather than treating `SKILL.md` as the whole package.

The Agent Skill reader follows the open Agent Skills specification for standard fields and name validation. Registry namespacing exists only in registry, protocol, and lock data; the `SKILL.md` name remains unqualified.

Parsing tolerates unknown top-level frontmatter fields. The initial typed document exposes only standard fields, while the captured tree preserves the original `SKILL.md` bytes.

### Registry identity

A registry skill uses:

```text
<namespace>/<agent-skill-name>
```

Agent Skill names use the open specification’s Unicode-capable validation and NFKC comparison. The typed internal name is canonical; the captured source file retains its original spelling.

`Namespace` remains opaque during scaffolding. Its user-facing grammar can be chosen with the first web and protocol boundary.

### Candidate

A candidate is one immutable captured tree under one skill identity. Review always refers to its exact tree digest.

Candidates have no mutable decision state. Publishing approves content under `(SkillID, TreeDigest)`. A later upload of the same skill and tree may reuse the existing publication.

### Publication

A publication binds one namespaced skill identity to one normalized tree digest. Its identity is:

```text
(SkillID, TreeDigest)
```

At most one publication exists for that pair. Publication content never changes.

Each skill has at most one current publication. The first publication becomes current. Publishing later content does not change the pointer; a user changes it explicitly.

### Upload provenance

The prototype records provenance available at its upload boundary:

- selected directory name;
- submitting Tailnet identity;
- submission time;
- resulting skill identity and tree digest.

The registry does not claim that this proves an upstream author or repository. Upstream source URLs, tags, and commits may be recorded later when remote acquisition exists.

### Project installation

The prototype supports one project scope. The user supplies the project directory directly; the CLI does not search parent directories for a project root.

Skills install under `<project>/.agents/skills/<name>/`; the lock lives at `<project>/.agents/ts-skills.lock`. One physical project location has at most one locked digest for each skill identity.

### CLI configuration

The CLI has one configured registry location. TOML remains the preferred human-editable format.

A likely value is:

```toml
registry = "https://ts-skillsd.example-tailnet.ts.net"
```

The configuration lives at `os.UserConfigDir()/ts-skills/config.toml`. Loopback HTTP is accepted only for local tests and development.

### Project lock

Each lock entry contains only:

- the namespaced skill identity;
- the normalized tree digest.

The lock does not identify the registry in v1. The CLI uses its configured registry and rejects returned content that does not match the locked skill and digest.

A lock contains at most one entry for each skill identity. Duplicate entries are invalid, including identical duplicates. The strict TOML schema and path are defined in the executor plan.

## Normalized tree digest

V1 has no registry-assigned versions or release IDs. The normalized tree digest identifies exact content. A namespaced skill plus digest identifies a publication.

The text form is:

```text
sha256:<64 lowercase hexadecimal characters>
```

### Included in the digest

The tree digest covers:

- every regular-file relative path;
- every regular-file byte.

### Excluded from the digest

The tree digest ignores:

- archive encoding and compression;
- file permissions, including executable bits;
- timestamps and ownership;
- empty directories.

Symlinks and special files are rejected. Paths must be valid UTF-8, use `/` separators, and contain no empty, `.` or `..` segments.

Since browser directory selection cannot report POSIX executable bits, uploads normalize modes away. On systems with POSIX modes, installation creates directories as traversable and regular files as non-executable. Direct execution of bundled scripts is outside the prototype; agents may invoke them through an interpreter.

### Hash framing

The tree hash uses collision-safe framing:

1. Encode the tags `file\0` and `ts-skills-tree-v1\0` as ASCII bytes, where `\0` means one NUL byte.
2. Encode each normalized path as UTF-8 and measure its length in bytes.
3. Encode path and content lengths as unsigned 64-bit big-endian integers.
4. For each file, compute `SHA-256(file-tag || path-length || path-bytes || content-length || content)`.
5. Sort entries by `path-bytes`.
6. Compute `SHA-256(tree-tag || leaf-digest...)` using each leaf digest’s raw 32 bytes.

Scaffold tests must include fixed vectors for:

- a single file;
- multiple files supplied in different input orders;
- a multibyte UTF-8 path;
- the same files with different source permissions producing the same digest.

## Content safety

Upload and installation treat skill content as data. They do not run scripts, package-manager hooks, MCP commands, or instructions from `SKILL.md`.

Browser-selected directories pass through one normalized ingestion path. Before candidate creation, the daemon must:

- validate paths before writing them;
- reject traversal, absolute paths, links, special files, and path collisions;
- enforce request, file-count, path-length, per-file, and expanded-size limits;
- stage the whole tree before accepting it;
- validate `SKILL.md` and its directory relationship;
- compute the digest from the staged tree.

Before installation, the CLI must:

- stage the complete tree;
- recompute the tree digest;
- check that the returned publication matches the requested skill and optional digest;
- refuse digest or validation mismatches;
- avoid overwriting unrelated content;
- preserve the previous valid installation when replacement fails.

The concrete limits, download archive library, replacement mechanism, and crash-recovery policy remain implementation decisions.

## Web frontend

The basic web frontend needs only enough UI to:

- browse published skills;
- select and upload a directory;
- inspect a candidate’s `SKILL.md` and complete file list;
- publish a candidate;
- change the current publication.

The frontend must render imported content as inert text. Mutations require Tailnet-derived identity, CSRF protection, output escaping, and request limits.

The prototype uses server-rendered `html/template` pages and only enough browser JavaScript to encode directory-selection manifests.

## Tailnet service

`ts-skillsd` runs through `tsnet` as a durable service identity. It should not need a matching listener on ordinary host interfaces.

Tailnet policy controls reachability. `README.md` defines daemon enrollment, persistent state, MagicDNS, HTTPS, and smoke-test steps; policy and secret changes remain human-controlled.

## Success criteria

The prototype succeeds when:

1. `ts-skillsd` persists registry state and is reachable by allowed Tailnet users.
2. A user can upload one conforming skill through browser directory selection.
3. Repeated uploads produce the same digest when their paths and contents match.
4. The registry shows the captured `SKILL.md`, complete file list, provenance, and digest before publication.
5. Publishing makes the exact candidate available to the CLI.
6. The first publication becomes current, and later current changes require an explicit action.
7. `ts-skills` installs the current publication into an explicit project.
8. An exact install and restore use the requested tree digest.
9. The project lock contains one `(SkillID, TreeDigest)` entry per managed skill.
10. Changing the current publication does not change an existing lock.
11. Corrupt or mismatched content fails before replacing a valid installation or lock.
12. Upload, review, download, and installation execute none of the skill’s content.

## Out of scope for the prototype

The prototype does not provide:

- VCS or HTTP source acquisition;
- user-wide installation;
- project-root discovery;
- multiple registries;
- registry-assigned versions, aliases, dependencies, or version ranges;
- automatic source polling or updates;
- separate registry roles, namespace ownership, rejection, or revocation;
- uninstall, pruning, or automatic lock-driven deletion;
- public marketplace, ratings, comments, or federation;
- publisher verification, artifact signatures, or malware scoring;
- runtime sandboxing or enforcement of installed-skill permissions;
- hook, MCP server, or package-manager execution;
- per-agent conversion or generated agent configuration;
- a public mutation API or CLI publishing workflow;
- a rich single-page frontend.

## Domain vocabulary

### Skill

A complete Agent Skill directory rooted at `SKILL.md`.

### Skill identity

A registry namespace and canonical Agent Skill name.

### Tree digest

The SHA-256 identity of normalized relative paths and regular-file contents.

### Candidate

An immutable captured skill tree available for review.

### Publication identity

The namespaced skill and tree digest pair.

### Publication

The immutable fact that one candidate’s content was approved under one publication identity.

### Current publication

The publication selected by an install request that omits a digest.

### Locked skill

One exact publication identity selected for a project.

## Provisional Go shape

The following values and one remote seam guide the first scaffold. They do not define storage schemas, HTTP payloads, upload forms, archive encoding, or lock codecs.

### Agent Skill model

```go
package agentskill

import "io/fs"

const Filename = "SKILL.md"

type Name struct {
    canonical string
}

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
    // private parsed document and rooted fs.FS
}

func ParseName(src string) (Name, error)
func (n Name) String() string

func Parse(src []byte) (Document, error)
func Load(fsys fs.FS, dir string) (Directory, error)

func (d Directory) Document() Document
func (d Directory) FS() fs.FS
```

`Directory` is a validated parser view over an `fs.FS`; it is not an immutable candidate snapshot. Capture first copies an upload into owned staging storage, then the product-specific registry package loads and hashes that staged tree.

`Document()` returns an independent value. Callers cannot mutate maps or pointer-backed values held inside `Directory`.

### Registry and installation model

`internal/agentskill` owns only the portable Agent Skills document and directory rules. `internal/protocol` owns the private HTTP contract shared by the client and daemon: route shapes, media types, headers, wire bodies, canonical wire identity conversion, and error-code status mapping. `internal/registry` builds on Agent Skills with ts-skills namespaces, skill IDs, publication IDs, tree digests, and verified publication trees. `internal/tree` validates, durably stages, encodes, and decodes bounded portable publication trees; ZIP is its private v1 transport representation. `internal/client` keeps command interpretation, configuration, registry HTTP access, requirements, locked selections, installation, rollback, and recovery behind one application entry point. `internal/server` keeps candidate IDs, catalog records, SQLite storage, upload handling, Tailnet identity, and HTTP policy together.

The CLI validates every downloaded tree against its requested publication before replacing the managed destination and writing the lock. No project-defined remote or catalog interfaces remain.

## Landed repository layout

```text
.
├── cmd/
│   ├── ts-skills/    # thin client process entry point
│   └── ts-skillsd/   # thin daemon entry point
├── internal/
│   ├── agentskill/   # portable Agent Skills format parsing and validation
│   ├── client/       # client command, registry access, install, and recovery
│   ├── protocol/     # private client/daemon HTTP contract
│   ├── registry/     # ts-skills identity, hashing, and publication verification
│   ├── tree/         # portable tree validation, durable staging, and transport
│   ├── server/       # daemon, catalog, candidate IDs, storage, HTTP, and UI
│   └── version/      # release build version
├── docs/
│   ├── research/
│   └── SPEC.md
├── go.mod
└── go.sum
```

The command packages contain only process entry adapters. Each binary's application logic lives in one deep internal package: `client` or `server`. Cross-binary mechanics live in shared packages only where both applications need the same vocabulary or invariants.

## Resolved implementation choices

The executor plan resolves the prototype choices that this specification first left open:

- strict NFKC namespace grammar;
- one user config and one project digest lock in TOML;
- project installation under `.agents/skills`;
- browser directory manifests for upload and rootless ZIP archives for publication download;
- fixed request, expanded-tree, file, path, and depth limits;
- SQLite facts plus immutable digest-addressed tree directories;
- escaped `SKILL.md`, complete file list, provenance, and digest review;
- Tailnet TLS through `tsnet` with connection-derived actors;
- one physical project writer and convergent stage-and-rename installs.

Deployment-specific Tailnet ACL, tag, auth-key, MagicDNS, and HTTPS changes remain under human control.

## Related research

- [`docs/research/agentskills-spec.md`](research/agentskills-spec.md)
- [`docs/research/agentskills-mgmt-projects.md`](research/agentskills-mgmt-projects.md)
- [`docs/research/go-project-layout.md`](research/go-project-layout.md)
- [`docs/research/new-go-repo-setup.md`](research/new-go-repo-setup.md)
- [`docs/research/tsnet-howto.md`](research/tsnet-howto.md)

The strict unknown-frontmatter parser proposed in `agentskills-spec.md` is superseded for this prototype by the tolerant, source-preserving rule above.
