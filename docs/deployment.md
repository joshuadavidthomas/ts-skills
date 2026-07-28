# Deploy ts-skillsd on a tailnet

The deployed daemon listens only through tsnet: it joins your tailnet as its own machine and serves HTTPS on its Tailnet address. It never opens a port on the host network, and every request is attributed to the Tailnet identity that made it.

## Before you start

1. **Enable MagicDNS and HTTPS certificates** for the tailnet. Certificates are issued for the full MagicDNS name (`ts-skillsd.example-tailnet.ts.net`), not the short machine name. Certificate names appear in public Certificate Transparency logs, so pick a hostname that reveals nothing private.
2. **Decide who may reach the registry.** Anyone who can open a TCP connection to the daemon on port 443 can curate skills — there is no application-level permission model in v1. Use your tailnet ACLs or grants to scope access. If the daemon will run under a tag, create the tag owner and grant the intended users access to that tag.
3. **If device approval or Tailnet Lock is enabled**, arrange approval or signing for the new node ahead of time.

This project never modifies tailnet policy; ACLs, tags, and approval stay in your hands.

## Configure and enroll the daemon

`ts-skillsd` is configured entirely through environment variables:

| Variable | Meaning |
| --- | --- |
| `TS_SKILLSD_STATE_DIR` | Required. Persistent state directory, private to one daemon process. |
| `TS_SKILLSD_HOSTNAME` | Tailnet machine name. Default `ts-skillsd`. |
| `TS_AUTHKEY` | Standard Tailscale first-enrollment auth key. Only needed once. |
| `TS_SKILLSD_AUTHKEY_FILE` | File containing a first-enrollment auth key. Takes precedence over `TS_AUTHKEY`. |
| `TS_SKILLSD_TAG` | Optional advertised tag, e.g. `tag:skills-registry`. |
| `TS_SKILLSD_VERBOSE` | Enable embedded Tailscale backend logs for debugging. |

For the first start, create a tagged, one-off, non-ephemeral auth key in the Tailscale admin console (pre-approved, if your tailnet uses device approval). tsnet reads the standard `TS_AUTHKEY` variable itself. For secret stores that deliver a file, set `TS_SKILLSD_AUTHKEY_FILE` instead; the file value takes precedence over `TS_AUTHKEY`.

This example uses a private auth-key file:

```console
install -d -m 0700 /var/lib/ts-skillsd
install -m 0600 /path/from/secret-store/ts-skillsd.authkey /run/ts-skillsd.authkey
export TS_SKILLSD_STATE_DIR=/var/lib/ts-skillsd
export TS_SKILLSD_AUTHKEY_FILE=/run/ts-skillsd.authkey
export TS_SKILLSD_TAG=tag:skills-registry
go run ./cmd/ts-skillsd
```

Watch the log for enrollment, then check the Tailscale Machines page: confirm the machine name (a name collision adds a numeric suffix — use whatever full MagicDNS name was actually assigned), the tag, and approval state.

After enrollment succeeds, stop the daemon, delete the auth-key file, unset `TS_SKILLSD_AUTHKEY_FILE` or `TS_AUTHKEY`, and start it again. The node identity now lives in the state directory; later starts need no key.

### The state directory

```text
registry.sqlite    registry metadata
registry.lock      process lifetime lock
trees/             immutable published and candidate trees
tmp/               bounded upload staging
csrf.key           persistent CSRF key (0600)
tsnet/             embedded-node keys and state
```

Treat `tsnet/tailscaled.state` like a private key: it *is* the machine identity. Never point two live daemons at the same state directory, and never copy it to a second machine. Losing it means enrolling again with a new key.

## Configure clients

On each machine that installs skills, create `${XDG_CONFIG_HOME:-$HOME/.config}/ts-skills/config.toml`:

```toml
registry = "https://ts-skillsd.example-tailnet.ts.net"
```

The client machine must itself be on the tailnet and allowed by policy to reach the daemon on port 443.

## Smoke test with two machines

Use machine A for curation and machine B for installation.

1. On A, open `https://ts-skillsd.example-tailnet.ts.net/upload`, upload [`examples/example-skill`](../examples/example-skill) under namespace `team`, review it, and publish. Uploading the same files as a ZIP and as a browser-selected directory should show the same digest.
2. On B, install it into a scratch project:

   ```console
   mkdir -p /tmp/demo
   go run ./cmd/ts-skills install --project /tmp/demo team/example-skill
   ```

   Check `/tmp/demo/.agents/skills/example-skill/` and the digest recorded in `/tmp/demo/.agents/ts-skills.lock`.
3. On A, publish different content for the same skill and make it current.
4. On B, run `go run ./cmd/ts-skills restore --project /tmp/demo`. Restore keeps the digest recorded in the lock — it does not follow the newer publication.
5. Optional identity check: add a forged `X-Forwarded-User` header with a browser extension and publish a throwaway candidate. Its provenance must still show your real Tailnet identity; the daemon derives actors from the connection, never from headers.

## Troubleshooting

**No HTTPS certificate.** MagicDNS or HTTPS certificates are not enabled for the tailnet, or you used the short hostname instead of the full assigned MagicDNS name.

**Browser can't reach the daemon.** The node is unapproved, unsigned (Tailnet Lock), or your ACLs don't allow the source user or machine to reach it on port 443.

**CSRF error on upload.** Reload `/upload` and resubmit; tokens are tied to the browser session and the daemon's persistent key.

**"Registry is busy."** Another daemon holds the state-directory lock. One state directory, one daemon.

**Install reports local changes.** The installed tree no longer matches its locked digest. Move your edited copy aside first; there is no force flag.
