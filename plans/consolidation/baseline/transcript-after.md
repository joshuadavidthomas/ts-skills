# Consolidation after transcript

Captured 2026-07-29 from the plan 006 working copy, before its commit.

## Drift check

```console
$ jj log -r '2037ced944acc38456c090ff62e74c9de099318d::@' --no-graph
```

Plans 001–005 each appear as one commit in order. Their rows in the
consolidation README were all `DONE` before this run.

## Build

```console
$ just build
go build  -o ts-skills ./cmd/ts-skills
go build  -o ts-skillsd ./cmd/ts-skillsd
```

## Dev-mode comparison

All state, projects, client configuration, HTTP recordings, and the daemon
log were created under `/tmp`:

```text
state:           /tmp/ts-skillsd-after.BJUBxb
install project: /tmp/ts-skills-project-after.0Jnop2
restore project: /tmp/ts-skills-restore-after.OKiTKu
recording:       /tmp/ts-skills-run-after.01DABC
```

The daemon started on an ephemeral loopback port and identified each request
as `dev@localhost`:

```console
$ TS_SKILLSD_DEV=1 TS_SKILLSD_DEV_LISTEN=127.0.0.1:0 \
    TS_SKILLSD_STATE_DIR="$STATE" ./ts-skillsd
[pid 1692226]
ts-skillsd: dev mode treats every local connection as dev@localhost; never expose this listener
ts-skillsd: dev mode: http://127.0.0.1:36265 (state: /tmp/ts-skillsd-after.BJUBxb)
```

The catalog and upload page returned `200`. The empty catalog still said
`No skills have been published.` and the upload page still said `Upload a
skill`.

The browser upload, review, publish, and current-selection requests returned
`303`; the current API and tree archive returned `200`. The run uploaded the
current bundled files, which are 207 and 22 bytes. The earlier transcript
listed 216 and 29 bytes, but `jj diff --from squukypr --to @ --
examples/example-skill` was empty and the content-addressed results below
match exactly.

```console
$ curl -sS http://127.0.0.1:36265/api/v1/skills/team/example-skill/current
{"namespace":"team","name":"example-skill","digest":"sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b"}

$ sha256sum tree.zip .agents/ts-skills.lock
36b85731be73e6819fa01f56375eea27b44b7836c793d17bd9de712de2af0696  tree.zip
6a054deb252285fd13028035f4f77bbc73337977fd5e354e1d28c1d84c690446  .agents/ts-skills.lock
```

The archive headers, installed files, and command output match the baseline:

```console
Content-Disposition: attachment; filename="example-skill.zip"
Content-Length: 533
Content-Type: application/zip
X-Ts-Skills-Publication-Digest: sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b
X-Ts-Skills-Publication-Name: example-skill
X-Ts-Skills-Publication-Namespace: team

$ ts-skills install --project "$PROJECT" --config "$PROJECT/config.toml" team/example-skill
Installed team/example-skill at sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b.

$ ts-skills restore --project "$RESTORE" --config "$RESTORE/config.toml"
Restored locked skills.

$ diff -r "$PROJECT/.agents/skills" "$RESTORE/.agents/skills"
exit=0

$ ts-skills install --project "$PROJECT" --config "$PROJECT/config.toml" nonexistent/skill
ts-skills: cannot install because the requested skill publication was not found: fetch skill nonexistent/skill: registry value not found: requested publication does not exist
exit=1
```

The exact daemon PID exited after the run. Its ephemeral listener and port
8080 were both free afterward.

## Tailnet comparison

An untagged scratch node first verified the denied half of the capability
gate: `POST /candidates` returned `403` without a curation capability. The
initial policy name was invalid because Tailscale reserves the
`tailscale.com` domain. The daemon and policy now use the project-owned
`joshuadavidthomas.com/cap/ts-skills` name.

A tagged scratch node, `ts-skillsd-consolidation5.tail014d2.ts.net`, then
joined with `tag:skills-registry`. It answered Tailnet ping and served a valid
MagicDNS certificate:

```text
subject: CN=ts-skillsd-consolidation5.tail014d2.ts.net
issuer: C=US; O=Let's Encrypt; CN=YE2
```

The catalog returned `200`; the curator successfully uploaded, published,
and selected `team/example-skill` (`303` for each mutation). The current API,
archive, client install, and lock-only restore produced the same publication
digest, archive hash, lock hash, installed tree, and missing-skill diagnostic
as the dev baseline. This is the curated `200` half of the gate.

All test daemons were stopped by their recorded PIDs; their scratch state,
projects, and auth-key files were removed. The temporary enrolled nodes may
need removal in the Tailnet admin console.

## Diff summary

| Check | Baseline | After | Result |
| --- | --- | --- | --- |
| Publication and installed-tree digest | `sha256:24a68d…47496b` | `sha256:24a68d…47496b` | identical |
| `tree.zip` SHA-256 | `36b857…2af0696` | `36b857…2af0696` | identical |
| Lock SHA-256 | `6a054d…c690446` | `6a054d…c690446` | identical |
| Install, restore, and missing-skill diagnostic | recorded in `transcript.md` | recorded above | identical |
| Dev HTTP path | 200/303/200 sequence | 200/303/200 sequence | identical |
| Tailnet TLS | unavailable | valid MagicDNS certificate; catalog returned 200 | new coverage |
| Tailnet capability gate | unavailable | uncurated request returned 403; curator mutations returned 303 | new coverage |

No unsanctioned behavioral difference was found. Plan 006 is complete.
