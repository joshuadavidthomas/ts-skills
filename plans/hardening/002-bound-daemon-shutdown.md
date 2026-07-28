# Plan 002: Bound daemon shutdown so stuck handlers cannot hang the process

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/hardening/README.md.
>
> **Drift check (run first)**:
> `jj diff --from a3f57f4975809df1db7c64053922155be4800228 --to @ -- internal/daemon/ cmd/ts-skillsd/`
> If in-scope files have changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Effort**: M
- **Risk**: MED
- **Depends on**: 001-thread-context-through-tree-walks.md
- **Planned at**: revision `a3f57f4975809df1db7c64053922155be4800228`, 2025-07-28

## Why this matters

Today a handler stuck in long work can hold the daemon open forever. After
the graceful shutdown deadline expires, `shutdownHTTP` force-closes
connections — but `handlers.wait()` still blocks until every admitted
handler returns, and a handler that doesn't observe its request context
never returns. `SIGTERM` against this daemon can hang indefinitely, which
systemd/docker stop sequences escalate to SIGKILL on their own timeout,
defeating the whole graceful-shutdown design.

## Current state

`internal/daemon/daemon.go:266-293` — the run loop joins `server.Serve`,
then waits unconditionally:

```go
case <-ctx.Done():
	handlers.closeAdmission()
	shutdownErr = shutdownHTTP(server, timeout)
	serveErr = <-serveResult
	...
}
handlers.wait()
return errors.Join(shutdownErr, serveErr)
```

`internal/daemon/daemon.go:365-371` — the gate has no deadline:

```go
func (g *handlerGate) wait() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.active != 0 {
		g.drained.Wait()
	}
}
```

`internal/daemon/daemon.go:383-390` — `shutdownHTTP` gives the HTTP server a
bounded graceful window, then `server.Close()` force-closes connections:

```go
func shutdownHTTP(server *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(fmt.Errorf("gracefully shut down Tailnet HTTP: %w", err), server.Close())
	}
	return nil
```

Handlers run under a request context derived from the listener's
`http.Server`; `newHTTPServer` (daemon.go around line 296) wraps the handler
with the admission gate but sets no `BaseContext`. The existing forced-close
behavior test is `internal/daemon/daemon_test.go:62-168` — use its structure
(blocked handler + timeout) as the test exemplar.

Idioms involved: `goroutine-lifetimes-must-be-explicit.md`,
`select-cancellation-and-timers.md`,
`structured-concurrent-task-groups.md` in the reference notes.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Tests (package) | `go test -race ./internal/daemon/ -count=1` | all pass |
| Tests (all) | `just test` | all pass |
| Static analysis | `just vet` | exit 0 |
| Full gate | `just check` | exit 0 |

## Scope

**In scope**:
- `internal/daemon/daemon.go`
- `internal/daemon/daemon_test.go`
- `cmd/ts-skillsd/main.go` (only if the `daemon.Run`/`RunDev` signatures change)

**Out of scope**:
- `internal/web/` — handler-level hygiene is plan 005; this plan only
  guarantees the daemon lifecycle terminates.
- CLI exit shape in `cmd/ts-skillsd/main.go` — plan 006 owns that; touch
  this file only if a signature forces it.
- tsnet lifecycle in `internal/tailnet/` — tsnet shutdown already exists;
  this plan doesn't extend it.

## Steps

### Step 1: Give admitted request work a cancellable owner

In `daemon.Run` (and `RunDev` if it shares this structure), derive a
cancelable server context from the caller's ctx at startup:

```go
serverCtx, cancelServerWork := context.WithCancel(ctx)
defer cancelServerWork()
```

Wire it into the server via `http.Server.BaseContext`:

```go
BaseContext: func(net.Listener) context.Context { return serverCtx },
```

After `shutdownHTTP` returns (both branches of the select), call
`cancelServerWork()` so every still-running handler's `r.Context()` cancels.
Plan 001 guarantees the web layer's heavy work observes that context.

**Verify**: `go build ./...` → exit 0.

### Step 2: Bound the handler drain

Replace the bare `handlers.wait()` with a bounded wait. Shape:

```go
select {
case <-waitDone:               // waitDone <- struct{}{} from a goroutine running handlers.wait()
case <-time.After(drainBound): // bounded
    drainErr = fmt.Errorf("...handler drain exceeded %s ...", drainBound)
}
```

`drainBound` should be derived from the existing `timeout` parameter (e.g.
equal to it), not a new knob — this project does not add speculative config.
Keep the `handlerGate` itself unchanged; the bound lives at the join point,
per `select-cancellation-and-timers.md`. Join `drainErr` into the returned
error so operators can see shutdown was not clean.

The goroutine running `handlers.wait()` must be leak-proof: buffered
result channel of capacity 1, so an abandoned waiter goroutine can always
send and exit.

**Verify**: `go test -race ./internal/daemon/ -count=1` → all existing tests
pass unchanged.

### Step 3: Tests

Add to `internal/daemon/daemon_test.go`, modeled on the 62-168 blocked-
handler test:

1. **Stuck handler, deadline exceeded**: handler ignores its context
   entirely (simulating pre-001 code). `Run` must return, and the returned
   error must mention the drain bound. Assert wall-clock time stays under a
   generous multiple of the configured timeout — do not assert exact
   durations.
2. **Handler observing cancellation**: after 001's plumbing, a handler
   blocked on `r.Context().Done()` drains and shutdown reports no drain
   error.
3. Keep the existing dev-server and normal-shutdown tests untouched and
   passing.

**Verify**: `go test -race ./internal/daemon/ -run 'Shutdown|Drain' -count=1` → all pass.

### Step 4: Full gate

**Verify**: `just check` → exit 0.

## Test plan

Covered by Step 3. Structural exemplar: the blocked-handler fixture in
`internal/daemon/daemon_test.go:62-168`; extend it rather than writing a
parallel fixture.

## Done criteria

- [ ] `rg "BaseContext" internal/daemon/daemon.go` → matches, wired to an owned cancelable context
- [ ] No unbounded `handlers.wait()` call remains in the run path
- [ ] New tests cover ignore-context and observe-context handlers
- [ ] `just check` → exit 0
- [ ] No files outside the in-scope list are modified

## STOP conditions

Stop if:

- The "Current state" excerpts don't match.
- Bounding the drain appears to require changing the `daemon.Run` or
  `RunDev` signatures beyond what `cmd/ts-skillsd/main.go` absorbs — a
  public-ish shape change belongs in a memo, not improvised.
- You find handlers that hold resources (storage handles) which shutdown
  must close *before* the drain completes — ordering there is a design
  decision; hand it back.
- The assumption "web handlers observe r.Context() after plan 001" is
  false in the live tree (plan 001 incomplete or reverted).
- A step's verification fails twice after a reasonable fix attempt.

On stopping, write a **handback**: current state, desired outcome,
lingering questions. Descriptive, not prescriptive.

## Maintenance notes

- The drain bound is intentionally not configurable; if operators need it,
  add it to `daemon.Config` deliberately with docs, not ad hoc.
- A reviewer should scrutinize what happens to storage handles when the
  drain bound is exceeded — today shutdown leaks them to process exit,
  which is acceptable only because the process is dying. If `Run` is ever
  embedded in a longer-lived host, that changes.
- After plan 006 lands, `main()`'s `run()` extraction and this plan's
  error joining compose: the drain error reaches stderr via the normal
  error path.
