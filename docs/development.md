# Developing ts-skills

Everything here runs on one machine with no Tailnet credentials.

## Tests and checks

```console
go test ./...
go test -race ./...
go vet ./...
```

The suite covers upload normalization, digest identity, SQLite restart, install and restore over HTTP, lock handling, interrupted transactions, Tailnet actor mapping, and daemon shutdown — all through in-process servers.

Useful focused runs while debugging:

```console
go test ./internal/cli -run TestRunInstallsCurrentAndRestoresLockedTree
go test ./internal/web -run 'TestReadOnlyAPICurrentAndExactDownloadsSurviveStorageRestart|TestRestoreUsesLockedPublicationAfterCurrentChanges'
go test ./internal/install -run 'TestTransactionFailurePointsRecoverToLockDestinationAgreement|TestRestore'
go test ./internal/daemon -run TestDevRuntimeServesLoopbackHTTPAsDevActor
```

Cross-platform builds:

```console
GOOS=windows GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=arm64 go build ./...
```

## Running the registry locally

Use dev mode — the [README quickstart](../README.md#quickstart) is the whole procedure:

```console
TS_SKILLSD_DEV=1 go run ./cmd/ts-skillsd
```

`TS_SKILLSD_DEV` is the switch. Two optional variables adjust it: `TS_SKILLSD_DEV_LISTEN` changes the loopback address (default `127.0.0.1:8080`; non-loopback addresses are rejected), and `TS_SKILLSD_STATE_DIR` overrides where dev state lives (default: `ts-skillsd-dev/` under your user cache directory) — delete it to reset the registry.

### How dev mode differs from deployment

Both modes run the identical storage, catalog, and web handler stack. They differ only in the two seams the daemon composes at startup:

| | Deployed (`tsnet`) | Dev (`TS_SKILLSD_DEV=1`) |
| --- | --- | --- |
| Listener | `tsnet` TLS on the Tailnet, port 443 | plain HTTP on loopback |
| Actor | `WhoIs` on the accepted connection | constant `dev@localhost` |

Dev mode never enrolls a Tailnet node, refuses to start if `TS_SKILLSD_AUTHKEY_FILE` is set, and rejects non-loopback listen addresses. It is a development convenience, not a deployment option — nothing about it weakens the deployed path, which still derives every actor from the Tailnet connection.

Testing against a real tailnet is a deployment concern; see the [deployment guide](deployment.md), including its two-machine smoke test.

## The bundled example skill

[`examples/example-skill`](../examples/example-skill) is a minimal valid Agent Skill used by the quickstart and smoke tests. `TestBundledExampleSkillIsValid` in `internal/agentskill` keeps it loadable, so change it deliberately.
