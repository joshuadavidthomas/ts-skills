# Agent Skills specification and Go type design

This note records the open Agent Skills file format and a proposed Go model for reading, writing, and validating it. The primary source is the [Agent Skills specification](https://agentskills.io/specification), maintained in the [`agentskills/agentskills` repository](https://github.com/agentskills/agentskills/blob/main/docs/specification.mdx).

## What an Agent Skill contains

An Agent Skill packages instructions and supporting resources that an agent can load for a matching task. A skill may contain:

- Markdown instructions;
- executable scripts;
- detailed reference material;
- templates, schemas, images, and other assets; and
- arbitrary extra files and directories.

The format supports three stages of loading:

1. During discovery, a client loads each skill's `name` and `description`.
2. When a skill matches a task, the client loads the full `SKILL.md` instructions.
3. During execution, the agent reads references, uses assets, or runs scripts as needed.

The specification defines the directory's contents. Each client chooses where to discover skills, how users invoke them, which tools it supports, and which script runtimes it can execute.

## Required file layout

The smallest conforming skill is:

```text
my-skill/
└── SKILL.md
```

A conforming skill must meet these rules:

- The skill is a directory.
- The directory contains a file named exactly `SKILL.md`.
- `SKILL.md` contains YAML frontmatter followed by Markdown.
- The frontmatter `name` matches the directory basename.

The conventional full layout is:

```text
my-skill/
├── SKILL.md          # Required
├── scripts/          # Optional executable helpers
├── references/       # Optional documentation
└── assets/           # Optional templates, schemas, images, and data
```

Other files and directories are allowed. The specification does not require a manifest, README, license file, fixed heading structure, or installation path. `.agents/skills/` is a common discovery location rather than a format requirement.

## Frontmatter fields

The open format defines six top-level fields:

| Field | Required | Type and constraints |
| --- | --- | --- |
| `name` | Yes | String, 1–64 characters. Lowercase alphanumeric characters and hyphens; no leading, trailing, or consecutive hyphens. Must match the directory basename. |
| `description` | Yes | Non-empty string, 1–1024 characters. Should explain what the skill does and when a client should select it. |
| `license` | No | String containing a license name or a reference to a license file bundled with the skill. |
| `compatibility` | No | String, 1–500 characters. Records real product, runtime, package, network, or environment requirements. |
| `metadata` | No | Mapping from string keys to string values. Holds extra or client-specific data. |
| `allowed-tools` | No | Space-delimited string naming tools approved for use by the skill. This field is experimental and client support varies. |

Example:

```markdown
---
name: pdf-processing
description: Extracts text, fills forms, and combines PDF documents. Use for PDF files, scanned documents, and PDF form fields.
license: Apache-2.0
compatibility: Requires Python 3.11 and network access
metadata:
  author: example-org
  version: "1.0"
allowed-tools: Read Bash
---

# PDF processing

Inspect the input, preserve the original file, and validate the generated PDF.
```

The reference validator rejects unknown top-level fields. Portable client-specific data therefore belongs under `metadata`.

### Name character ambiguity

The written specification describes names as using Unicode lowercase alphanumeric characters, then illustrates the rule with the ASCII ranges `a-z` and `0-9`. The official reference validator follows the Unicode reading: it applies NFKC normalization and accepts lowercase Unicode letters and numbers plus hyphens. ASCII names remain the safest choice across clients.

## Markdown and supporting resources

The specification imposes no heading structure or minimum length on the Markdown body. It recommends step-by-step instructions, examples, edge cases, and checks when those details help an agent perform the task.

Its progressive-disclosure guidance recommends:

- keeping `SKILL.md` below 500 lines;
- keeping the instructions below about 5,000 tokens;
- moving optional detail into supporting files;
- linking supporting files with paths relative to the skill root; and
- keeping references shallow instead of chaining nested reference files.

These are authoring recommendations rather than validator-enforced conformance rules.

Files under `scripts/` should document dependencies, handle expected edge cases, and report useful errors. Files under `references/` should stay focused. Files under `assets/` may contain any static material the skill needs. Script-language support depends on the client.

## Validation and implementation differences

The project publishes a reference validator:

```console
pip install skills-ref
skills-ref validate ./my-skill
```

It checks frontmatter and naming rules, but its README calls it a demonstration implementation. It does not assess instruction quality, supporting-resource links, scripts, assets, line counts, or token counts.

The implementation also differs from the written specification in a few places:

- It accepts lowercase `skill.md`, while the specification requires exactly `SKILL.md`.
- Its Unicode name handling is broader than the ASCII ranges shown in the prose.
- It does not enforce every declared optional-field type and length.

A client successfully loading a skill does not prove that the skill conforms to the open format. A strict implementation should follow the published specification and record its choice where the specification is ambiguous.

## Client and vendor extensions

The following concerns sit outside the open file format:

- `.agents/skills/`, `.claude/skills/`, and other discovery paths;
- project-versus-user precedence and collision handling;
- slash commands, `$skill`, and `@skill` invocation;
- prompt catalog encodings;
- vendor fields such as `model`, `hooks`, `agent`, or `user-invocable`; and
- OpenAI's optional `agents/openai.yaml` file.

The open specification includes `allowed-tools`, but marks it experimental. Tool-name grammar and permission behavior remain client-specific.

# Proposed Go model

The Go model should separate one `SKILL.md` document from the directory that contains it. Parsing can validate the document by itself; loading the directory adds the filename and parent-name checks.

```go
package agentskill

import "io/fs"

const Filename = "SKILL.md"

// Frontmatter mirrors the six fields in the Agent Skills specification.
type Frontmatter struct {
	Name          string
	Description   string
	License       *string
	Compatibility *string
	Metadata      map[string]string
	AllowedTools  *string
}

// Document is one parsed SKILL.md file.
type Document struct {
	Frontmatter
	Instructions string
}

// Directory is a validated skill directory. Its filesystem is rooted at the
// skill directory, so callers address supporting files relative to that root.
type Directory struct {
	document Document
	files    fs.FS
}

func (d Directory) Document() Document
func (d Directory) Open(name string) (fs.File, error)
```

`Directory` can implement `fs.FS`, which lets callers use the standard library for arbitrary resources:

```go
body, err := fs.ReadFile(skillDirectory, "references/api.md")
err = fs.WalkDir(skillDirectory, ".", walk)
```

There is no need for `Script`, `Reference`, or `Asset` types. Supporting resources may be text, executable code, binary images, schemas, or nested directories. `fs.FS` preserves that open shape without loading every file into memory.

## Operations

The package needs four main operations:

```go
// Parse parses and validates a SKILL.md document. It cannot check whether the
// skill name matches the parent directory.
func Parse(src []byte) (Document, error)

// Marshal validates a document and emits canonical YAML frontmatter followed
// by its Markdown instructions.
func Marshal(doc Document) ([]byte, error)

// Load reads dir/SKILL.md, validates it, and checks its name against dir.
func Load(fsys fs.FS, dir string) (Directory, error)

// LoadDir adapts an operating-system path to Load.
func LoadDir(path string) (Directory, error)
```

`Marshal` need not preserve YAML comments, key order, quoting, folded scalars, or the original spacing between tool names. It should preserve the instruction text and produce one canonical representation. Exact lexical round trips would require retaining the source bytes or a YAML syntax tree and should wait for a concrete caller.

## Optional fields

Pointers preserve whether an optional scalar appeared:

```go
License       *string
Compatibility *string
AllowedTools  *string
```

That distinction matters for `compatibility`: omission is valid, while a present empty string violates its 1–500 character rule. A nil `Metadata` map naturally records omission.

`allowed-tools` should remain a string in the core type. The specification defines the wire value as an experimental string and leaves tool-name semantics to clients. A helper may offer a tokenized view without changing the stored value:

```go
func (f Frontmatter) Tools() []string {
	if f.AllowedTools == nil {
		return nil
	}
	return strings.Fields(*f.AllowedTools)
}
```

## YAML boundary type

The package should keep YAML tags out of the domain type and decode through a private boundary type:

```go
type frontmatterYAML struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       *string           `yaml:"license"`
	Compatibility *string           `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  *string           `yaml:"allowed-tools"`
}
```

Using `map[string]string` rejects nested, numeric, boolean, and sequence metadata values. A `yaml.v3.Decoder` with `KnownFields(true)` rejects unknown top-level fields. The parser should also reject duplicate keys and trailing YAML documents.

The complete file is not a YAML document. `Parse` should locate frontmatter delimiters as complete lines, decode only the bytes between them, and preserve everything after the closing delimiter as Markdown. Splitting on every `---` sequence would break valid Markdown bodies.

## Validation errors

Validation should report all independent field faults in one pass:

```go
type Problem struct {
	Field   string
	Message string
}

