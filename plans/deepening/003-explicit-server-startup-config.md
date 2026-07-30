# 003 — Give server startup one explicit configuration owner

> **Executor instructions:** Follow this plan with no hidden session context. If
> a STOP condition occurs, write a handback instead of improvising.

**Source item:** Project-wide design audit: sentinel configuration and duplicated environment knowledge
**Effort index:** `plans/deepening/README.md`
**Planned at:** 2026-07-30, parent `6a886ab8`, bookmark `main`
**Depends on:** 001-web-owns-upload-limits
**Executor target:** routine execution ready: yes
**Source type:** audit
**Audit category:** architecture
**Standards concern:** boundaries and state
**Impact:** Makes mode selection, normalization, and startup reporting discoverable
**Effort:** M
**Risk:** MED; startup and environment behavior are operator-facing contracts
**Confidence:** HIGH; duplicate ownership is directly visible
**Source direction:** Parse environment once into an explicit normalized run configuration

## Purpose

Remove empty-value sentinels from `Run` and stop the command entry point from
reimplementing server configuration solely to log it.

## What Better Means

There is one environment parser that selects production or development mode
and returns a normalized configuration. `Run` accepts only that explicit
configuration. The started notification contains the resolved facts needed for
logging, so `cmd/ts-skillsd` does not read related environment variables again.

## Current-State Evidence

- `internal/server/runtime.go:29-43` treats `Config{}` as “load environment.”
- `runtime.go:51-67` gives a partially empty `DevConfig` a different sentinel
  meaning.
- `cmd/ts-skillsd/main.go:34-50` selects dev mode outside the server.
- `main.go:65-78` duplicates the dev state-directory default and normalization.
- `internal/server/config.go:41-62` already owns the canonical dev default.
- Client end-to-end tests call `RunDev` with explicit settings, so a deliberate
  explicit development construction path must remain available.

## Desired End State

- A named constructor parses all `TS_SKILLSD_*`/auth-key environment input and
  returns a normalized production-or-development run configuration.
- Run mode is explicit in the constructed value, not inferred from empty fields.
- `Run` performs no ambient environment reads.
- Tests can still construct explicit development configuration without
  Tailnet credentials.
- Startup reporting receives address, mode, and resolved state directory from
  the server-owned configuration/runtime.

## Scope

- `cmd/ts-skillsd/main.go` and tests
- `internal/server/config.go`, `runtime.go`, and tests
- Startup callback/result types needed to report normalized facts

## Out of Scope

- Renaming environment variables
- Changing auth-key precedence or tsnet enrollment behavior
- Changing default paths, hostnames, listen addresses, or dev identity
- Adding a configuration framework

## Design Claim

Per `coding-standards/references/boundaries.md`, ambient environment input must
be translated once at the boundary. Per `state.md`, mode-specific data should
belong to an explicit mode rather than an invalid combination of empty fields.

## Architecture Diagnosis

- **Current friction:** Callers must know undocumented zero-value meanings and
  duplicate one normalized fact.
- **Deepening direction:** A constructed run configuration owns parsing,
  defaults, validation, and mode selection.
- **Deletion test:** Deleting command-side dev parsing removes duplicated policy;
  deleting the constructor would spread it back across command and runtime.
- **Locality / leverage claim:** Future environment/default changes touch one
  parser and one set of table tests.
- **Recommendation strength:** Strong
- **ADR conflicts:** None

## Implementation Sequence

### Step 1 — Characterize the environment contract

Table-test production/dev selection, defaults, auth-key exclusions,
normalization, invalid booleans, and enrolled-state protection through the new
constructor boundary.

### Step 2 — Introduce explicit mode configuration

Use a Go-native representation that cannot contain both production and
development settings simultaneously. A single private tagged configuration
with constructors is acceptable; do not add an interface merely to encode two
modes.

Keep an explicit development constructor/path for in-process tests. Remove all
implicit environment reads from execution functions.

### Step 3 — Move startup reporting to normalized facts

Pass a typed started notification containing the actual listener address,
selected mode, and resolved state directory. Delete `devStateDirForLog` and
command-side dev environment parsing.

### Step 4 — Collapse duplicated runtime construction where honest

Share the catalog/web core wiring between production and development only where
their lifecycles are actually identical. Keep Tailnet and loopback listener
adapters distinct.

## Verification

### Automated

- [ ] `go test ./cmd/ts-skillsd ./internal/server -race -count=1`
- [ ] `just test`
- [ ] `just lint`
- [ ] `just vet`

### Evals / Regression Checks

- [ ] `rg -n "os.Getenv|os.LookupEnv" cmd/ts-skillsd` finds no server configuration reads.
- [ ] `rg -n "== \\(Config{}\\)|StateDir == \"\" && .*Listen == \"\"" internal/server` finds no execution sentinel.
- [ ] Existing dev end-to-end tests still construct an explicit loopback server.

### Manual

- [ ] Start dev mode with its default environment and confirm the logged state
  directory equals the one actually opened.

## Autonomy Boundary

Routine execution may introduce private tagged configuration and typed startup
facts. Design review is required to change the public shape of explicit
`RunDev` test construction beyond this plan. Human approval is required for any
environment name, precedence, default, security check, or operator-visible
behavior change.

## Drift Checks

- [ ] Re-read this plan and the effort index.
- [ ] Confirm plan 001 has removed request-body fields from runtime construction.
- [ ] Re-open startup and config tests.
- [ ] Run the narrow tests before editing.

## STOP Conditions

Stop if explicit configuration cannot represent existing production and e2e
callers, normalization currently depends on runtime resources not named here,
or preserving behavior requires multiple environment reads.

## Rejected Approaches

- Change `Run(ctx, Config{})` to `Run(ctx)` while leaving `RunDev` sentinel-based
  — keeps two incompatible ownership rules.
- Move all environment parsing into the command — makes the thin process adapter
  own server policy.
- Introduce a configuration library — no complexity here requires one.

## Standing Policy Updates

Execution functions receive constructed configuration; environment reads stay
in one boundary constructor.

## Executor Notes

Preserve every existing environment variable and error class. The goal is one
owner, not a new configuration language.
