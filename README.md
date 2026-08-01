# ts-skills

A private [Agent Skills](docs/research/agentskills-spec.md) registry for your tailnet.

`ts-skillsd` serves a small web UI where tailnet users can browse skills and authorized curators can upload and publish them. `ts-skills` installs a published skill into a project and pins its exact content in a lock file.

- **Content-addressed.** A publication is `namespace/name` plus the SHA-256 digest of its file tree, and is immutable once published. Uploading the same files always produces the same digest.
- **No accounts.** The daemon serves only through [tsnet](https://tailscale.com/kb/1244/tsnet), so every upload and publish action is attributed to the Tailnet identity that made it, and curation is scoped by an ACL application capability. There are no passwords, tokens, or API keys.
- **Reproducible installs.** `ts-skills install` writes the skill under `<project>/.agents/skills/<name>/` and records its digest in `<project>/.agents/ts-skills.lock`. `ts-skills restore` recreates exactly what the lock says, even after the registry's current publication moves on.

## Installation

### Quick install

macOS, Linux, and Windows Subsystem for Linux (WSL):

```bash
curl -fsSL https://raw.githubusercontent.com/joshuadavidthomas/ts-skills/main/install.sh | sh
```

Windows PowerShell:

```powershell
iwr https://raw.githubusercontent.com/joshuadavidthomas/ts-skills/main/install.ps1 -useb | iex
```

The scripts install `ts-skills` and `ts-skillsd` in `~/.local/bin`. Set `TS_SKILLS_INSTALL_DIR` to choose another directory, and ensure the chosen directory is on `PATH`. They verify the release checksum and use an authenticated `gh` CLI to verify GitHub build provenance when the release attestation is available.

### Homebrew

```bash
brew tap joshuadavidthomas/homebrew
brew install ts-skills
```

This installs both `ts-skills` and `ts-skillsd`.

### Release binaries

Each [GitHub release](https://github.com/joshuadavidthomas/ts-skills/releases) ships one archive per platform (Linux, macOS, Windows; amd64 and arm64) containing both binaries, with checksums and signed provenance attestations.

### Go install

With [Go](https://go.dev/) 1.26 or newer:

```bash
go install github.com/joshuadavidthomas/ts-skills/cmd/ts-skills@latest
go install github.com/joshuadavidthomas/ts-skills/cmd/ts-skillsd@latest
```

Or build from source:

```bash
git clone https://github.com/joshuadavidthomas/ts-skills.git
cd ts-skills
just build
```

## Quickstart

Try the whole flow on one machine — no Tailscale account, no configuration. Start a local registry in dev mode:

```console
TS_SKILLSD_DEV=1 ts-skillsd
```

Open <http://127.0.0.1:8080/upload> and publish the bundled example:

1. Enter `team` as the namespace.
2. Select [`examples/example-skill`](examples/example-skill) as the skill directory.
3. Stage it, review the file list and digest, and publish. The first publication automatically becomes current.

Install it into a scratch project:

```console
mkdir -p /tmp/demo
printf 'registry = "http://127.0.0.1:8080"\n' > /tmp/demo/config.toml
ts-skills install --project /tmp/demo --config /tmp/demo/config.toml team/example-skill
```

That gives you:

```text
/tmp/demo/.agents/skills/example-skill/   the installed skill
/tmp/demo/.agents/ts-skills.lock          the pinned digest
```

Delete the installed skill and get it back exactly as pinned:

```console
rm -rf /tmp/demo/.agents/skills/example-skill
ts-skills restore --project /tmp/demo --config /tmp/demo/config.toml
```

When you're done, stop the daemon with Ctrl-C and delete `/tmp/demo`. Dev-mode registry state lives in your user cache directory under `ts-skillsd-dev/`; delete it to start fresh.

Dev mode serves plain HTTP on loopback and attributes every request to `dev@localhost`. It exists so you can try the registry without a tailnet — real deployments always run through tsnet, where identity comes from the connection.

## Deploying on a tailnet

See the [deployment guide](docs/deployment.md). In short: enable MagicDNS and HTTPS certificates on your tailnet, enroll the daemon once with an auth key, point each client's `config.toml` at the daemon's MagicDNS name, and Tailscale ACLs decide who can reach it. The guide includes ready-to-use systemd and Docker setups — tsnet needs no open host ports, no TUN device, and no root.

## Development

```console
just test    # tests with the race detector
just lint    # pre-commit hooks, including golangci-lint
just check   # tests, lint, vet, govulncheck, and module tidiness
```

The test suite needs no Tailnet credentials. See the [development guide](docs/development.md) for focused tests and how dev mode is wired, and [releasing](docs/releasing.md) for how releases are cut.

## License

ts-skills is licensed under the MIT license. See the [`LICENSE`](LICENSE) file for more information.
