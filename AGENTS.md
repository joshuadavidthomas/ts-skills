## Commands

- `just build`: Build the `ts-skills` client and `ts-skillsd` daemon
- `just test`: Run all tests with the race detector
- `just coverage`: Run race-enabled tests with coverage
- `just fmt`: Format Go code
- `just lint`: Run all pre-commit hooks
- `just vet`: Run Go static analysis
- `just vuln`: Check dependencies for known vulnerabilities
- `just tidy`: Tidy `go.mod` and `go.sum`
- `just check`: Run tests, lint, vet, vulnerability checks, and module tidiness
- `just run -- ARGS`: Run the daemon from source
- `just css`: Rebuild the committed Tailwind CSS

## Validation

Run these after implementing changes:

- Tests: `just test`
- Lint: `just lint`
- Static analysis: `just vet`
- Format: `just fmt`
- After editing templates or `internal/server/tailwind.css`: `just css`

## Architecture

- Module: `github.com/joshuadavidthomas/ts-skills`
- `cmd/ts-skills/` is the client CLI entry point
- `cmd/ts-skillsd/` is the tsnet registry daemon entry point
- `internal/agentskill/` parses and validates the open Agent Skills format
- `internal/registry/` owns ts-skills namespaces, publication identity, tree hashing, and verification
- `internal/safetree/` validates and stages bounded, portable file trees
- `internal/treearchive/` defines the v1 ZIP transport contract shared by the client and daemon
- `internal/server/` owns the daemon, tsnet node, catalog, candidate identity, SQLite storage, browser upload, HTTP API, and server-rendered UI
- `internal/version/` carries the build version injected into release binaries
- `cmd/ts-skills/` owns the CLI, configuration, registry HTTP client, and installer
- `cmd/ts-skillsd/` is a thin daemon entry point over `internal/server`

A package exists only when both binaries import it. A constructor parses untrusted input. An interface needs two production implementations.

## Codebase patterns

- Use plain `database/sql` with `modernc.org/sqlite`; do not add an ORM.
- Treat the migration ladder in `internal/server/sqlite.go` as append-only history. Never edit an old migration; append a new entry.
- Render the UI on the server with `html/template` and Tailwind. Commit generated `internal/server/static/style.css` after running `just css`.
- Do not ask the browser for choices the server can infer.
- Address publications by namespace/name and SHA-256 tree digest. Published content is immutable.
- Production identity comes from tsnet peers; there are no application accounts or bearer tokens.
- Dev mode listens only on loopback and maps every request to `dev@localhost`.
- Thread `context.Context` through network, storage, and long-running filesystem work.
- Tests use in-process HTTP servers and `t.TempDir`; they do not require Tailnet credentials.
