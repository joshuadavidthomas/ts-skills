package agentskill

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestParseNameCanonicalizesNFKC(t *testing.T) {
	plain, err := ParseName("hello")
	if err != nil {
		t.Fatal(err)
	}
	fullwidth, err := ParseName("ｈｅｌｌｏ")
	if err != nil {
		t.Fatal(err)
	}
	if plain != fullwidth || fullwidth.String() != "hello" {
		t.Fatalf("canonical names differ: %#v and %#v", plain, fullwidth)
	}
}

func TestParseNameRejectsInvalidShapes(t *testing.T) {
	for _, name := range []string{"", "Upper", "-start", "end-", "two--hyphens", "has space", "a/b"} {
		if _, err := ParseName(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ParseName(%q) error = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestParseRejectsNonStringStandardFields(t *testing.T) {
	fields := []string{"name", "description", "license", "compatibility", "allowed-tools"}
	wrongTypes := []struct {
		name  string
		value string
	}{
		{name: "numeric", value: "123"},
		{name: "boolean", value: "true"},
		{name: "sequence", value: "[one, two]"},
		{name: "mapping", value: "{nested: value}"},
		{name: "null", value: "null"},
	}

	for _, field := range fields {
		for _, wrongType := range wrongTypes {
			t.Run(field+"/"+wrongType.name, func(t *testing.T) {
				values := map[string]string{
					"name":          "sample",
					"description":   "A sample",
					"license":       "MIT",
					"compatibility": "Local agents",
					"allowed-tools": "Bash Read",
				}
				values[field] = wrongType.value
				source := fmt.Sprintf("---\nname: %s\ndescription: %s\nlicense: %s\ncompatibility: %s\nallowed-tools: %s\n---\n", values["name"], values["description"], values["license"], values["compatibility"], values["allowed-tools"])

				_, err := Parse([]byte(source))
				requireInvalidDocumentField(t, err, field)
			})
		}
	}
}

func TestParseRejectsNonMappingMetadata(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "string", value: "owner"},
		{name: "numeric", value: "123"},
		{name: "boolean", value: "true"},
		{name: "sequence", value: "[owner, team]"},
		{name: "null", value: "null"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fmt.Sprintf("---\nname: sample\ndescription: A sample\nmetadata: %s\n---\n", test.value)
			_, err := Parse([]byte(source))
			requireInvalidDocumentField(t, err, "metadata")
		})
	}
}

func TestParseRejectsNonStringMetadataEntries(t *testing.T) {
	wrongTypes := []struct {
		name  string
		value string
	}{
		{name: "numeric", value: "123"},
		{name: "boolean", value: "true"},
		{name: "sequence", value: "[team]"},
		{name: "mapping", value: "{team: core}"},
		{name: "null", value: "null"},
	}
	for _, entry := range []string{"key", "value"} {
		for _, wrongType := range wrongTypes {
			t.Run(entry+"/"+wrongType.name, func(t *testing.T) {
				key, value := "owner", "team"
				if entry == "key" {
					key = wrongType.value
				} else {
					value = wrongType.value
				}
				source := fmt.Sprintf("---\nname: sample\ndescription: A sample\nmetadata: {%s: %s}\n---\n", key, value)
				_, err := Parse([]byte(source))
				requireInvalidDocumentField(t, err, "metadata")
			})
		}
	}
}

func TestParseRejectsTopLevelYAMLMergeKeys(t *testing.T) {
	for _, mergeKey := range []string{"<<", "!!merge '<<'", "! <<"} {
		t.Run(mergeKey, func(t *testing.T) {
			source := fmt.Sprintf("---\ndefaults: &defaults\n  description: 123\nname: sample\n%s: *defaults\n---\n", mergeKey)
			_, err := Parse([]byte(source))
			requireInvalidDocumentField(t, err, "frontmatter")
		})
	}
}

func TestParseRejectsAliasedTopLevelKeys(t *testing.T) {
	source := []byte("---\nfield: &field description\nname: sample\n*field: 123\n---\n")
	_, err := Parse(source)
	requireInvalidDocumentField(t, err, "frontmatter")
}

