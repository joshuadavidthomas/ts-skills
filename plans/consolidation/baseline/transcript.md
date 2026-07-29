# Consolidation baseline transcript

Captured 2026-07-29 at working-copy revision `006e2bf8` (`squukypr`).

The drift check required by plan 001 produced no output:

```console
$ jj diff --from 2037ced944acc38456c090ff62e74c9de099318d --to @ -- cmd/ internal/
```

This baseline uses scratch-only state and client configuration:

```text
state:           /tmp/ts-skillsd-baseline.wGpsyw
install project: /tmp/ts-skills-proj.MXMmn7
restore project: /tmp/ts-skills-restore.dU4tSw
```

No config or daemon state was written beneath `$HOME`.

## 1. Build

```console
$ just build
go build  -o ts-skills ./cmd/ts-skills
go build  -o ts-skillsd ./cmd/ts-skillsd

$ ./ts-skills --version
ts-skills dev

$ ./ts-skillsd --version
ts-skillsd dev

$ sha256sum ts-skills ts-skillsd
6d6588bd34cf94be43490206567890fcc9d0319d4fae06e48b65d25215a6fab5  ts-skills
e2058e37682cc43ea15ed93788568ba8e1c5ba2f84485502ae27fe98fe340779  ts-skillsd
```

## 2. Dev-mode golden path

### 2.1 Start

```console
$ STATE=$(mktemp -d /tmp/ts-skillsd-baseline.XXXXXX)
$ TS_SKILLSD_DEV=1 TS_SKILLSD_DEV_LISTEN=127.0.0.1:0 \
    TS_SKILLSD_STATE_DIR="$STATE" ./ts-skillsd
[pid 1148427]
ts-skillsd: dev mode treats every local connection as dev@localhost; never expose this listener
ts-skillsd: dev mode: http://127.0.0.1:35723 (state: /tmp/ts-skillsd-baseline.wGpsyw)
```

The daemon bound the requested ephemeral loopback port. Every dev request ran as `dev@localhost`.

### 2.2 Catalog and upload page

```console
$ curl -sS -i http://127.0.0.1:35723/
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
Content-Length: 1698

[empty catalog page]
No skills have been published.
Upload a skill to stage it for review, then publish it to the catalog.

$ curl -sS -i -c /tmp/ts-skills-baseline-run.Zy8fNp/cookies.txt \
    http://127.0.0.1:35723/upload
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8
X-Csrf-Token: [ephemeral token]

[upload page]
Upload a skill
Namespace
Skill files
Stage for review
```

The upload page matches its documented form contract: `namespace`, then a JSON `manifest`, then ordered `file-N` multipart parts. The CSRF cookie and token were passed on every mutation.

### 2.3 Upload, review, publish, and make current

```console
$ curl -sS -i -b cookies.txt -c cookies.txt -H 'X-CSRF-Token: [ephemeral token]' \
    -F 'namespace=team' \
    -F 'manifest=[{"index":0,"path":"example-skill/SKILL.md","size":216},{"index":1,"path":"example-skill/references/greeting.txt","size":29}]' \
    -F 'file-0=@examples/example-skill/SKILL.md;filename=SKILL.md' \
    -F 'file-1=@examples/example-skill/references/greeting.txt;filename=greeting.txt' \
    http://127.0.0.1:35723/candidates
HTTP/1.1 303 See Other
Location: /candidates/250dddfc3bd45a3611a1b5a478619ba9
Content-Length: 0

$ curl -sS -i -b cookies.txt \
    http://127.0.0.1:35723/candidates/250dddfc3bd45a3611a1b5a478619ba9
HTTP/1.1 200 OK
Content-Type: text/html; charset=utf-8

Review team/example-skill
Candidate: 250dddfc3bd45a3611a1b5a478619ba9
Digest: sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b
Source: example-skill
Submitted by: dev@localhost
Files: SKILL.md, references/greeting.txt

$ curl -sS -i -b cookies.txt -c cookies.txt -H 'X-CSRF-Token: [ephemeral token]' \
    -X POST http://127.0.0.1:35723/candidates/250dddfc3bd45a3611a1b5a478619ba9/publish
HTTP/1.1 303 See Other
Location: /candidates/250dddfc3bd45a3611a1b5a478619ba9
Content-Length: 0

$ curl -sS -i -b cookies.txt -c cookies.txt -H 'X-CSRF-Token: [ephemeral token]' \
    -X POST --data-urlencode 'skill=team/example-skill' \
    --data-urlencode 'digest=sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b' \
    http://127.0.0.1:35723/current
HTTP/1.1 303 See Other
Location: /
Content-Length: 0
```

