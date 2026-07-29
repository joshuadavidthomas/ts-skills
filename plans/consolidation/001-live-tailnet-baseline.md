# Plan 001: Record a live-tailnet behavioral baseline

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/consolidation/README.md.
>
> **Drift check (run first)**:
> `jj diff --from 2037ced944acc38456c090ff62e74c9de099318d --to @ -- cmd/ internal/`
> Any code drift is fine for this plan (it changes no code), but record
> the revision the baseline was actually captured at — the whole point is
> a pinned reference.

## Status

- **Effort**: M
- **Risk**: LOW (no code changes; touches a live tailnet)
- **Depends on**: none
- **Planned at**: revision `2037ced944acc38456c090ff62e74c9de099318d`, 2026-07-28

## Why this matters

Plans 002–005 change install semantics and collapse ten packages. Every
one of them re-verifies against "the golden path still behaves exactly as
before" — which is unfalsifiable unless *before* is written down. The live
tailnet smoke test has also never been run (dev mode has); this plan pays
that debt and produces the reference transcript in one motion.

## Test-hygiene rules (repo policy, non-negotiable)

- All daemon state lives in scratch directories under `/tmp` — never under
  `~/.cache`, `~/.local`, or any home path.
- Dev mode uses an **ephemeral port** (`:0` / defaulted listen), never a
  fixed 8080 assumption.
- Kill daemons by **exact recorded pid**, never `pkill` by name.
- Before ending the session, verify port 8080 is free:
  `ss -tlnp | grep :8080` → no output.

## Procedure

Record every command and its full output into
`plans/consolidation/baseline/transcript.md` as you go (create the
directory). Prefix each section with the step number below.

### 1. Build

```
just build
./ts-skills --version && ./ts-skillsd --version
```

Record binary hashes: `sha256sum ts-skills ts-skillsd`.

### 2. Dev-mode golden path

Follow `docs/development.md` for the exact invocation. Shape:

1. `STATE=$(mktemp -d /tmp/ts-skillsd-baseline.XXXXXX)` — scratch state.
2. Start `ts-skillsd` in dev mode with an ephemeral/defaulted listen
   address; record the pid and the bound address it prints.
3. Web UI: fetch `GET /` (catalog page, expect empty), `GET /upload`.
4. Upload `examples/example-skill` through `POST /candidates`
   (multipart, per the upload page's form contract), then
   `GET /candidates/{id}`, `POST /candidates/{id}/publish`,
   `POST /current`.
5. Record the API surface: `GET <api>/current` for the skill, and
   download `<api>/publications/{digest}/tree.zip`; record
   `sha256sum` of the zip and the publication digest.
6. Client: `PROJ=$(mktemp -d /tmp/ts-skills-proj.XXXXXX)`; point the CLI
   config (scratch config file, not `~/.config`) at the dev origin;
   `ts-skills install <skill>` into `$PROJ`; record the resulting
   `.agents/` tree (`find $PROJ/.agents -type f | sort`), lock file bytes
   (`sha256sum`), and installed tree digest.
7. Restore: copy the lock file alone into a second scratch project;
   `ts-skills restore`; verify byte-identical tree
   (`diff -r` between projects' `.agents/skills`).
8. Failure shape: run `ts-skills install nonexistent/skill` and record
   the exact diagnostic and exit code.
9. Kill the daemon by recorded pid; confirm process exit; confirm the
   port it bound is released.

### 3. Live tailnet

Requires a tailnet auth key (tagged, per `docs/deployment.md`). Shape:

1. Scratch state dir under `/tmp`; auth key via `TS_SKILLSD_AUTHKEY_FILE`
   (the supported chain from hardening 014 — do not export `TS_AUTHKEY`
   if the file form works).
2. Start `ts-skillsd`; record the node name and tailnet address; verify
   the node appears in the tailnet admin console / `tailscale status`.
3. Repeat the golden path of step 2 (upload → publish → current →
   install → restore) over the tailnet HTTPS origin, from this host.
4. Verify the capability gate: a mutation route (`POST /candidates`)
   from an identity *without* the curation capability must return 403;
   with it, 200. Record which identities were used.
5. Record TLS details: `curl -sv https://<node>/ 2>&1 | grep -E 'subject|issuer'`.
6. Shut down by pid. If the node was enrolled ephemeral, confirm it
   leaves the tailnet; if not, record that node cleanup is manual and
   whether it was done.

### 4. Pin it

Append to the transcript: date, `jj log -r @ --no-graph`, binary hashes,
and the two tree digests (publication digest, installed digest). Update
this plan's status row and add a reconciliation entry to the README.

## Verification

- `plans/consolidation/baseline/transcript.md` exists, contains all
  numbered sections, both digests, and both exit-code records.
- `ss -tlnp | grep :8080` → empty.
- No new files under `$HOME` from the run (config and state were scratch).

## STOP conditions

- **No tailnet auth key is available**: complete section 2 (dev mode),
  mark this plan `DONE (dev-only baseline)` in the README table with a
  one-line note, and record in the reconciliation log that section 3 must
  be run before 006's final comparison can claim tailnet parity.
- **Any golden-path step fails**: that is a product bug discovered by the
  baseline, not something this plan fixes. Stop, write a handback naming
  the failing step and output, and file it — the consolidation effort
  does not start on a broken baseline.
- The upload form contract in step 2.4 doesn't match `docs/development.md`
  or the actual UI: stop and hand back rather than reverse-engineering
  multipart details into the transcript.

## Maintenance notes

The transcript is a point-in-time artifact, deliberately checked in. Plans
002–005 each re-run the dev-mode golden path (section 2) and 006 re-runs
everything; they diff against this file. If the product's UX changes
intentionally later, the transcript does not need updating — it is the
baseline for *this effort*, not living documentation.
