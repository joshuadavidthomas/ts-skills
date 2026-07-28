# Plan 020: One refined registry-origin value from config to client

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/config/ internal/client/ internal/cli/`
> Plans 003, 006 legitimately change in-scope files — reconcile against
> landed work; on an unexpected mismatch, treat it as a STOP condition.

## Status

- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (sequence after 014 per the index order)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

"Registry origin" is validated twice and expressed nowhere. `config.Load`
applies strict origin rules (HTTPS, or loopback HTTP; host required; no
userinfo/query/fragment/path) and stores the result as a plain mutable
`*url.URL` — then `client.NewRemote` can't see that proof and reruns an
almost identical validator (`internal/client/client.go:45-49, 468-495`).
Two copies of one policy that can drift apart, and the refined facts live
in a type (`url.URL`) that also permits everything the rules forbid.
Boundary data should be refined once at the edge and move inward as a value
that can't forget what it is.

## Current state

```go
// internal/config/config.go:13-17
type Config struct {
	Registry *url.URL
}
```

`config.validateOrigin` (config.go, in `Load` path around lines 52-59 +
the quoted rules below) enforces:

```go
if source == nil || (source.Scheme != "https" && source.Scheme != "http") { ... }
if source.Host == "" || source.Hostname() == "" || source.Opaque != "" { ... }
if source.User != nil || source.RawQuery != "" || source.ForceQuery || source.Fragment != "" { ... }
if (source.Path != "" && source.Path != "/") || ... { ... }
if source.Scheme == "http" && !isLoopbackHost(source.Hostname()) { ... }
```

The client's duplicate check lives at `internal/client/client.go:468-495`
(same policy, second copy; `NewRemote` also dereferences the URL pointer,
client.go:45-49).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/config/ ./internal/client/ ./internal/cli/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/config/config.go` (+ tests)
- `internal/client/client.go` (+ tests) — drop the duplicate validator; accept the origin type
- `internal/cli/cli.go` — passes the new type through (mechanical)

**Out of scope**:
- TOML file shape — `registry = "https://..."` string unchanged.
- Daemon config — it doesn't consume the client Config (check; if
  `daemon.go` builds a client, adapt the call site only).
- Where the origin type lives — recommendation: `internal/client` owns it
  (it's the client's requirement; config just produces it) OR a small new
  home if importing client from config is inverted (config is a leaf;
  client imports agentskill/install/protocol/registry/safetree — config→client
  import is fine and acyclic). Decide per the import direction that keeps
  config leaf-like.

## Steps

### Step 1: Define the refined type

One origin value — opaque fields, value semantics (no pointer
mutability):

```go
// Origin is a validated registry base: HTTPS, or loopback HTTP; host only —
// no path, credentials, query, or fragment.
type Origin struct{ /* private */ }
func ParseOrigin(string) (Origin, error)
func (o Origin) URL() *url.URL   // fresh copy for net/url consumers
func (o Origin) String() string
```

The parser implements exactly today's rules (both copies agree today —
verify by diffing the two validators before writing).

**Verify**: `go build ./internal/config/ ./internal/client/` → exit 0.

### Step 2: One validator

`config.Load` returns `Config{Registry client.Origin}`-shaped value via
`ParseOrigin`; `client.NewRemote` accepts `Origin` and deletes its own
validator (client.go:468-495) and nil-URL handling. `cli` passes it through.

**Verify**: `go test -race ./internal/config/ ./internal/client/ ./internal/cli/ -count=1` → all pass.

### Step 3: Full gate

**Verify**: `rg "validateOrigin|isLoopbackHost" internal/` → exactly one
implementation site; `just check` → exit 0.

## Test plan

- Parser table: all current rejection classes both sides used to test —
  scheme, host, userinfo, query, fragment, path, loopback-only HTTP —
  move/merge into one table where the type lives (exemplar: the config and
  client validator tests being replaced).
- Conformance: config.Load output feeds `client.NewRemote` directly in a
  test (the two-places-can't-drift test).

**Verify**: `go test -race ./internal/config/ ./internal/client/ ./internal/cli/ -count=1` → all pass.

## Done criteria

- [ ] Origin policy exists once; config and client share the type
- [ ] `NewRemote` can't receive a raw `*url.URL`
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The two validators actually DISAGREE today on some input — that
  difference is a behavioral decision (which rule wins?); hand back with
  the concrete divergent input.
- A caller mutates the exported `*url.URL` today (grep for `.Registry.Path =`
  style writes) — remove the mutation as part of the plan only if trivial;
  otherwise handback.
- config→client import direction turns out wrong for layering (e.g. daemon
  needs the origin without wanting client) — place the type in its own
  leaf or `protocol`; handback with the import evidence.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- New rules about what a registry origin may be (e.g. mTLS hosts, ports)
  change in ONE place — `ParseOrigin`.
- `Origin.URL()` returning a fresh copy is deliberate: callers get what
  they need for net/http without shared mutable state.
