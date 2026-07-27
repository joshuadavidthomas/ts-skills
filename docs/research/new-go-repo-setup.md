# New Go Repository Setup

This document defines a baseline for a new Go repository. It covers source layout, local tools, tests, continuous integration, releases, dependency updates, and repository security.

## Repository layout

Use one Go module unless the project has a clear need for several independently versioned modules. Commit both `go.mod` and `go.sum`. Do not commit `go.work`; developers may create it for local workspace changes.

For an executable, put each entry point under `cmd/` and keep application code under `internal/`:

```text
.
├── cmd/
│   └── <name>/
│       └── main.go
├── internal/
│   └── <package>/
├── docs/
│   └── releasing.md
├── .github/
│   ├── dependabot.yml
│   └── workflows/
│       ├── test.yml
│       ├── lint.yml
│       └── release.yml
├── .gitignore
├── .golangci.yml
├── .pre-commit-config.yaml
├── AGENTS.md
├── CHANGELOG.md
├── Justfile
├── LICENSE
├── README.md
├── SECURITY.md
├── go.mod
├── go.sum
└── mise.toml
```

Keep `main.go` small. It should construct the application, install signal cancellation, call the application entry point, and map errors to exit codes. Put command behavior and domain rules in packages that tests can call without starting a subprocess.

Use narrow package boundaries. Add shared test helpers only after several packages need the same setup. Prefer fakes at network, filesystem, process, clock, and prompt boundaries over broad mocks of internal code.

## Go version and tool versions

Set an exact supported Go version in `go.mod`. GitHub Actions must read the version from that file rather than repeat it in workflow YAML.

Use `mise.toml` to pin developer tools:

```toml
[tools]
golangci-lint = "<version>"
pre-commit = "<version>"
```

Pin tool versions in one place where possible. When an action also requires a tool version, keep the two values in sync through dependency-update automation or a documented update step.

## Local command surface

Provide a `Justfile` with these commands:

| Command | Purpose |
| --- | --- |
| `just build` | Build the executable or packages. |
| `just test` | Run all tests with the race detector. |
| `just coverage` | Run race-enabled tests with coverage. |
| `just fmt` | Format Go source with `gofmt`. |
| `just lint` | Run all pre-commit checks. |
| `just vet` | Run `go vet ./...`. |
| `just tidy` | Run `go mod tidy`. |
| `just check` | Run tests, lint checks, and vet. |
| `just run -- ARGS` | Run an executable from source, when applicable. |

The core commands should use these forms:

```sh
go test ./... -race
go test ./... -race -cover
gofmt -w .
go vet ./...
go mod tidy
```

`just check` should match the checks required before review. Keep each underlying command available on its own so failures are quick to reproduce.

## Formatting and linting

Use pre-commit for fast local checks:

- Remove trailing whitespace.
- Require a final newline.
- Validate TOML and YAML.
- Run `gofmt` on Go files.
- Run `golangci-lint` across the module.
- Validate and audit GitHub Actions workflow files.

Commit `.golangci.yml`. Pinning the binary without recording the enabled linters leaves the lint contract unclear. Keep the config small, document any disabled check, and avoid enabling a rule whose fixes would make the code harder to read.

Run `gofmt`, `golangci-lint`, `go mod tidy`, and `go vet` as separate CI jobs. Separate jobs show which contract failed and allow independent reruns.

## Tests

Place tests beside the packages they cover. Use table tests when several inputs share one behavior, but keep distinct scenarios separate when a table would hide the reason for each case.

Use standard-library test tools first:

- `t.TempDir` for isolated filesystem tests.
- `httptest.Server` for HTTP clients and integrations.
- In-memory output buffers for CLI output.
- Small fake interfaces for external APIs and child processes.
- Test fixtures only when inline data would obscure the behavior under test.

All pull requests must run:

```sh
go test ./... -race -v
```

Add operating-system or Go-version matrices only when the project supports behavior that differs across those boundaries. A portable command-line tool should test each supported operating system before release.

Coverage should guide test work rather than reward a number. Add a coverage upload or threshold only when the team plans to review and maintain it.

## Continuous integration

Create separate `test.yml` and `lint.yml` workflows. Run both for pull requests and pushes to the default branch.

Each workflow must:

- Start with read-only repository permissions.
- Pin every third-party action to a full commit SHA.
- Include the readable action version in a comment.
- Set `persist-credentials: false` on checkout.
- Read the Go version from `go.mod`.
- Cancel stale runs for the same pull request or branch.
- Avoid repository secrets for pull-request validation.

The lint workflow must reject:

- Files reported by `gofmt -l .`.
- `golangci-lint` findings.
- Changes produced by `go mod tidy`.
- `go vet ./...` findings.

Add `govulncheck ./...` as a CI job. It checks whether the module uses known vulnerable code paths. Add gosec or CodeQL only when the project risk and maintenance cost justify another security scanner.

## Dependency updates

Configure Dependabot for both Go modules and GitHub Actions. Run it weekly and use a short cooldown to avoid adopting a new release immediately.

Review dependency changes through the same tests and lint checks as application changes. Do not merge an update only because its version constraint resolves successfully.

Commit `go.sum`. CI must run `go mod tidy` and fail when it changes `go.mod` or `go.sum`.

## Workflow security

Audit workflows before commit and in CI. The checks must:

- Require full commit SHAs for actions.
- Reject unknown action references.
- Restrict actions to an explicit owner allowlist.
- Validate workflow syntax.
- Run a workflow security scanner in its strict mode.

Use `permissions: {}` at workflow scope when jobs need different rights. Grant each job only the rights it needs. Normal test and lint jobs need only `contents: read`.

Never persist checkout credentials unless a step must push through the checkout. Pass release tokens only to the publishing step.

## Releases for executable projects

Library-only modules do not need a binary release workflow. Executable projects should release from version tags through a protected `release` environment.

Before publishing, the release job must:

1. Run race-enabled tests.
2. Run `go vet ./...`.
3. Run `go mod tidy` and confirm that the module files stay unchanged.
4. Build from a clean checkout with full tag history.

Use GoReleaser for ordinary cross-platform command-line tools. A typical release should:

- Build with `CGO_ENABLED=0` when the program does not need C libraries.
- Produce Linux, macOS, and Windows binaries for supported architectures.
- Inject the version from the tag through linker flags.
- Strip debug data from release binaries when debugging symbols are not part of the support plan.
- Package Windows binaries as ZIP files and Unix binaries as tar archives.
- Publish a SHA-256 checksum manifest.
- Create GitHub build-provenance attestations for the published artifacts.

Start the release workflow with no permissions. Grant its release job only:

```yaml
permissions:
  contents: write
  id-token: write
  attestations: write
```

Set `persist-credentials: false` on checkout. Disable cancellation for release jobs so a newer tag cannot stop an active publication.

Document the tag procedure, required environment secrets, produced assets, version injection, and recovery steps in `docs/releasing.md`.

## Repository documentation

Include these files from the first public release:

- `README.md` with installation, use, and development commands.
- `CHANGELOG.md` using a consistent release format and semantic versions.
- `LICENSE` with the chosen project license.
- `SECURITY.md` with supported versions and a private reporting path.
- `AGENTS.md` with commands, validation steps, package boundaries, and project-specific coding rules.

Add `CONTRIBUTING.md`, `CODEOWNERS`, and issue or pull-request templates when the contributor or review process needs them. Empty templates add upkeep without helping a small repository.

## Recommended validation gate

A change is ready for review when these commands pass:

```sh
just test
just lint
just vet
```

CI remains the source of truth. It must also check module tidiness, workflow security, and known Go vulnerabilities.
