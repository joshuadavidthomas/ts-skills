# Plan 009: Require a curator capability at every catalog mutation

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/registry/ internal/web/ internal/tailnet/ internal/daemon/`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: MED (touches authorization on every mutation route)
- **Depends on**: none (sequence after 017/018 per the index order)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Curation authority is translated once at the tsnet edge into a boolean, then
re-checked by convention in three handlers. The registry mutations
themselves accept a plain `Actor` that proves identity, not authority — so
any future mutation route (or non-web caller) can reach a privileged
transition without any check, and nothing in the type system stops it. This
was flagged independently by three reviewers. Authorization that lives only
in handler arrangements is a waiting hole; it belongs in the transition
contract.

## Current state

Authority is born here — `internal/tailnet/tailnet.go:52-93`
(`ActorResolver` reduces LocalAPI capability JSON to the actor plus a
bool):

```go
// internal/web/web.go:51-54
type Identity struct {
	Actor     registry.Actor
	CanCurate bool
}
```

and spent by repetition in three handlers — `internal/web/web.go:246-253`
(candidate creation), `371-386` (publish), `393-403` (set current), all
shaped like:

```go
identity, err := h.actors.Resolve(r)
...
if !identity.CanCurate { render 403; return }
```

Dev mode hardcodes the capability: `internal/daemon/daemon.go` dev resolver
returns `Identity{Actor: dev actor, CanCurate: true}`.

The receiving seam knows nothing of it — `internal/registry/catalog.go:27-37`:

```go
type CatalogRecords interface {
	...
	PublishCandidate(context.Context, CandidateID, Actor, time.Time) (PublishResult, error)
	SelectCurrent(context.Context, PublicationID, Actor, time.Time) (CurrentPublication, error)
```

and `registry.Actor` (internal/registry/identity.go:26-29) is an audit
identity only: `id` + `display`.

Web's identity port: `internal/web/web.go:56-58`:

```go
type ActorResolver interface {
	Resolve(r *http.Request) (Identity, error)
}
```

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/registry/ ./internal/web/ ./internal/tailnet/ ./internal/daemon/ ./internal/storage/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/registry/` — new capability type; mutation signatures
- `internal/web/web.go` + `internal/web/web_test.go` — resolver port, route wiring
- `internal/tailnet/tailnet.go` + tests — resolver produces capability
- `internal/daemon/daemon.go` + tests — dev resolver produces capability
- `internal/storage/` — signature updates only (Actor → capability where it lands)

**Out of scope**:
- `internal/protocol/` — wire shape unchanged (403 responses keep their body)
- Capability granularity beyond one curator bit (no per-namespace ACLs —
  not today's model; don't invent)
- The `web.Catalog` mirror interface (deferred in the index)

## Steps

### Step 1: Define the capability in registry

Add to `internal/registry` (identity.go is the home) a curator value that
*dereferences to* an Actor — sketch:

```go
// Curator is an Actor authorized to mutate the catalog.
type Curator struct{ actor Actor }

func NewCurator(actor Actor) Curator
func (c Curator) Actor() Actor
```

Do not add capability sets or roles; the whole point is one refined type
carrying the existing actor.

**Verify**: `go build ./internal/registry/` → exit 0.

### Step 2: Mutations require the capability

Change the registry-side mutation parameters: `Capture` (its provenance
carries the actor — decide whether provenance stays actor-only or the
caller passes the curator separately; the provenance record is audit data,
so pass the capability as a distinct parameter and derive the provenance
actor from it), `Publish(ctx, id, curator, at)`, `SetCurrent(ctx, id, curator, at)`.
Step the signature change through `CatalogRecords`, `storage.Catalog`, the
web handlers, and integration tests. Read-only methods keep taking nothing.

Note: plans 010 and 018 also reshape these exact signatures — whichever
lands first, the later plan adapts. Sequence per the index.

**Verify**: `go build ./... && go test -race ./internal/registry/ ./internal/storage/ -count=1` → all pass.

### Step 3: Translate once at the identity edge

Reshape the web seam: the resolver returns either a denied result or an
`Identity` gaining a `Curator registry.Curator` for curating identities —
preferred shape: `web.Identity` keeps `Actor`, and curating resolution is a
separate method or a wrapped resolver that produces `Curator`. Pick ONE of:

- `Identity` gains `Curator() (registry.Curator, bool)` — presence = authority, or
- `ActorResolver.ResolveCurator(r) (registry.Curator, error)` used only by curating routes.

Recommended: the second — curating routes resolve authority explicitly and
non-curating routes never touch it. Tailnet's resolver maps capability JSON
→ `NewCurator(actor)`; the dev resolver returns a fixed curator. Handlers
then drop their `CanCurate` checks and pass `curator` straight into the
catalog call; denial lives in the resolver, one place.

**Verify**: `go test -race ./internal/web/ ./internal/tailnet/ ./internal/daemon/ -count=1` → all pass.

### Step 4: Full gate

**Verify**: `rg "CanCurate" internal/` → zero matches (the boolean is gone);
`just check` → exit 0.

## Test plan

- `internal/web/web_test.go`: each curating route with a non-curating
  resolver still yields 403 — now asserted against resolver denial rather
  than handler checks; remove now-dead handler-level denial tests, keep the
  route-level ones (web_test.go:324-369 is the enumeration exemplar).
- `internal/registry` (or storage): calling `Publish`/`SetCurrent` requires
  a Curator — compile-time, plus a constructor test that
  `NewCurator(NewActor(...))` behaves.
- Denied-path integration: non-curating tsnet identity → 403 on publish
  (extend existing network tests if convenient, else web_test suffices).

**Verify**: `go test -race ./internal/web/ ./internal/registry/ ./internal/storage/ -count=1` → all pass.

## Done criteria

- [ ] `rg "Curator" internal/registry/identity.go` → capability type defined
- [ ] `Publish`, `SetCurrent`, `Capture` cannot be called with a bare `Actor`
- [ ] `rg "CanCurate"` → no matches
- [ ] 403 behavior on non-curating identities unchanged (tests)
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The "Current state" excerpts don't match (plans 010/018 reshape these
  signatures — reconcile against whichever landed).
- A non-web production caller of catalog mutations surfaces (the design
  assumes web is the only entry; if the catalog integration adapter in
  `catalog_integration_test.go` is intended as precedent for a second
  caller, the capability placement needs a memo).
- You find denial semantics in tsnet capability parsing you don't expect
  (e.g. multiple capability grants to merge). Handback; don't invent merge
  policy.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Rule going forward: catalog mutations take `Curator`; reads take nothing.
  A mutation that accepts `Actor` is a review rejection.
- `Actor` stays the audit vocabulary (provenance, published-by); `Curator`
  is only proof of authority — do not store curators in persisted rows,
  persist their `Actor()`.