type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string
```

Callers can inspect it with `errors.As`. Malformed delimiters and YAML should wrap one format sentinel:

```go
var ErrInvalidFormat = errors.New("invalid SKILL.md format")
```

Filesystem operations should wrap their errors without hiding `fs.ErrNotExist`, permission failures, or other causes.

## `Parse` checks

`Parse` should:

1. require valid UTF-8;
2. require `---` as the first complete line;
3. require a closing `---` line;
4. decode exactly one YAML mapping;
5. reject duplicate and unknown keys;
6. enforce each field's YAML type;
7. require `name` and `description`;
8. validate field lengths and name syntax;
9. preserve the Markdown after the closing delimiter; and
10. allow an empty Markdown body.

For compatibility with the official validator, name validation should apply Unicode NFKC normalization, count Unicode code points, and accept lowercase Unicode letters and numbers separated by single internal hyphens. Validation should preserve the source value rather than silently rewriting it.

## `Load` checks

`Load` adds the rules that need directory context:

1. the path names a directory;
2. the directory contains an entry named exactly `SKILL.md`;
3. the normalized document name matches the normalized directory basename; and
4. arbitrary extra files and directories remain valid.

Reading the directory entries before opening `SKILL.md` prevents a case-insensitive filesystem from accepting a file stored as `skill.md`.

An `os.DirFS` value does not confine symlinks to its root. A registry that accepts untrusted skill directories will need a separate ingestion policy for symlinks, file counts, file sizes, and copied storage. Those security rules are registry policy, not Agent Skills format rules.

## Deliberate omissions

The first implementation should omit:

- parser and validator interfaces;
- registry identifiers or activation state;
- typed license identifiers;
- resource subclasses;
- a YAML syntax tree;
- lowercase filename fallbacks;
- vendor-specific top-level fields; and
- compatibility aliases for nonconforming shapes.

This keeps the package centered on reading, writing, and validating the open Agent Skills directory format.

## Sources

- [Agent Skills specification](https://agentskills.io/specification)
- [Specification source](https://github.com/agentskills/agentskills/blob/main/docs/specification.mdx)
- [Agent Skills repository overview](https://github.com/agentskills/agentskills)
- [Client implementation guide](https://agentskills.io/client-implementation/adding-skills-support)
- [Reference parser and validator](https://github.com/agentskills/agentskills/tree/main/skills-ref)
- [Claude Code skills](https://code.claude.com/docs/en/skills)
- [OpenAI Codex skills](https://developers.openai.com/codex/skills/)
