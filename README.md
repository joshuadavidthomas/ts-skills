# ts-skill-registry

A private [Agent Skills](docs/research/agentskills-spec.md) registry for your tailnet.

`ts-skillsd` serves a small web UI where tailnet users can browse skills and authorized curators can upload and publish them. `ts-skills` installs a published skill into a project and pins its exact content in a lock file.

- **Content-addressed.** A publication is `namespace/name` plus the SHA-256 digest of its file tree, and is immutable once published. The same files always produce the same digest, whether uploaded as a ZIP or a browser-selected directory.
- **No accounts.** The daemon serves only through [tsnet](https://tailscale.com/kb/1244/tsnet), so every upload and publish action is attributed to the Tailnet identity that made it and curation is scoped by an ACL application capability; there are no passwords, tokens, or API keys.
- **Reproducible installs.** `ts-skills install` writes the skill under `<project>/.agents/skills/<name>/` and records its digest in `<project>/.agents/ts-skills.lock`. `ts-skills restore` recreates exactly what the lock says, even after the registry's current publication moves on.

## Quickstart

You need Go 1.26 or newer. No Tailscale account, no configuration.

Start a local registry in dev mode:

```console
TS_SKILLSD_DEV=1 go run ./cmd/ts-skillsd
```

Open <http://127.0.0.1:8080/upload> and publish the bundled example:

1. Enter `team` as the namespace.
2. Choose **Directory** and select [`examples/example-skill`](examples/example-skill).
3. Stage it, review the file list and digest, and publish. The first publication automatically becomes current.

Install it into a scratch project:

```console
mkdir -p /tmp/demo
printf 'registry = "http://127.0.0.1:8080"\n' > /tmp/demo/config.toml
go run ./cmd/ts-skills install --project /tmp/demo --config /tmp/demo/config.toml team/example-skill
```

That gives you:

```text
/tmp/demo/.agents/skills/example-skill/   the installed skill
/tmp/demo/.agents/ts-skills.lock          the pinned digest
```

Delete the installed skill and get it back exactly as pinned:

```console
rm -rf /tmp/demo/.agents/skills/example-skill
go run ./cmd/ts-skills restore --project /tmp/demo --config /tmp/demo/config.toml
```

When you're done, stop the daemon with Ctrl-C and delete `/tmp/demo`. Dev-mode registry state lives in your user cache directory under `ts-skillsd-dev/`; delete it to start fresh.

Dev mode serves plain HTTP on loopback and attributes every request to `dev@localhost`. It exists so you can try the registry without a tailnet — real deployments always run through tsnet, where identity comes from the connection.

## Deploying on a tailnet

See the [deployment guide](docs/deployment.md). In short: enable MagicDNS and HTTPS certificates on your tailnet, enroll the daemon once with an auth key, point each client's `config.toml` at the daemon's MagicDNS name, and Tailscale ACLs decide who can reach it.

## Development

```console
go test ./...
```

The test suite needs no Tailnet credentials. See the [development guide](docs/development.md) for focused tests, race and cross-platform checks, and how dev mode is wired.
