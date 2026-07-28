# Plan 013: Move the rootless-ZIP archive contract into protocol; prove rejections through Remote.Fetch

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/protocol/ internal/client/ internal/web/`
> Plans 003, 004, 012 legitimately change in-scope files — reconcile
> against landed work; on an unexpected mismatch, treat it as a STOP
> condition.

## Status

- **Effort**: M
- **Risk**: MED (wire-contract change compat window — see key constraint)
- **Depends on**: none (sequence after 012 per the index order)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

The v1 tree-archive rules exist nowhere in `protocol`. The client's maximum
envelope is computed from byte layout it *copied from the server's encoder*
(its constant's comment names `web.rootlessZIP` —
`internal/client/client.go:30-34`: extended timestamps, header sizes) and
`web.rootlessZIP` fixes `zip.Store`, timestamps, and modes
(`internal/web/web.go:552-560`). A legal server-side change — an extra ZIP
field, different metadata — can make conforming archives fail existing
clients. A versioned protocol package that doesn't state the envelope is a
boundary that only looks maintained. Relatedly, several client security
tests call `decodeZIP`/`preflightZIP` directly
(`internal/client/client_test.go:171-227`), pinning private choreography
instead of the rejection contract.

## Current state

Verified excerpts:

```go
// internal/client/client.go:30-34
// rootlessZIPEntryFixedBytes accounts for the local file header, data
// descriptor, central directory header, and the extended timestamp copied
// into both headers by web.rootlessZIP. The path follows each header.
rootlessZIPEntryFixedBytes int64 = 30 + 16 + 46 + 9 + 9
rootlessZIPEndBytes        int64 = 22
```

```go
// internal/web/web.go:552-560 (encoder)
header := &zip.FileHeader{Name: name, Method: zip.Store}
header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
header.SetMode(0o644)
```

The client's own tree-acceptance safety limits stay in `safetree`
(`maxRootlessZIPBytes` derives from them, client.go:371-389) — this plan
moves only the *envelope contract*, not the expanded-tree limits.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (package) | `go test -race ./internal/protocol/ ./internal/client/ ./internal/web/ -count=1` | all pass |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/protocol/protocol.go` (+ new test file) — the archive contract
- `internal/client/client.go`, `internal/client/client_test.go`,
  `internal/client/zip_preflight.go`
- `internal/web/web.go`, `internal/web/web_test.go` (encoder conformance)

**Out of scope**:
- Expanded-tree limits (`safetree.Limits`) — unchanged.
- Any JSON/HTTP wire shapes in protocol — unchanged.
- Protocol *version bump* — the goal is to make v1's envelope explicit and
  loose enough that today's server output and any reasonable v1 encoder
  conform. If you conclude a bump is required, STOP (see below).

## Steps

### Step 1: State the contract in protocol

Add to `internal/protocol` (godoc-level contract + constants): the v1 tree
archive is a rootless (no leading directory entry) ZIP using `zip.Store`
entries only; a per-archive maximum byte ceiling expressed independently of
current encoder metadata — e.g. derive from a stated per-entry overhead
allowance large enough for legal header variation, NOT from the current
encoder's exact 30+16+46+9+9 byte sum. The allowance must comfortably cover
today's encoder output (94 bytes/entry) — pick and document the contract
value (e.g. a round 256 bytes/entry) so future encoders have legal room.

Put the *function* that computes a limits-derived ceiling there too, so
client and server call one implementation
(`protocol.TreeArchiveCeiling(limits...)`-shaped; storage of the dep from
protocol to safetree.Limits is fine — protocol already is the shared
vocabulary package; confirm no import cycle).

**Verify**: `go build ./internal/protocol/ && go test ./internal/protocol/` → exit 0.

### Step 2: Both sides consume the contract

Client: `maxRootlessZIPBytes` delegates to the protocol helper; drop the
encoder-copied constants and comment. Web encoder: cite the protocol
contract in a doc comment and keep emitting the same bytes (conformance,
not change). Assert in tests that current encoder output is well under the
contract allowance.

**Verify**: `go test -race ./internal/client/ ./internal/web/ -count=1` → all pass.

### Step 3: Prove rejections through Fetch

Translate the direct-`decodeZIP`/`preflightZIP` tests
(`client_test.go:171-227`) into `httptest.Server`-served archives fetched
through `Remote.Fetch`, asserting the public failure class
(`safetree.ErrLimitExceeded`, `protocol.ErrProtocol`) — exemplar: the
Fetch-path rejection tests already at `client_test.go:367-429`. Keep pure
byte-parser unit tests ONLY where the case cannot be expressed through a
valid HTTP response (argue each survivor in a comment).

**Verify**: `go test -race ./internal/client/ -count=1` → all pass; the
deletion of direct-helper tests doesn't reduce the rejection matrix's case
count.

### Step 4: Full gate

**Verify**: `rg "rootlessZIPEntryFixedBytes|rootlessZIPEndBytes" internal/client/` → no matches; `just check` → exit 0.

## Test plan

- protocol: ceiling function — exact known-input vectors (limits → bytes),
  monotonicity, overflow guards (mirroring the existing overflow test cases
  in client_test.go:121-168, moved to protocol).
- web encoder conformance: a golden or length assertion that the current
  archive sits under the ceiling.
- client: rejection matrix through `Remote.Fetch` (Step 3).

**Verify**: `just test` → all pass.

## Done criteria

- [ ] The v1 archive envelope (Store-only, rootless, per-entry allowance, ceiling) is documented and computed in `internal/protocol`
- [ ] No reference to `web.rootlessZIP` from client code
- [ ] Rejection tests cross `Remote.Fetch` over real HTTP
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- Raising the per-entry allowance breaks interop with ALREADY-DEPLOYED
  clients (a deployed client rejects archives its own tighter ceiling
  can't hold) — that's a versioned-compatibility decision, not this plan's
  call; handback with the deployed-version evidence.
- protocol importing safetree creates pressure toward a cycle or you find
  protocol already depends on request-scoped config — put the ceiling
  function wherever it stays acyclic and describe the placement in the
  handback.
- The web encoder turns out to vary output (e.g. compression switches)
  beyond the stated contract — expand the contract honestly rather than
  pinning today's bytes.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- Any future archive change starts in protocol: state the rule, then
  encode. Client envelope checks must stay derived from protocol, never
  from observed server bytes — that's the finding this plan removes; guard
  it in review.
- The per-entry allowance is now public contract; if it's ever raised, old
  clients must be considered (that's what a version bump is for).
