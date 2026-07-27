# Agent skill management projects

Research snapshot: 2026-07-27

## Scope

This report compares command-line tools and registries that install or manage Agent Skills: directories built around `SKILL.md`, often with scripts, references, assets, hooks, subagents, or MCP configuration.

It covers:

- Vercel `skills` and skills.sh
- GitHub CLI `gh skill`
- Rulesync
- Tessl
- iFlytek SkillHub
- Sentry dotagents
- OpenSkills
- SkillKit
- SkillHub.club
- Anthropic's native marketplace and adjacent authoring tools

General MCP registries fall outside the scope unless a project also manages Agent Skills.

## Findings

No project yet combines all of these controls:

- runtime isolation
- immutable, signed skill artifacts
- a lockfile that controls restore and verifies downloaded bytes
- verified publisher identity
- blocking security scans
- public moderation and revocation
- safe, transactional updates

The strongest choices depend on the job:

- Tessl has the broadest hosted governance controls.
- Rulesync has the best open cross-agent workflow for reviewed internal sources.
- `gh skill` is the cleanest GitHub-native installer.
- iFlytek SkillHub is the best self-hosted registry base.
- Vercel `skills` has the largest dedicated public ecosystem and the widest agent support.
- Sentry dotagents fits teams that want one reviewed project configuration projected into several agents.

Treat every skill as executable supply-chain input. A Markdown-only skill can tell an agent to run commands, read secrets, change files, or download later payloads.

## Popularity

These figures measure different things. GitHub stars show interest. npm counts include `npx`, CI, upgrades, retries, and cache misses. Catalog counts may include indexed copies rather than reviewed packages. None is a count of active users.

