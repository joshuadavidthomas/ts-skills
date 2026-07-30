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
- After editing templates or `internal/server/web/tailwind.css`: `just css`

## Architecture

- Module: `github.com/joshuadavidthomas/ts-skills`
- `cmd/ts-skills/` is a thin client process entry point over `internal/client`
- `cmd/ts-skillsd/` is a thin daemon process entry point over `internal/server`
- `internal/client/` owns command interpretation, configuration, registry HTTP access, installation, locking, rollback, and recovery
- `internal/agentskill/` parses and validates the open Agent Skills format
- `internal/protocol/` defines the private HTTP contract shared by the client and daemon
- `internal/registry/` owns ts-skills namespaces, publication identity, tree hashing, and verification
- `internal/tree/` validates, durably stages, encodes, and decodes bounded portable publication trees
- `internal/server/` composes the server runtime and tsnet node
- `internal/server/catalog/` owns candidate identity, publication lifecycle, SQLite storage, and durable catalog trees
- `internal/server/api/` serves the private machine-readable registry protocol
- `internal/server/web/` owns browser upload, curation routes, templates, and static assets
- `internal/version/` carries the build version injected into release binaries

Cross-binary mechanics belong in shared packages only when both binaries use them. Binary-specific application logic stays within its internal package tree; command packages contain process entry adapters only. A constructor parses untrusted input. An interface needs two production implementations.

## Codebase patterns

- Use plain `database/sql` with `modernc.org/sqlite`; do not add an ORM.
- Treat the migration ladder in `internal/server/catalog/sqlite.go` as append-only history. Never edit an old migration; append a new entry.
- Render the UI on the server with `html/template` and Tailwind. Commit generated `internal/server/web/static/style.css` after running `just css`.
- Do not ask the browser for choices the server can infer.
- Address publications by namespace/name and SHA-256 tree digest. Published content is immutable.
- Production identity comes from tsnet peers; there are no application accounts or bearer tokens.
- Dev mode listens only on loopback and maps every request to `dev@localhost`.
- Thread `context.Context` through network, storage, and long-running filesystem work.
- Tests use in-process HTTP servers and `t.TempDir`; they do not require Tailnet credentials.
