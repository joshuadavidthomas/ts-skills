# ts-skill-registry

`ts-skillsd` is a private Agent Skill registry served through an embedded Tailscale node. It accepts ZIP or browser-directory uploads, keeps immutable publications, and exposes the current publication to `ts-skills`. The daemon listens only through `tsnet`; it does not open port 443 on the host network.

## Tailnet requirements

Before starting the daemon:

1. Enable MagicDNS and HTTPS certificates for the tailnet. Tailscale certificates validate the full MagicDNS name, such as `ts-skillsd.example-tailnet.ts.net`, rather than the short machine name.
2. Decide whether the daemon will use a tag. If so, create the tag owner and grant the intended users or machines TCP access to that tag on port 443. Advertising a tag does not grant network access by itself.
3. If device approval or Tailnet Lock is enabled, arrange approval or signing for the new node.

This repository does not modify tailnet policy. Check the existing grants or ACLs before enrollment. HTTPS certificate names appear in public Certificate Transparency logs, so choose a hostname that does not contain private information.

## Daemon configuration

`ts-skillsd` reads these environment variables at startup:

| Variable | Use |
| --- | --- |
| `TS_SKILLSD_STATE_DIR` | Required persistent state directory. |
| `TS_SKILLSD_HOSTNAME` | Tailnet machine name; defaults to `ts-skillsd`. |
| `TS_SKILLSD_AUTHKEY_FILE` | Optional file containing a first-enrollment auth key. Remove it after enrollment. |
| `TS_SKILLSD_TAG` | Optional single advertised tag, for example `tag:skills-registry`. |

The state directory must be private, writable, durable, and used by only one daemon process. It contains:

```text
registry.sqlite       registry metadata
registry.lock         process lifetime lock
trees/                immutable published and candidate trees
tmp/                  bounded upload and capture staging
csrf.key              persistent 32-byte key, mode 0600
tsnet/                 embedded-node keys, state, and logs
```

Treat `tsnet/tailscaled.state` as a private key. Do not copy one state directory to two live daemons.

## First enrollment

Create a tagged, one-off, non-ephemeral auth key in the Tailscale admin console. Make it pre-approved if the tailnet uses device approval. Store only the key in a mode-0600 file, then start the daemon:

```console
install -d -m 0700 /var/lib/ts-skillsd
install -m 0600 /path/from/secret-store/ts-skillsd.authkey /run/ts-skillsd.authkey
export TS_SKILLSD_STATE_DIR=/var/lib/ts-skillsd
export TS_SKILLSD_HOSTNAME=ts-skillsd
export TS_SKILLSD_AUTHKEY_FILE=/run/ts-skillsd.authkey
export TS_SKILLSD_TAG=tag:skills-registry
go run ./cmd/ts-skillsd
```

Watch the daemon output for enrollment status. In the Tailscale Machines page, confirm the assigned machine name, requested tag, approval state, and Tailnet Lock signature. Name collisions can add a numeric suffix, so use the assigned full MagicDNS name.

After successful enrollment:

1. Stop the daemon.
2. Delete the auth-key file and unset `TS_SKILLSD_AUTHKEY_FILE`.
3. Keep `TS_SKILLSD_STATE_DIR` unchanged.
4. Restart and confirm that the same Tailnet machine identity returns.

The saved tsnet state handles later starts. Losing that state requires a new enrollment key.

## Client configuration

Create `${XDG_CONFIG_HOME:-$HOME/.config}/ts-skills/config.toml` on each client machine. Use the full HTTPS MagicDNS origin:

```toml
registry = "https://ts-skillsd.example-tailnet.ts.net"
```

The client must run on a Tailnet machine whose identity can reach the daemon on TCP port 443.

## Two-machine smoke test

Use Machine A for curation and Machine B for installation. Both machines must pass the tailnet policy check for the daemon.

1. On Machine A, open `https://ts-skillsd.example-tailnet.ts.net/upload` in a browser.
2. Upload an Agent Skill ZIP, review its escaped `SKILL.md` and file list, publish it, and confirm it becomes current. Uploading the same files through browser directory selection should show the same tree digest.
3. On Machine B, create the client config above and an explicit project directory, then install current:

   ```console
   mkdir -p /tmp/skill-smoke-project
   go run ./cmd/ts-skills install --project /tmp/skill-smoke-project team/example-skill
   ```

4. Inspect `/tmp/skill-smoke-project/.agents/skills/example-skill/` and `/tmp/skill-smoke-project/.agents/ts-skills.lock`. The lock records the published digest.
5. On Machine A, publish different content for the same skill and select it as current.
6. On Machine B, restore the existing lock:

   ```console
   go run ./cmd/ts-skills restore --project /tmp/skill-smoke-project
   ```

   Restore keeps the digest already recorded on Machine B rather than following the new current publication.
7. To check header spoofing, send a request with an invented identity header. The recorded upload/publish actor must still match the Tailnet connection returned by `WhoIs`:

   ```console
   curl -H 'X-Forwarded-User: someone-else@example.com' \
     https://ts-skillsd.example-tailnet.ts.net/
   ```

Stop after local verification if a corrupt-transfer test needs a modified test server. Do not alter a live tailnet or production registry to inject corruption.