func TestParseAllowsQuotedMergeLikeUnknownField(t *testing.T) {
	document, err := Parse([]byte("---\nname: sample\ndescription: A sample\n\"<<\": {description: 123}\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if document.Description != "A sample" {
		t.Fatalf("description = %q", document.Description)
	}
}

func TestParseAllowsUnknownFrontmatterNodeTypes(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "numeric", value: "123"},
		{name: "boolean", value: "true"},
		{name: "sequence", value: "[one, two]"},
		{name: "mapping", value: "{nested: true}"},
		{name: "null", value: "null"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fmt.Sprintf("---\nname: sample\ndescription: A sample\nvendor-field: %s\nmetadata: {\"123\": \"true\"}\n---\n", test.value)
			document, err := Parse([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if document.Metadata["123"] != "true" {
				t.Fatalf("metadata = %#v", document.Metadata)
			}
		})
	}
}

func TestParseAllowsUnknownFrontmatterAndDirectoryPreservesBytes(t *testing.T) {
	source := []byte("---\nname: sample\ndescription: A sample\nvendor-number: 123\nvendor-enabled: true\nvendor-items: [one, two]\nvendor-field:\n  nested: true\nvendor-empty: null\nmetadata:\n  owner: team\nlicense: MIT\n---\n# Keep this exact\n")
	files := fstest.MapFS{"sample/SKILL.md": &fstest.MapFile{Data: source}}
	directory, err := Load(files, "sample")
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(directory.FS(), Filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(source) {
		t.Fatalf("SKILL.md bytes changed:\n%s", got)
	}

	first := directory.Document()
	*first.License = "changed"
	first.Metadata["owner"] = "changed"
	second := directory.Document()
	if *second.License != "MIT" || second.Metadata["owner"] != "team" {
		t.Fatalf("Document returned mutable aliases: %#v", second)
	}
}

func requireInvalidDocumentField(t *testing.T, err error, field string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("error = %v, want ErrInvalidDocument", err)
	}
	var validationError *ValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if validationError.Field != field {
		t.Fatalf("validation field = %q, want %q", validationError.Field, field)
	}
}

func TestLoadRequiresExactFilenameAndMatchingDirectory(t *testing.T) {
	document := []byte("---\nname: right\ndescription: test\n---\n")
	_, err := Load(fstest.MapFS{"right/skill.md": &fstest.MapFile{Data: document}}, "right")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lowercase filename error = %v, want fs.ErrNotExist", err)
	}
	_, err = Load(fstest.MapFS{"wrong/SKILL.md": &fstest.MapFile{Data: document}}, "wrong")
	if !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("mismatched directory error = %v, want ErrInvalidTree", err)
	}
}

func TestTreeDigestTextIsStrict(t *testing.T) {
	var digest TreeDigest
	roundTrip, err := ParseTreeDigest(digest.String())
	if err != nil || roundTrip != digest {
		t.Fatalf("ParseTreeDigest round trip = %v, %v", roundTrip, err)
	}
	for _, text := range []string{"", "SHA256:" + digest.String()[7:], "sha256:ABCDEF", "sha256:" + "A000000000000000000000000000000000000000000000000000000000000000"} {
		if _, err := ParseTreeDigest(text); !errors.Is(err, ErrInvalidTreeDigest) {
			t.Errorf("ParseTreeDigest(%q) error = %v", text, err)
		}
	}
}

func TestSumTreeFixedVectors(t *testing.T) {
	tests := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{name: "one file", fsys: fstest.MapFS{"root/a.txt": &fstest.MapFile{Data: []byte("alpha")}}, want: "sha256:4ee3caf80c2628f843ba3d4e3def66d64f6e070389c8327828b4c809828c16b2"},
		{name: "multiple files", fsys: fstest.MapFS{"root/z.txt": &fstest.MapFile{Data: []byte("last")}, "root/a.txt": &fstest.MapFile{Data: []byte("first")}}, want: "sha256:2569f5dfc068d1860fab107038ae3f365e3115b5bcd87d64d0a41223d22d4f33"},
		{name: "multibyte path", fsys: fstest.MapFS{"root/café/雪.txt": &fstest.MapFile{Data: []byte("data")}}, want: "sha256:0c8256073984f16961c34901117b5f7dc84c693354d6e52657e9962b126911fb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			digest, err := SumTree(context.Background(), test.fsys, "root")
			if err != nil {
				t.Fatal(err)
			}
			if digest.String() != test.want {
				t.Fatalf("digest = %q, want %q", digest.String(), test.want)
			}
		})
	}
}