### 2.4 API and archive

```console
$ curl -sS -i http://127.0.0.1:35723/api/v1/skills/team/example-skill/current
HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 127

{"namespace":"team","name":"example-skill","digest":"sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b"}

$ curl -sS -D zip.headers -o tree.zip \
    http://127.0.0.1:35723/api/v1/skills/team/example-skill/publications/sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b/tree.zip
HTTP/1.1 200 OK
Content-Disposition: attachment; filename="example-skill.zip"
Content-Length: 533
Content-Type: application/zip
X-Ts-Skills-Publication-Digest: sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b
X-Ts-Skills-Publication-Name: example-skill
X-Ts-Skills-Publication-Namespace: team

$ sha256sum tree.zip
36b85731be73e6819fa01f56375eea27b44b7836c793d17bd9de712de2af0696  tree.zip
```

### 2.5 Install and restore

```console
$ printf 'registry = "http://127.0.0.1:35723"\n' > /tmp/ts-skills-proj.MXMmn7/config.toml
$ ./ts-skills install --project /tmp/ts-skills-proj.MXMmn7 \
    --config /tmp/ts-skills-proj.MXMmn7/config.toml team/example-skill
Installed team/example-skill at sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b.
exit=0

$ find /tmp/ts-skills-proj.MXMmn7/.agents -type f | sort
/tmp/ts-skills-proj.MXMmn7/.agents/.ts-skills/write.lock
/tmp/ts-skills-proj.MXMmn7/.agents/skills/example-skill/SKILL.md
/tmp/ts-skills-proj.MXMmn7/.agents/skills/example-skill/references/greeting.txt
/tmp/ts-skills-proj.MXMmn7/.agents/ts-skills.lock

$ cat /tmp/ts-skills-proj.MXMmn7/.agents/ts-skills.lock
schema = 1

[[skills]]
skill = "team/example-skill"
digest = "sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b"

$ sha256sum /tmp/ts-skills-proj.MXMmn7/.agents/ts-skills.lock
6a054deb252285fd13028035f4f77bbc73337977fd5e354e1d28c1d84c690446  /tmp/ts-skills-proj.MXMmn7/.agents/ts-skills.lock

$ cp /tmp/ts-skills-proj.MXMmn7/.agents/ts-skills.lock \
    /tmp/ts-skills-restore.dU4tSw/.agents/ts-skills.lock
$ printf 'registry = "http://127.0.0.1:35723"\n' > /tmp/ts-skills-restore.dU4tSw/config.toml
$ ./ts-skills restore --project /tmp/ts-skills-restore.dU4tSw \
    --config /tmp/ts-skills-restore.dU4tSw/config.toml
Restored locked skills.
exit=0

$ diff -r /tmp/ts-skills-proj.MXMmn7/.agents/skills \
    /tmp/ts-skills-restore.dU4tSw/.agents/skills
exit=0
```

The lock digest is the installed-tree digest: the client verifies the tree against it before writing the lock. Publication digest and installed-tree digest are therefore both `sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b`.

### 2.6 Failure shape

```console
$ ./ts-skills install --project /tmp/ts-skills-proj.MXMmn7 \
    --config /tmp/ts-skills-proj.MXMmn7/config.toml nonexistent/skill
ts-skills: cannot install because the requested skill publication was not found: fetch skill nonexistent/skill: registry value not found: requested publication does not exist
exit=1
```

### 2.7 Shutdown

```console
$ kill 1148427
$ wait 1148427
$ ss -tlnp | rg ':35723'
exit=1
```

The recorded daemon PID exited and its ephemeral port was released.

## 3. Live tailnet

No tailnet auth key was available in this session. `TS_SKILLSD_AUTHKEY_FILE` and `TS_AUTHKEY` were both unset. Per plan 001's STOP condition, this transcript ends after the successful dev baseline. Section 3 must run before plan 006 can claim tailnet parity.

## 4. Pin

```console
$ jj log -r @ --no-graph
squukypr josh@joshthomas.dev 2026-07-28 20:49:39 006e2bf8
(empty) (no description set)

$ ss -tlnp | rg ':8080'
exit=1
```

- Publication digest: `sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b`
- Installed-tree digest: `sha256:24a68d634c7c7b5460bb429e462ea577eba0e5867272f5528a15a6b36947496b`
- Archive SHA-256: `36b85731be73e6819fa01f56375eea27b44b7836c793d17bd9de712de2af0696`
- Lock SHA-256: `6a054deb252285fd13028035f4f77bbc73337977fd5e354e1d28c1d84c690446`

Port 8080 was free. The dev daemon, project directories, configuration files, and all state were under `/tmp`.
