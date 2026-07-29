# Plan 004: Merge the server side into internal/server

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/consolidation/README.md.
>
> **Drift check (run first)**:
> `jj diff --from 2037ced944acc38456c090ff62e74c9de099318d --to @ -- internal/daemon/ internal/web/ internal/storage/ internal/registry/ internal/tailnet/ internal/upload/`
> 003 lands before this plan, so identity moves and entity flattening are
> expected drift. Anything else unexplained: STOP.

## Status

- **Effort**: L
- **Risk**: MED (large diff, but dominated by file moves and un-exporting;
  the one real design change is unifying the two Catalog types)
- **Depends on**: 002, 003
- **Planned at**: revision `2037ced944acc38456c090ff62e74c9de099318d`, 2026-07-28

## Why this matters

Six packages — `daemon` (753), `web` (851), `storage` (~950), `registry`
(~380 after 003), `tailnet` (185), `upload` (231) — are one program: the
daemon. `daemon` exists largely to import the other five and wire them
together. The walls between them are exactly where the plumbing lives:

- `web.Catalog` (web.go:41) — 9-method interface, one implementation
  (`registry.Catalog`), plus a forwarding adapter in daemon.
- `registry.CatalogStore` (registry/catalog.go:31) — port interface, one
  implementation (`storage.Catalog`).
- `registry.Tree` (registry/catalog.go:16) — one implementation
  (`storage.treeView`).
- Two types named `Catalog` (`registry.Catalog` rule layer over
  `storage.Catalog` persistence layer) for one SQLite database.

tsidp's equivalent — OIDC protocol + funnel + UI + persistence — is one
`server` package with feature files and one `IDPServer` struct. That is
the target shape.

## Target state

```
internal/server/
  daemon.go       Run(Config), RunDev(DevConfig), serving, shutdown  (from daemon)
  tailnet.go      tsnet enrollment, credentials, ActorResolver       (from tailnet)
  catalog.go      unified Catalog: transition rules + SQLite         (registry/catalog.go + storage/catalog.go)
  sqlite.go       schema, pragmas, migrations                        (from storage)
  records.go      row scan/encode                                    (from storage)
  trees.go        tree materialize/verify/serve, treeView            (from storage)
  upload.go       multipart staging                                  (from upload)
  handlers.go     mux, routes, page handlers                         (from web)
  ui.go           templates, static, render helpers                  (from web)
  templates/ static/                                                 (moved embeds)
```

Interfaces deleted (all single-implementation):

- `web.Catalog` → handlers hold `*Catalog` directly.
- `registry.CatalogStore` → gone; rules and persistence are methods on
  one `Catalog` in one package. The rule methods (`Publish` transition
  checks, `SetCurrent` existence check — hardening 010) remain distinct
  *functions*, they just stop crossing an interface to reach SQL.
- `registry.Tree` → concrete `*treeView` (already `fs.FS + io.Closer`).

Interfaces replaced by a func field (two real implementations exist —
tailnet resolver and dev fixed curator):

- `web.CuratorResolver` (web.go:53) →
  `curator func(*http.Request) (Curator, error)` on the handler, golink
  style. Production wires the tailnet resolver's method value; dev wires
  a closure returning the fixed dev curator.

Exported surface of `internal/server` (everything else unexported):
`Run`, `Config`, `RunDev`, `DevConfig`, `Version` passthroughs if any.
Types that 005's e2e test needs are *not* pre-exported — the e2e test
drives the daemon through `RunDev` and HTTP only.

## Steps

1. Mechanical move: relocate the six packages' files into
   `internal/server/` with the layout above; one `package server`
   declaration; fix imports. Tests move alongside
   (`daemon_test.go`, `web_test.go`, `catalog_test.go`,
   `upload_test.go`, `tailnet_test.go` → same names, package `server`).
   `internal/web/network_install_test.go` does **not** move here — it is
   parked unchanged (build-tagged `//go:build ignore` with a `TODO(plan
   005)` marker) because its home is `cmd/ts-skills` and 005 lands it
   there. Commit checkpoint: `go build ./...` green with everything still
   exported.
2. Unify the two Catalogs: `registry.Catalog` (rules) + `storage.Catalog`
   (SQLite, close-phase, digest mutex) become one `Catalog`. The rules
   keep their names and their tests keep their assertions — including the
   concurrent first-publication race test (hardening 010) and the
   close-phase behavior tests (hardening 018). `CatalogStore` and the
   `web.Catalog` mirror + daemon adapter die here.
3. Replace `CuratorResolver` with the func field; delete the interface.
4. Un-export: sweep every identifier in `package server` not reachable
   from `cmd/ts-skillsd` or the package's own tests down to unexported.
   Merge duplicated test helpers (scratch catalogs, fixture trees) into
   one `server_test.go` helper set.
5. Delete the now-empty `internal/{daemon,web,storage,registry,tailnet,upload}`
   directories. `registry`'s flat entity structs and `Curator` land in
   `internal/server/catalog.go` (or `entities.go` if catalog.go passes
   ~600 lines).
6. Re-run the dev-mode golden path (001 §2); diff against baseline —
   byte-identical expectations (this plan changes no behavior).

## Verification

- `go build ./...` && `go test -race ./... -count=1` && `just check`
- `rg 'type\s+\w+\s+interface' internal/server/` → zero project-defined
  interfaces (the func field replaced the last legitimate one).
- `find internal -maxdepth 1 -type d` → `agentskill`, `install`,
  `safetree`, `server`, `version`, plus `client`/`cli`/`config`/
  `protocol` (which 005 dissolves) only.
- `go vet ./...` clean; `rg '"github.com/joshuadavidthomas/ts-skills/internal/server"' internal/` →
  only `cmd/ts-skillsd` imports it (client-side packages must not).
- Golden-path diff recorded in reconciliation entry.

## STOP conditions

- Unifying the Catalogs forces weakening the publication-race test, the
  close-phase tests, or the curator-capability tests — stop; those are
  the behavioral guarantees this effort promised to preserve.
- An import cycle appears that requires moving a type this plan didn't
  plan to move (beyond entities into server) — stop and hand back; ad-hoc
  type relocation under deadline is how the last layout happened.
- The diff for step 2 exceeds what a reviewer can hold (~1,000 changed
  lines beyond pure moves) — split: land step 1 (moves) as its own jj
  commit first, then 2–5. (Step 1 alone is always safe to land.)

## Maintenance notes

The package comment for `internal/server` should state the organizing
rule: *one package, files group features, no interfaces without two
production implementations, exported surface is what `cmd/ts-skillsd`
calls.* New server features get a new file, not a new package.