func TestBundledExampleSkillIsValid(t *testing.T) {
	directory, err := Load(os.DirFS("../.."), "examples/example-skill")
	if err != nil {
		t.Fatal(err)
	}
	if got := directory.Document().Name.String(); got != "example-skill" {
		t.Fatalf("example name = %q", got)
	}
}

func TestSumTreeRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files := fstest.MapFS{"root/a.txt": &fstest.MapFile{Data: []byte("alpha")}}
	digest, err := SumTree(ctx, files, "root")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SumTree with cancelled context error = %v", err)
	}
	if digest != (TreeDigest{}) {
		t.Fatalf("SumTree with cancelled context digest = %s, want zero", digest.String())
	}
}

func TestInspectBindsDocumentAndDigest(t *testing.T) {
	files := fstest.MapFS{
		"right/SKILL.md":       &fstest.MapFile{Data: []byte("---\nname: right\ndescription: test\n---\n")},
		"right/scripts/run.sh": &fstest.MapFile{Data: []byte("#!/bin/sh\n")},
	}
	want, err := SumTree(context.Background(), files, "right")
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), files, "right")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Digest() != want {
		t.Fatalf("Inspect digest = %s, want %s", inspection.Digest().String(), want.String())
	}
	if got := inspection.Document().Name.String(); got != "right" {
		t.Fatalf("Inspect document name = %q, want %q", got, "right")
	}
	if _, err := inspection.Directory().FS().Open("scripts/run.sh"); err != nil {
		t.Fatalf("Inspect directory FS open tree file: %v", err)
	}
	if err := inspection.RequireName(inspection.Document().Name); err != nil {
		t.Fatalf("RequireName(tree's own name) = %v", err)
	}
	other, err := ParseName("other")
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.RequireName(other); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("RequireName(other) error = %v, want ErrInvalidTree", err)
	}
}

func TestInspectRejectsLoadAndTreeFailures(t *testing.T) {
	document := &fstest.MapFile{Data: []byte("---\nname: root\ndescription: test\n---\n")}
	if _, err := Inspect(context.Background(), fstest.MapFS{"root/other.md": document}, "root"); err == nil {
		t.Fatal("Inspect without SKILL.md error = nil")
	}
	unsafe := fstest.MapFS{
		"root/SKILL.md":   document,
		"root/evil\\.txt": &fstest.MapFile{Data: []byte("x")},
	}
	if _, err := Inspect(context.Background(), unsafe, "root"); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("Inspect with unsafe entry error = %v, want ErrInvalidTree", err)
	}
}

func TestInspectRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files := fstest.MapFS{"root/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: root\ndescription: test\n---\n")}}
	if _, err := Inspect(ctx, files, "root"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect with cancelled context error = %v, want context.Canceled", err)
	}
}

func TestSumTreeIgnoresModesAndMapOrder(t *testing.T) {
	first := fstest.MapFS{
		"root/a": &fstest.MapFile{Data: []byte("a"), Mode: 0o600},
		"root/b": &fstest.MapFile{Data: []byte("b"), Mode: 0o644},
	}
	second := fstest.MapFS{
		"root/b": &fstest.MapFile{Data: []byte("b"), Mode: 0o400},
		"root/a": &fstest.MapFile{Data: []byte("a"), Mode: 0o777},
	}
	one, err := SumTree(context.Background(), first, "root")
	if err != nil {
		t.Fatal(err)
	}
	two, err := SumTree(context.Background(), second, "root")
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("digest depends on order or permissions: %s != %s", one, two)
	}
}
