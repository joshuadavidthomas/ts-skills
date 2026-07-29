# Plan 003: Flatten value ceremony; move shared identity into agentskill

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving on.
> If anything in "STOP conditions" occurs, stop and write a handback —
> do not improvise. When done, update this plan's status row in
> plans/consolidation/README.md.
>
> **Drift check (run first)**:
> `jj diff --from 2037ced944acc38456c090ff62e74c9de099318d --to @ -- internal/registry/ internal/agentskill/ internal/install/ internal/storage/ internal/web/ internal/tailnet/ internal/client/ internal/upload/`
> 002 lands before this plan, so `internal/install` *will* have drifted;
> that is expected. For every other in-scope package, compare the
> "Current state" excerpts against the live code; on a mismatch, treat it
> as a STOP condition.

## Status

- **Effort**: L
- **Risk**: MED (wide but mechanical; compiler-driven; zero behavior change)
- **Depends on**: 001 (baseline); sequence after 002 to avoid churning
  files 002 deletes
- **Planned at**: revision `2037ced944acc38456c090ff62e74c9de099318d`, 2026-07-28

## Why this matters

Two problems, one pass.

**Ceremony.** `internal/registry/identity.go` is 218 lines and 29
functions, nearly all constructor+getter wrappers:

```go
func NewCurator(actor Actor) Curator { return Curator{actor: actor} }
func (c Curator) Actor() Actor       { return c.actor }
func (a Actor) ID() string           { return a.id }
func (a Actor) Display() string      { return a.display }
```

`entities.go` repeats the pattern for `Candidate`, `Publication`,
`CurrentPublication`, `SkillSummary` — private fields, copying
constructors, one-line getters. Consumers pay in chains like
`x.Publication().Skill().Name().String()`. golink's equivalent is
`type Link struct { Short, Long, Owner string }`. The ceremony roughly
doubles every data type and buys nothing where the values are produced by
trusted code.

**Dependency direction.** `registry` is imported by 7 packages including
client-side ones (`install` imports it 6×, `client` 1×) purely for the
identity vocabulary (`SkillID`, `PublicationID`, `Namespace`,
`CandidateID`). After plans 004/005, client code must not import server
code — so the *shared vocabulary* must move to `agentskill`, the package
that already owns `Name` and `TreeDigest`, before the merges.

## The rule (write it once, apply it everywhere)

**A type keeps a validating constructor and private fields only if it
parses untrusted input** — HTTP requests, uploads, config files, CLI
args, DB rows, lock files. Values produced only by trusted code become
plain structs with exported fields. This is the tsidp/golink line:
`http.Handler` and parse-at-the-boundary, nothing else.

## Current state → target

### Moves (registry → agentskill), keeping validation

These parse untrusted text (URLs, lock files, DB rows) and keep their
parser + canonical `String()` forms **byte-identical**:

- `Namespace`, `ParseNamespace` (identity.go)
- `SkillID`, `NewSkillID`, `ParseSkillID`
- `PublicationID`, `NewPublicationID`
- `CandidateID`, `NewCandidateID`, `CandidateIDFromBytes`,
  `ParseCandidateID`, `IsZero`, `Bytes` (representation invariants from
  hardening 017 — keep whole)
- `validateBoundedText`, `canonicalTime` helpers as needed

New home: `internal/agentskill/identity.go`. These may keep private
fields — they are parsed types under the rule.

### Flattens (stay in registry, lose ceremony)

Produced only by trusted server code:

- `Actor{ID, Display string}` — exported fields; keep a
  `validateBoundedText` check where an Actor is first built *from a
  request* (tailnet resolver), not in a constructor every caller pays.
- `Curator{Actor Actor}` — stays a distinct type (it is the capability
  evidence from hardening 009; a bare Actor must not satisfy a Curator
  parameter) but with an exported field and no getter.
- `Provenance{Source, SubmittedBy, SubmittedAt}` — exported fields;
  `canonicalTime` applied where storage writes/reads, not via constructor.
- `UploadSource` → `string` field on Provenance unless a real invariant
  exists beyond bounded text (audit the parser; expect none).
- `Candidate`, `Publication`, `CurrentPublication`, `SkillSummary`
  (entities.go:10-81) — exported fields; delete `NewX` + getters. The
  places that *scan these from DB rows* (`storage/records.go:31,92`) keep
  their validation inline — that is the actual untrusted boundary.

### Flattens (install)

- `LockedSkill`, `FetchedSkill` (model.go) — exported fields; keep
  validation where the *lock file is parsed* (`lock.go`), not on every
  construction.

## Steps

1. Create `internal/agentskill/identity.go`; move the parsed types listed
   above verbatim (including their tests, moved from
   `registry/model_test.go` / `identity` tests). `registry` re-exports
   nothing — every consumer's import flips to `agentskill` in the same
   change (mechanical, compiler-driven; a scripted rewrite is fine, and
   per repo rule the script is deleted after use).
2. Flatten the registry-resident types per the table; update all use
   sites (`storage`, `web`, `tailnet`, `upload`, `cli`, `client`,
   `install`). Getter-chain call sites become field accesses.
3. Flatten install's `LockedSkill`/`FetchedSkill`.
4. Move row-scan validation inline at `records.go` scan sites; request
   validation inline at the tailnet resolver.
5. Golden checks: lock-file fixture bytes from 002's test unchanged;
   digest vectors unchanged; DB fixtures from `storage/catalog_test.go`
   unchanged (identity `String()`/blob forms are on-disk contracts).

## Verification

- `go build ./...` && `go test -race ./... -count=1` && `just check`
- `rg 'func New(Actor|Curator|Provenance|Candidate|Publication|CurrentPublication|SkillSummary)\b' internal/`
  → no hits.
- `rg 'func \([a-z] (Actor|Curator|Provenance|Candidate|Publication|CurrentPublication|SkillSummary)\) [A-Z]' internal/`
  → no getter methods remain on flattened types.
- `rg '"github.com/joshuadavidthomas/ts-skills/internal/registry"' internal/install/ internal/client/`
  → no hits (client side no longer imports registry).
- `wc -l internal/registry/*.go` recorded (identity.go gone; entities.go
  expected under ~60 lines).
- Dev-mode golden path (001 §2) re-run: lock hash + publication digest
  match baseline exactly.

## STOP conditions

- Any on-disk or on-wire byte changes: lock fixture, digest vectors, DB
  blob/text encodings, API JSON shapes. This plan is representation-
  neutral by definition.
- A flattened type turns out to have a *load-bearing* invariant that
  trusted producers actually violate (a test fails because a zero value
  now flows somewhere a constructor used to reject) — stop and record
  it; the fix is validation at the real boundary, not restoring ceremony,
  but that decision goes in the handback, not the diff.
- The move creates an import cycle (`agentskill` must not import
  `registry` or `storage`; it may import `safetree`).

## Maintenance notes

After this plan, `registry` contains only server-side catalog rules and
flat entity structs — which is exactly what makes 004's merge a file
move. If a new identifier type is ever added, it goes in
`agentskill/identity.go` *only if both binaries parse it*; otherwise it
is a plain struct where it is used.
