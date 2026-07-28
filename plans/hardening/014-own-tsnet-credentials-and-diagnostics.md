# Plan 014: Daemon owns tsnet credentials and diagnostics explicitly

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/daemon/ internal/tailnet/ cmd/ts-skillsd/`
> Plans 002, 006 legitimately change in-scope files — reconcile against
> landed work; on an unexpected mismatch, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: MED (touches enrollment; a mistake locks the daemon out of the tailnet)
- **Depends on**: none (sequence after 013 per the index order; 002 and 006 should land first since they touch the same files)
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Two ambient-authority leaks at the tsnet boundary. (1) The daemon's config
reads only `TS_SKILLSD_AUTHKEY_FILE`, but an empty `AuthKey` reaches
`tsnet.Server`, which then discovers credentials itself — `TS_AUTHKEY`,
`TS_AUTH_KEY`, OAuth client credentials, workload identity — by precedence
rules the daemon never sees. The daemon can enroll differently from what
its configuration says. Dev mode already treats ambient `TS_AUTHKEY` as
consequential (it rejects it); production silently depends on it.
(2) `tailnet.ListenTLS` mutates process-global state — `log.Printf` for
verbose output (UserLogf left unset, so node login/status output follows
the standard logger's destination) and `hostinfo.SetApp("ts-skillsd")` at
listener construction. Both are composition-root concerns happening inside
an adapter.

## Current state

`internal/daemon/daemon.go:127-153` (`ConfigFromEnv`) — reads
`TS_SKILLSD_STATE_DIR`, `TS_SKILLSD_HOSTNAME`, `TS_SKILLSD_TAG`,
`TS_SKILLSD_VERBOSE`, `TS_SKILLSD_AUTHKEY_FILE`; never touches
`TS_AUTHKEY`. `internal/daemon/daemon.go:73-82` (DevConfigFromEnv) —
explicitly rejects `TS_AUTHKEY` in dev.

`internal/tailnet/tailnet.go:124-134` (`ListenTLS`):

```go
	ts := &tsnet.Server{
		Hostname: config.Hostname, Dir: config.StateDir,
		AuthKey: config.AuthKey,
		AdvertiseTags: append([]string(nil), config.AdvertiseTags...),
		Logf:   func(string, ...any) {},
	}
	if config.Verbose {
		ts.Logf = log.Printf
	}
	hostinfo.SetApp("ts-skillsd")
```

`UserLogf` unset → tsnet's default routes node messages through the process
standard logger (tsnet@v1.102.0 tsnet.go:233-236, 1105-1114 — third-party
behavior, verified via module cache).

Contrast (well-shaped): both binaries own their streams explicitly —
`cmd/ts-skillsd/main.go:15-46`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (packages) | `go test -race ./internal/daemon/ ./internal/tailnet/ -count=1` | all pass |
| Build | `just build` | exit 0 |
| Full gate | `just check` | exit 0 |
| Manual smoke | `TS_AUTHKEY=tskey-auth-fake go run ./cmd/ts-skillsd` (dev=false env) | daemon starts OR fails with the new explicit error, never by silent env discovery |

## Scope

**In scope**:
- `internal/daemon/daemon.go` (+ tests) — credential resolution, diagnostics wiring
- `internal/tailnet/tailnet.go` (+ tests) — accept a log func; drop hostinfo mutation
- `cmd/ts-skillsd/main.go` — app label placement if plan 006 hasn't landed its
  `run()`; if it has, placement goes there

**Out of scope**:
- `internal/web`'s logger injection — plan 005; wire whatever plan 005
  provides at the same daemon composition point, don't build it here.
- Dev-mode rejection of `TS_AUTHKEY` — already exists; keep it.
- Deployment docs — update only the env-var documentation that exists in
  (check `docs/` and README for `TS_SKILLSD_AUTHKEY_FILE` references); no new doc site.

## Steps

### Step 1: Resolve credentials in daemon config, deliberately

`ConfigFromEnv` (or `normalizeConfig` after it) resolves the supported
chain in one documented order: `TS_SKILLSD_AUTHKEY_FILE` contents,
then `TS_AUTHKEY`. Set the result on `Config.AuthKey`. Decide and document:
(a) if neither is present but the tsnet state dir already holds node keys,
  starting is legal (re-auth not required); (b) ambient tsnet-supported
  variables the daemon does NOT support (`TS_AUTH_KEY`, `TS_OAUTH_*`,
  workload identity) cause an explicit startup error naming the variable
  when AuthKey resolution found nothing AND state has no keys — otherwise
  they're ignored-but-documented. The point: enrollment behavior is now a
  function of daemon config + persisted state, never of unexplored env.

**Verify**: `go test -race ./internal/daemon/ -count=1` → all pass; new
table tests cover each chain position.

### Step 2: Adapter stops touching process globals

`tailnet.ServerConfig` gains `Logf func(string, ...any)` (daemon supplies
it; nil → discard). `ListenTLS` sets `ts.Logf = config.Logf` (fall back to
discard) and ALWAYS sets `ts.UserLogf` to a daemon-supplied func (_default
to discarding status noise, or route to the same Logf — pick one and
document_). `hostinfo.SetApp` moves to `cmd/ts-skillsd` (process bootstrap).

**Verify**: `go build ./...` → exit 0; `rg "hostinfo" internal/tailnet/` →
no matches; `rg "hostinfo.SetApp" cmd/` → matches in the daemon binary.

### Step 3: Daemon wires diagnostics

Daemon constructs the Logf from its Verbose flag and (after plan 005 lands)
its logger; passes it through ServerConfig. No new env knobs.

**Verify**: `go test -race ./internal/daemon/ ./internal/tailnet/ -count=1` → all pass.

### Step 4: Full gate

**Verify**: `just check` → exit 0; manual smoke command above behaves as stated.

## Test plan

- daemon: env-resolution table tests for every chain position, the
  ambiguity rules, and rejection of unsupported ambient vars when they'd
  matter (exemplar: existing `ConfigFromEnv` tests in daemon_test.go).
- tailnet: construction maps config → tsnet.Server fields (keep it to a
  seam test; tsnet.Start in tests stays dev-loopback/fixed as today).

**Verify**: `go test -race ./internal/daemon/ ./internal/tailnet/ -count=1` → all pass.

## Done criteria

- [ ] `rg "TS_AUTHKEY|TS_AUTH_KEY|TS_OAUTH|WORKLOAD" internal/daemon/` — ambient vars are either consumed deliberately or explicitly rejected
- [ ] `rg "log.Printf|hostinfo" internal/tailnet/` → no matches
- [ ] `just check` → exit 0
- [ ] Deployment docs mentioning enrollment env vars updated to the stated chain
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- `TS_AUTHKEY` support turns out to be load-bearing for an existing
  deployment path documented in this repo — changing default enrollment is
  then an ops decision; hand back with the evidence.
- tsnet requires UserLogf/Logf to be set BEFORE `Start` in ways forcing the
  adapter to own them anyway — describe the constraint rather than hacking
  around it.
- Plan 005 hasn't landed and daemon has no logger — proceed with
  `log.Printf`-equivalent default rather than inventing logging design here.
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- The credential chain order lives in one doc comment AND the deployment
  doc; keep them adjacent in review.
- Any new tsnet knob must arrive via `ServerConfig` — if a tsnet feature
  demands env discovery, it gets an explicit daemon-level opt-in, never
  pass-through.
