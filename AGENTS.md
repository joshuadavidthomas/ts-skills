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
- After editing templates or `internal/web/tailwind.css`: `just css`

## Architecture

- Module: `github.com/joshuadavidthomas/ts-skills`
- `cmd/ts-skills/` is the client CLI entry point
- `cmd/ts-skillsd/` is the tsnet registry daemon entry point
- `internal/agentskill/` parses, validates, and hashes Agent Skill directories
- `internal/cli/` owns client commands, flags, output, and error mapping
- `internal/client/` implements the typed HTTP registry client and verifies downloaded trees
- `internal/config/` loads and validates client TOML configuration
- `internal/daemon/` configures and runs the production tsnet server and loopback dev server
- `internal/install/` installs and restores locked skills with transactional project updates
- `internal/protocol/` defines the versioned HTTP wire types, headers, and error codes
- `internal/registry/` owns skill identities, candidates, immutable publications, and catalog rules
- `internal/safetree/` validates and stages bounded, portable file trees
- `internal/storage/` persists registry metadata in SQLite and trees in digest-addressed directories
- `internal/tailnet/` runs the embedded Tailscale node and maps peers to registry actors
- `internal/upload/` validates and stages browser directory uploads
- `internal/version/` carries the build version injected into release binaries
- `internal/web/` owns the HTTP API and server-rendered management UI

## Codebase patterns

- Use plain `database/sql` with `modernc.org/sqlite`; do not add an ORM.
- Treat the migration ladder in `internal/storage/sqlite.go` as append-only history. Never edit an old migration; append a new entry.
- Render the UI on the server with `html/template` and Tailwind. Commit generated `internal/web/static/style.css` after running `just css`.
- Do not ask the browser for choices the server can infer.
- Address publications by namespace/name and SHA-256 tree digest. Published content is immutable.
- Production identity comes from tsnet peers; there are no application accounts or bearer tokens.
- Dev mode listens only on loopback and maps every request to `dev@localhost`.
- Thread `context.Context` through network, storage, and long-running filesystem work.
- Tests use in-process HTTP servers and `t.TempDir`; they do not require Tailnet credentials.
