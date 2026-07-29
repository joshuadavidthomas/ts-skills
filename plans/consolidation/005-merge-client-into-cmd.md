# Plan 005: Merge the client side into cmd/ts-skills; dissolve protocol

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/consolidation/README.md.
>
> **Drift check (run first)**:
> `jj diff --from 2037ced944acc38456c090ff62e74c9de099318d --to @ -- internal/cli/ internal/client/ internal/config/ internal/install/ internal/protocol/ cmd/ts-skills/`
> 002 (install rewrite) and 003 (identity moves) land before this plan;
> that drift is expected. Anything else unexplained: STOP.

## Status

- **Effort**: L
- **Risk**: MED (mostly moves; two real changes — deleting
  `install.Remote` and relocating the archive contract)
- **Depends on**: 003, 004
- **Planned at**: revision `2037ced944acc38456c090ff62e74c9de099318d`, 2026-07-28

## Why this matters

Five packages exist to build one CLI binary: `cli` (224), `client` (480 +
131 zip preflight), `config` (~60), `install` (~700 after 002), and half
of `protocol` (104, shared with web). The boundaries generate plumbing:

- `install.Remote` (model.go:71) — one-method interface, one production
  implementation (`client.Remote`), existing so `install` can avoid
  importing `client` while `cli` wires them together.
- `config` is one TOML file read, in its own package, importing `client`
  for the `Origin` type.
- `cli.Run` still carries an unused `stdin io.Reader` (deferred item in
  the hardening README).

golink's entire CLI story is `cmd/golink/main.go` (16 lines) over one
package; tclip's client is one 89-line file. The idiom for "code only one
binary uses" is: it lives in that binary's main package, as feature files.

## Target state

```
cmd/ts-skills/            package main
  main.go                 exit handling (exists; stays thin)
  cli.go                  subcommand dispatch, flags        (from internal/cli, stdin param dropped)
  client.go               HTTP client, Origin, zip decode   (from internal/client + zip_preflight.go)
  config.go               TOML config load                  (from internal/config)
  installer.go            install/restore flow              (from internal/install, post-002)
  lock.go  project.go  fsutil.go  paths…                    (remaining install files)
  e2e_test.go             network install e2e               (from internal/web/network_install_test.go)
```

`internal/agentskill/archive.go` gains the v1 archive contract from
`protocol` — it is genuinely shared vocabulary (server encodes, client
decodes): `TreeArchiveZIPMethod`, entry-overhead allowance, and the size
ceiling. **In-plan decision on `TreeArchiveCeiling`:** if server limits
are fixed in practice (one `safetree.Limits` value used by daemon and
client), replace the 60-line overflow-proof derivation with one shared
constant and a test asserting the encoder stays under it; keep the
derivation only if limits are genuinely configurable at runtime. Record
the outcome in the reconciliation entry.

Deleted outright:

- `install.Remote` — `Installer` holds the concrete client. If 002 left
  `Installer` as a struct over a fetch func, collapse accordingly.
- `internal/protocol` — contract to `agentskill`, error sentinels
  (`ErrInvalidRequest`, `ErrInternal`, hardening 003/013) go with the
  client code that maps them (`client.go`), since the server side names
  its own statuses.
- `cli.Run`'s `stdin` parameter.
- `internal/{cli,client,config,install,protocol}` directories.

Kept intact: `client.Origin` and config parsing (untrusted input — the
rule from 003), CLI diagnostics shape (hardening 006: exit once at main,
`AlreadyReported` becomes unexported), archive rejection classes
(hardening 013: rejections surface through `Fetch`).

## Steps

1. Mechanical move into `cmd/ts-skills` as `package main` feature files;
   tests move as main-package tests (allowed and idiomatic; golink tests
   its root package the same way). Commit checkpoint: build green.
2. Delete `install.Remote`; wire `Installer` to the concrete client.
   Drop `stdin` from the (now-internal) run function.
3. Move the archive contract to `agentskill/archive.go`; make the
   ceiling decision; update the two consumers (server handlers from 004,
   client decode). Delete `internal/protocol`.
4. Un-park `network_install_test.go` (marked `TODO(plan 005)` by 004)
   into `cmd/ts-skills/e2e_test.go`: main package test that boots
   `server.RunDev` on an ephemeral loopback port with scratch state,
   runs install/restore through the real client code, and asserts the
   installed tree digest. This is now the repo's one true end-to-end
   test; keep it honest (real HTTP, real filesystem, no seams).
5. Un-export sweep within the main package (everything; it exports
   nothing by definition) and helper de-duplication.
6. Re-run dev-mode golden path (001 §2) with the *built binary*; diff
   against baseline: lock hash, tree digest, failure diagnostics, help
   text (`ts-skills -h`, subcommand `-h`) — byte-identical except where
   002 already recorded the `ErrRecovered` diagnostic removal.

## Verification

- `go build ./...` && `go test -race ./... -count=1` && `just check`
- `just build` and `./ts-skills install …` golden path against a dev
  daemon (001 hygiene rules: scratch state, ephemeral port, kill by pid,
  `ss -tlnp | grep :8080` empty at the end).
- `find internal -maxdepth 1 -type d` → exactly `agentskill`, `safetree`,
  `server`, `version`.
- `rg 'type\s+\w+\s+interface' cmd/ internal/` → zero project-defined
  interfaces repo-wide.
- `rg '"github.com/joshuadavidthomas/ts-skills/internal/server"' cmd/ts-skills/`
  → only `e2e_test.go` (production client code must reach the server via
  HTTP only).
- Package/line metrics recorded for 006's table.

## STOP conditions

- The e2e test cannot express something the old `network_install_test.go`
  asserted (e.g. a capability-denial path that needs the tailnet
  resolver) — stop and record exactly which assertion is stranded; the
  answer may be a second focused test inside `internal/server`, but that
  is a handback decision.
- The ceiling decision (step 3) reveals the client and daemon actually
  use *different* `safetree.Limits` values today — stop; that is a
  latent contract bug, not something to paper over while moving files.
- Any baseline diff in step 6 beyond the sanctioned `ErrRecovered` line.

## Maintenance notes

`cmd/ts-skills` exports nothing and imports `agentskill`, `safetree`,
`version`, and (tests only) `server`. If a second consumer of the client
code ever appears (a library use case, a TUI), *that* is the moment an
`internal/client` package gets re-extracted — with the consumer in hand,
not speculatively.
