# Test ts-skills locally

Use this guide to check the code, build both commands, and run the registry from one development machine.

## Requirements

- Go 1.26.5 or newer
- Tailscale on the machine for the browser and CLI smoke test
- A tailnet where you may enroll a `tsnet` node and enable HTTPS
- The `zip` command for the optional ZIP-upload check

The commands below use a POSIX shell. The automated test suite needs no Tailnet credentials.

## Run the local checks

From the repository root:

```console
go test ./...
go test -race ./...
go vet ./...
```

These commands cover upload normalization, SQLite restart, HTTP install and restore, project locks, transaction interruption, Tailnet identity mapping, and daemon shutdown.

Run the main end-to-end tests on their own when debugging:

```console
go test ./internal/cli -run TestRunInstallsCurrentAndRestoresLockedTree
go test ./internal/web -run 'TestReadOnlyAPICurrentAndExactDownloadsSurviveStorageRestart|TestRestoreUsesLockedPublicationAfterCurrentChanges'
go test ./internal/install -run 'TestTransactionFailurePointsRecoverToLockDestinationAgreement|TestRestore'
go test ./internal/tailnet -run TestActorResolver
```

Create one unique workspace for the interactive check, then build both commands into it:

```console
TS_SKILLS_DEV_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/ts-skills-dev.XXXXXXXX")" || exit 1
[ -n "$TS_SKILLS_DEV_ROOT" ] || exit 1
export TS_SKILLS_DEV_ROOT
printf 'development workspace: %s\n' "$TS_SKILLS_DEV_ROOT"
go build -o "$TS_SKILLS_DEV_ROOT/ts-skills" ./cmd/ts-skills
go build -o "$TS_SKILLS_DEV_ROOT/ts-skillsd" ./cmd/ts-skillsd
```

Keep the printed path. If you open a second terminal, change to the repository root and export the same `TS_SKILLS_DEV_ROOT` value there.

## Browser and CLI smoke test on one machine

`ts-skillsd` has no localhost mode. It listens through `tsnet` so the request actor always comes from the Tailnet connection. The HTTP tests use loopback servers internally, but the command never opens a host listener.

The first run needs the enrollment steps in the main [README](../README.md#first-enrollment). For disposable development state, use a private directory outside the repository:

```console
install -d -m 0700 "$TS_SKILLS_DEV_ROOT/state"
export TS_SKILLSD_STATE_DIR="$TS_SKILLS_DEV_ROOT/state"
export TS_SKILLSD_HOSTNAME=ts-skills-dev
export TS_SKILLSD_AUTHKEY_FILE=/run/ts-skillsd.authkey
"$TS_SKILLS_DEV_ROOT/ts-skillsd"
```

If you use a tag, set `TS_SKILLSD_TAG` before the first start. When enrollment completes, press Ctrl-C, delete the one-off key, and restart from the saved `tsnet` state:

```console
rm -f -- "$TS_SKILLSD_AUTHKEY_FILE"
unset TS_SKILLSD_AUTHKEY_FILE
"$TS_SKILLS_DEV_ROOT/ts-skillsd"
```

Leave the restarted daemon running and use a second terminal for the remaining steps. If this state directory already contains an enrolled node, omit `TS_SKILLSD_AUTHKEY_FILE` and start the daemon once.

Find the node’s full MagicDNS name in the Tailscale Machines page. Open its HTTPS upload page:

```text
https://ts-skills-dev.example-tailnet.ts.net/upload
```

On the upload page:

1. Enter `team` as the namespace.
2. Choose Directory.
3. Select [`examples/example-skill`](../examples/example-skill).
4. Stage the candidate and check its file list, digest, source, and Tailnet actor.
5. Publish it. The first publication becomes current.

You can also check the ZIP path:

```console
(cd examples && zip -r "$TS_SKILLS_DEV_ROOT/example-skill.zip" example-skill)
```

Upload `$TS_SKILLS_DEV_ROOT/example-skill.zip` under the same namespace. It should produce the same digest as the directory upload.

## Install the sample

Write a temporary client config with the full HTTPS MagicDNS origin:

```console
mkdir -p "$TS_SKILLS_DEV_ROOT/project"
printf '%s\n' 'registry = "https://ts-skills-dev.example-tailnet.ts.net"' \
  > "$TS_SKILLS_DEV_ROOT/config.toml"
```

Pass that config directly so your normal config stays untouched:

```console
"$TS_SKILLS_DEV_ROOT/ts-skills" install \
  --project "$TS_SKILLS_DEV_ROOT/project" \
  --config "$TS_SKILLS_DEV_ROOT/config.toml" \
  team/example-skill
```

Inspect the result:

```text
$TS_SKILLS_DEV_ROOT/project/
└── .agents/
    ├── .ts-skills/
    │   ├── operations/
    │   └── write.lock
    ├── skills/
    │   └── example-skill/
    │       ├── SKILL.md
    │       └── references/
    │           └── greeting.txt
    └── ts-skills.lock
```

The lock contains `team/example-skill` and the digest shown on the review page.

Delete the installed directory, then restore it from the lock:

```console
rm -rf "$TS_SKILLS_DEV_ROOT/project/.agents/skills/example-skill"
"$TS_SKILLS_DEV_ROOT/ts-skills" restore \
  --project "$TS_SKILLS_DEV_ROOT/project" \
  --config "$TS_SKILLS_DEV_ROOT/config.toml"
```

Restore should recreate the same files without changing the lock.

## Clean up

Stop the daemon before deleting development state. Removing the state directory destroys the embedded node identity; remove the stale node from the Tailscale Machines page as well.

Remove only the unique workspace created by this guide:

```console
case "$TS_SKILLS_DEV_ROOT" in
  */ts-skills-dev.*) rm -rf -- "$TS_SKILLS_DEV_ROOT" ;;
  *) printf 'refusing to remove unexpected path: %s\n' "$TS_SKILLS_DEV_ROOT" ;;
esac
unset TS_SKILLS_DEV_ROOT
```

## Common failures

### The daemon cannot obtain an HTTPS certificate

Enable MagicDNS and HTTPS certificates for the tailnet, then use the node’s full assigned MagicDNS name. A hostname collision may add a numeric suffix.

### The browser cannot reach the daemon

Check the node’s approval and Tailnet Lock state. Then check that the source user or machine may reach the daemon node or advertised tag on TCP port 443.

### An upload returns a CSRF error

Reload `/upload` and submit again. CSRF tokens belong to the current browser session and daemon key.

### Install reports local changes

The installed tree differs from its locked digest. Move your edited copy aside before installing or restoring; the prototype has no force flag.

### The registry or project is busy

Another daemon owns the registry state lock, or another CLI process owns the project writer lock. Stop the other process and retry. Do not share one state directory between daemon processes.