| Project | Adoption signal at the research date | Qualification |
|---|---:|---|
| [Vercel `skills`](https://github.com/vercel-labs/skills) | 27,350 stars; 82.8 million npm fetches since January 2026 | The strongest dedicated ecosystem signal; repeated `npx` use inflates downloads |
| [`gh skill`](https://cli.github.com/manual/gh_skill) | Distributed with GitHub CLI; `cli/cli` had about 45,000 stars | GitHub publishes no skill-specific usage count |
| [OpenSkills](https://github.com/numman-ali/openskills) | 10,640 stars; 29,872 monthly npm fetches | Large open-source mindshare despite a weak security model |
| [iFlytek SkillHub](https://github.com/iflytek/skillhub) | 4,919 stars; 2,698 monthly CLI fetches; public API reported 13,998 skills | Public catalog has much Chinese-language content |
| [SkillKit](https://github.com/rohitg00/skillkit) | About 1,400 stars; 1,473 monthly npm fetches | Its catalog aggregates other sources, including skills.sh |
| [Rulesync](https://github.com/dyoshikawa/rulesync) | 1,261 stars; 926,426 monthly npm fetches | Usage includes its wider rules and agent-config features |
| [Sentry dotagents](https://github.com/getsentry/dotagents) | 214 stars; 28,617 monthly npm fetches | npm traffic is a stronger signal than repository stars |
| [Tessl](https://docs.tessl.io/) | 19,047 monthly CLI fetches; public CLI shell repository had 70 stars | The package-manager implementation is closed source |
| [SkillHub.club](https://skillhub.club/) | 1,672 monthly CLI fetches; site claimed 120,000 skills | Public source did not match the current npm package closely enough for a full audit |

## Feature comparison

| Tool | Discovery | Pinning and restore | Security policy | Agent coverage | Private or self-hosted use |
|---|---|---|---|---:|---|
| Tessl | Hosted semantic registry and indexed Git skills | Versioned packages, Git commit pins, compatible updates, rollback | Snyk scans, severity blocks, source rules, release-age rules, inventory, audit | 6 documented production targets plus several preview targets | Private workspaces; hosted service |
| Rulesync | No central catalog | Commit locks, artifact hashes, npm SRI, `--frozen` | Strong extraction controls; no installed-skill scanner or moderation | 36 AI-tool targets plus Agent Skills and `AGENTS.md` | Private Git and npm sources |
| `gh skill` | GitHub Code Search | Full commit pins and recorded tree SHA; no frozen restore | Safe API fetches and previews; no content scanner or registry moderation | 47 targets | Public and private GitHub repositories |
| iFlytek SkillHub | Hosted or self-hosted registry | Semantic versions and local inventory; weak payload integrity | RBAC, review, audit, reports, optional Cisco scanner | 14 targets | Yes, including self-hosting |
| Vercel `skills` | skills.sh and Git/HTTPS discovery | Project and global lock hashes; restore does not strictly verify them | External audit display, but findings never block installation | 74 named adapters plus universal mode | Private Git, local paths, and well-known HTTPS sources |
| Sentry dotagents | Decentralized Git, local, and well-known discovery | Resolved commit recorded but informational | Source allowlists, path checks, minimum release age; no scanner | Claude Code, Cursor, Codex, Copilot, OpenCode, and Pi | Good fit for private repositories |
| SkillKit | Aggregated marketplace and Git hosts | Truncated `SKILL.md` checksum; no immutable source pin | Default-on static scanner; weak artifact controls | 47 concrete targets plus universal mode | Git hosts and local sources |
| OpenSkills | Git and local sources | No immutable pins or content lock | No scanner, moderation, or runtime controls | Universal loader plus Claude paths | Private Git and local paths |

## Security assessment

### Method

The score measures controls visible in source or public documentation.

| Area | Weight | Full-credit behavior |
|---|---:|---|
| Execution containment | 15 | Installation never runs package content; hooks and MCP commands need separate consent; runtime sandbox and permissions |
| Filesystem safety | 20 | Destination and archive-path checks, realpath confinement, link rejection, resource limits, atomic writes |
| Integrity and reproducibility | 20 | Immutable version or commit, lock-driven restore, verified digest and signature, rollback |
| Provenance and governance | 20 | Publisher identity, namespace ownership, source policy, scans, moderation, reports, quarantine, revocation |
| Conflict and update safety | 10 | Refuse overwrite by default, preflight, transaction or backup, update diff, stale-file cleanup |
| Privacy and least privilege | 10 | Project scope by default, protected credentials, narrow telemetry, clear opt-out |
| CLI dependency hygiene | 5 | Locked and signed CLI releases, no shell interpolation, controlled self-updates |

Scores are comparative research notes, not certifications. A scanner can miss a logic bomb, and a trusted publisher can be compromised.

### Ranking

| Rank | Tool | Score | Main reason |
|---:|---|---:|---|
| 1= | Tessl 0.93.0 | 66/100 | Strong registry policy and governance, offset by a closed client, telemetry, and no documented artifact signatures |
| 1= | Rulesync 15.1.0 | 66/100 | Strong path, archive, lock, and multi-agent controls, but some integrity failures only warn and there is no skill scanner |
| 3 | GitHub CLI 2.96.0 `gh skill` | 65/100 | Clean API-based fetching, commit pins, preview, and update rollback, but no scanner or trusted catalog |
| 4= | iFlytek SkillHub 0.1.9 | 60/100 | Inspectable registry governance and staged extraction, but weak payload integrity and a possible slug-confinement flaw |
| 4= | Vercel `skills` 1.5.20 | 60/100 | Good reach and path/archive controls, but advisory scans, weak frozen restore, and source-symlink risk |
| 6 | Sentry dotagents 1.19.0 | 47/100 | Useful source trust rules, but an informational lockfile and no scan, signature, or moderation layer |
| 7 | SkillKit 1.24.0 | 40/100 | Useful static scanner, offset by npm lifecycle execution, mutable sources, and destructive updates |
| 8 | OpenSkills 1.5.0 | 26/100 | Installer-level shell injection risk plus no pins, integrity checks, scanning, or moderation |

SkillHub.club remains unscored because the public repository trailed the npm package and did not expose enough of the current security implementation.

## Project notes

### Vercel `skills` and skills.sh

[skills.sh](https://skills.sh/) is a telemetry-ranked directory. The [`skills`](https://github.com/vercel-labs/skills) npm CLI installs the externally hosted content.

The CLI supports GitHub, GitLab, generic Git, SSH, HTTPS well-known indexes, local directories, project or global scope, update, remove, temporary use, and lock files. It defines 74 named agent adapters plus a universal `.agents/skills` target.

Security strengths:

- path containment and install-name sanitization
- terminal-control stripping
- archive traversal, link, size, and file-count checks for well-known packages
- SHA-256 checks for well-known v0.2 artifacts
- signed CLI releases and npm provenance
- project scope by default

Security limits:

- audit requests time out quickly and never block installation
- a package can remain listed despite a critical result from one scanner
- ordinary Git sources have no artifact signature
- lock hashes detect change but do not enforce a frozen restore
- copy mode can dereference source symlinks
- branch and tag sources remain mutable
- telemetry defaults on and includes source, skill, agent, scope, search, remove, and update data

Set `DO_NOT_TRACK=1` or `DISABLE_TELEMETRY=1` to disable telemetry.

Primary sources:

- [`vercel-labs/skills`](https://github.com/vercel-labs/skills)
- [skills.sh CLI documentation](https://skills.sh/docs/cli)
- [skills.sh API](https://skills.sh/docs/api)
- [skills.sh privacy policy](https://skills.sh/privacy)

### GitHub CLI `gh skill`

[`gh skill`](https://cli.github.com/manual/gh_skill) searches public GitHub `SKILL.md` files and installs from GitHub repositories. It supports search, preview, install, update, and publish across 47 agent targets.

The remote installer enumerates Git trees and downloads blobs through GitHub APIs. It does not extract arbitrary source archives or invoke npm. Remote files are written without executable mode. Local installs skip symlinks.

Use a full commit SHA with `--pin`. The `skill@VERSION` form selects that ref for the first install but does not keep later updates pinned. GitHub labels search results as unverified and applies no malicious-content scan or moderation gate.

The update path supports dry runs and per-skill swap-and-rollback, but dry run reports tree hashes rather than a file-level content diff. Lock metadata records provenance but cannot drive a frozen reinstall.

Primary sources:

- [`gh skill` manual](https://cli.github.com/manual/gh_skill)
- [GitHub skill guidance](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/add-skills)
- [`cli/cli` skill implementation](https://github.com/cli/cli/tree/trunk/pkg/cmd/skills)

### Rulesync

[Rulesync](https://github.com/dyoshikawa/rulesync) manages rules, ignore files, MCP configuration, commands, subagents, skills, hooks, permissions, and checks across more than 36 AI tools. It can import from GitHub, arbitrary Git repositories, local repositories, and npm-compatible registries.

Its declarative source model supports exact commits, artifact hashes, npm SRI, committed lock data, `--frozen`, and deliberate updates. It fetches npm packages without running lifecycle scripts. Archive extraction rejects traversal, links, devices, and oversized inputs.

The lock workflow has gaps:

- cached skills may be trusted based on directory existence rather than rehashing
- some artifact mismatches warn after writing instead of failing closed
- frozen mode does not bind every source-selection field
- one GitHub-compatible mode re-resolves mutable refs instead of retrieving the locked commit

Rulesync has no package scanner, publisher verification, or public moderation layer. Use it for reviewed internal sources, commit its lock files, pin full commit SHAs, and inspect generated hooks and MCP entries.

Primary sources:

- [declarative sources](https://github.com/dyoshikawa/rulesync/blob/main/docs/guide/declarative-sources.md)
- [supported tools](https://github.com/dyoshikawa/rulesync#supported-tools)

### Tessl

[Tessl](https://docs.tessl.io/) is a hosted registry and closed-source package-manager CLI. Its install unit is a versioned plugin that may contain skills, rules, commands, hooks, MCP servers, and evaluation scenarios.

Its governance features include:

- public and private workspaces
- semantic versions and compatible updates
- Git source pins
- rollback
- Snyk-powered scans
- configurable warning and hard-block thresholds
- Git host and organization allowlists
- minimum release and commit age
- inventory, audit history, RBAC, moderation states, and public-package approval

The main gaps are hard to inspect because the client source is absent. Public docs do not establish package-payload signatures, client-verified artifact digests, safe archive handling, symlink treatment, or atomic writes. Plugins can add hooks and local MCP commands, and no local runtime sandbox contains them.

Usage sharing defaults on. Tessl says collected data may include conversations, code snippets, and file contents. Disable it with:

```bash
tessl config set shareUsageData false
```

Primary sources:

- [package-manager tutorial](https://docs.tessl.io/tutorials/using-tessl-as-a-package-manager)
- [insecure-skill policy](https://docs.tessl.io/tutorials/protecting-against-insecure-skills)
- [CLI reference](https://docs.tessl.io/reference/cli-commands)
- [usage-data policy](https://docs.tessl.io/legal/sharing-usage-data)

### iFlytek SkillHub

[iFlytek SkillHub](https://github.com/iflytek/skillhub) offers a public registry, a self-hosted server, and the `@astron-team/skillhub` CLI. It supports namespaces, semantic versions, private visibility, RBAC, review gates, audit logs, scoped tokens, S3-compatible storage, and 14 agent profiles.

The open implementation includes bounded ZIP extraction, traversal checks, staged installation, refusal to overwrite without `--force`, and optional Cisco skill-scanner integration.

Its local inventory is not a reproducible lockfile. The CLI does not verify the downloaded payload against a signature or digest. Scanner enforcement depends on server policy. The reviewed name parser and destination join may permit a crafted registry slug to escape the expected install root when combined with forced replacement.

Primary sources:

- [`iflytek/skillhub`](https://github.com/iflytek/skillhub)
- [CLI guide](https://github.com/iflytek/skillhub/blob/main/docs/skillhub/en/guide/cli.md)
- [scanner integration](https://github.com/iflytek/skillhub/tree/main/scanner)

### Sentry dotagents

Sentry's [`@sentry/dotagents`](https://github.com/getsentry/dotagents) treats agent configuration as project dependencies.

#### Model

- `agents.toml` is the checked-in source of truth.
- `.agents/` holds installed skills and generated state.
- `agents.lock` records resolved commits and installed components.
- Agent-specific files are generated or symlinked from the shared state.

It supports skills, subagents, MCP servers, and hooks across Claude Code, Cursor, Codex, GitHub Copilot, OpenCode, and Pi. Support varies by feature: Pi reads `.agents/skills` directly, while other targets receive generated MCP, hook, or subagent configuration.

#### Distribution

Dotagents has no central catalog, publisher accounts, ranking, or review queue. It discovers dependencies from:

- GitHub shorthand such as `owner/repo@ref`
- GitHub and GitLab HTTPS or SSH URLs
- arbitrary Git URLs
- project-local paths
- HTTPS `/.well-known/skills/index.json`

Git discovery searches common roots such as `skills/`, `.agents/skills/`, `.claude/skills/`, and Claude marketplace layouts. Skills follow the Agent Skills directory shape with `SKILL.md` and optional bundled files.

#### Workflow

```bash
dotagents init
dotagents add owner/repo skill-name
dotagents install
dotagents list
dotagents remove skill-name
dotagents doctor
```

There is no separate update command. `install` fetches or refreshes sources, copies selected content into `.agents/`, writes lock state, and regenerates target-agent files. `sync` repairs local state and can adopt existing files. `doctor` checks links, ignored state, installed dependencies, and obsolete fields.

#### Trust and security

Dotagents can restrict sources by GitHub organization, exact repository, Git host or path prefix, and well-known host. It validates trust before network access. It also checks cache keys, downloaded paths, local source paths, Git arguments, and source names. A minimum-release-age option delays newly published Git changes.

The limits define its safe use:

- an absent `[trust]` section allows every source
- `agents.lock` is normally gitignored
- `resolved_commit` is informational rather than authoritative
- normal installation refreshes configured sources
- `install --frozen` is documented as a warned no-op
- tags and branches may move
- there are no skill signatures, payload hashes, malware scans, publisher verification, moderation, or revocation
- hooks and local MCP commands can run later under the agent's permissions
- well-known downloads lack whole-package integrity and may cache a partly downloaded package

Dotagents fits teams that own or review their source repositories and send `agents.toml` through ordinary code review. Require a restrictive `[trust]` section and pin full commit SHAs. Do not treat its lockfile as proof of reproducible bytes.

Primary sources:

- [README](https://github.com/getsentry/dotagents/blob/main/README.md)
- [CLI reference](https://github.com/getsentry/dotagents/blob/main/docs/src/content/docs/cli.mdx)
- [security model](https://github.com/getsentry/dotagents/blob/main/docs/src/content/docs/security.mdx)
- [specification](https://github.com/getsentry/dotagents/blob/main/specs/SPEC.md)

The older [`@iannuttall/dotagents`](https://github.com/iannuttall/dotagents) is a separate local migration and symlink tool. It has backups and undo, but no remote package management, lockfile, registry, or provenance model.

### OpenSkills

[OpenSkills](https://github.com/numman-ali/openskills) installs from Git or local paths and can generate an `AGENTS.md` block that tells agents how to load skills. Its small interface and universal loading pattern helped adoption.

Version 1.5.0 interpolates the user-provided repository URL into a shell command used by `execSync`. Embedded quotes or shell metacharacters can produce command injection during installation. Updates repeat shell-based Git construction from stored metadata.

It also lacks immutable commit pins, lock hashes, signatures, scanning, allowlists, moderation, and revocation. Recursive copies may dereference symlinks, and updates can leave stale files. Do not use this version with untrusted source strings.

Primary source: [`numman-ali/openskills`](https://github.com/numman-ali/openskills).

### SkillKit

[SkillKit](https://github.com/rohitg00/skillkit) supports GitHub, GitLab, Bitbucket, local paths, well-known sources, skills.sh, translation among agent formats, manifests, recommendations, a TUI, and 47 concrete agent targets.

Its scanner checks more than 46 static patterns for prompt injection, command execution, exfiltration, secrets, Unicode concealment, and malformed manifests. Scanning defaults on, and high-severity findings block installation unless the user passes `--force` or `--no-scan`.

The scanner is not containment. A skill with `package.json` causes the installer to run `npm install --omit=dev`, which can execute lifecycle scripts. Git sources normally follow mutable branches. The lock records only a 16-hex-character checksum of `SKILL.md`, omitting bundled files. Updates delete the installed directory before replacement and have no backup or rollback.

The advertised catalog size aggregates external sources. At the research date, its checked-in catalog reported 15,120 indexed entries rather than hundreds of thousands of independently hosted or reviewed packages.

Primary sources:

- [`rohitg00/skillkit`](https://github.com/rohitg00/skillkit)
- [security documentation](https://github.com/rohitg00/skillkit/blob/main/docs/fumadocs/content/docs/security.mdx)

### SkillHub.club

[SkillHub.club](https://skillhub.club/) exposes a hosted catalog, security grades, search, create, push, pull, publish, and installation for several coding agents.

The current npm documentation describes a security gate, explicit risk acceptance, OAuth, mode-`0600` tokens, and telemetry opt-out. The public CLI repository lagged behind the npm release and showed an installer that copied only `SKILL.md`, not bundled scripts, assets, or references. The mismatch prevents a sound audit of the shipped package. Test and pin the exact npm version before considering it for controlled use.

## Adjacent projects

### Anthropic skills

[Anthropic's skills repository](https://github.com/anthropics/skills) is the authoritative source for Anthropic's examples. Claude Code can install its collections through the native plugin marketplace. Raw skill directories follow the open Agent Skills format and can be installed elsewhere with other tools.

The native marketplace path is Claude-only, and the repository's star count measures interest in the examples rather than marketplace installation.

### Agent Skills reference tools

[`skills-ref`](https://github.com/agentskills/agentskills/tree/main/skills-ref) validates skill structure, reads properties, and renders prompt input. Its own documentation calls it a reference implementation rather than a production package manager.

### Microsoft Waza

[Waza](https://github.com/microsoft/waza) scaffolds, validates, benchmarks, and adversarially tests skills. It does not install from or operate a skill registry.

### Smithery

Smithery removed its independent skill installation command in 2026. Its skill pages now point users to Vercel's `skills` installer, so it is a discovery surface rather than a separate management system.

## Threat evidence

Snyk's [ToxicSkills study](https://snyk.io/blog/toxicskills-malicious-ai-agent-skills-clawhub/) examined 3,984 skills sampled from ClawHub and skills.sh. It reported:

- 76 confirmed malicious payloads
- 534 skills, or 13.4%, with critical findings
- 1,467 skills, or 36.82%, with at least one security issue

The findings included prompt injection, credential theft, unsafe downloads, obfuscation, destructive commands, reverse shells, and staged malware. These figures depend on Snyk's sample and scanner methodology; they do not state the current prevalence in every registry.

Related research:

- [ClawHub malicious campaign](https://snyk.io/articles/clawdhub-malicious-campaign-ai-agent-skills/)
- [Credential leakage in skills](https://snyk.io/blog/openclaw-skills-credential-leaks-research/)
- [Snyk Agent Scan](https://github.com/invariantlabs-ai/mcp-scan)

## Safe-use baseline

For registry and CLI authors:

1. Resolve every source ref to a full commit before installation.
2. Publish and verify a file manifest, artifact digest, source commit, scanner version, and signing identity.
3. Make versions immutable and support revocation without rewriting history.
4. Reject absolute paths, `..`, NULs, device files, hardlinks, symlinks, and case-collision tricks.
5. Bound compressed size, expanded size, file count, depth, and per-file size.
6. Stage and validate the complete tree, then replace the destination atomically.
7. Separate copying a skill from enabling hooks, MCP commands, binaries, or lifecycle scripts.
8. Refuse overwrite by default and show added, changed, removed, executable, hook, and MCP files.
9. Make frozen restore verify the downloaded bytes against committed lock data.
10. Require publisher identity, namespace ownership, scanning, reports, quarantine, and revocation for public registries.
11. Keep project scope as the default and require an explicit flag for user-wide installation.
12. Document every telemetry field and honor `DO_NOT_TRACK`.

For users and organizations:

1. Pin full commit SHAs rather than branches or tags.
2. Install into the project rather than a global agent directory.
3. Inspect every file, including scripts, hooks, MCP declarations, binaries, and remote URLs.
4. Make security scans fail closed at the chosen severity.
5. Review the file-level diff before every update.
6. Run agents in disposable workspaces or containers.
7. Mount only the task repository as writable.
8. Inject task-specific secrets instead of inheriting the full environment.
9. Deny outbound network access by default and allow named hosts when needed.
10. Ask before package installation, binary execution, secret access, destructive commands, hooks, or MCP startup.
