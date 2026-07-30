package agentskill

import (
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

func TestBundledExampleSkillIsValid(t *testing.T) {
	directory, err := Load(os.DirFS("../.."), "examples/example-skill")
	if err != nil {
		t.Fatal(err)
	}
	if got := directory.Document().Name.String(); got != "example-skill" {
		t.Fatalf("example name = %q", got)
	}
}
