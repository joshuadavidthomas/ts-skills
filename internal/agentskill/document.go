package agentskill

import (
	"bytes"
	"fmt"
	"io"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const Filename = "SKILL.md"

type Frontmatter struct {
	Name          Name
	Description   string
	License       *string
	Compatibility *string
	Metadata      map[string]string
	AllowedTools  *string
}

type Document struct {
	Frontmatter
	Instructions string
}

type frontmatterYAML struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       *string           `yaml:"license"`
	Compatibility *string           `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  *string           `yaml:"allowed-tools"`
}

func Parse(src []byte) (Document, error) {
	if !utf8.Valid(src) {
		return Document{}, newValidationError(ErrInvalidDocument, Filename, "must be valid UTF-8")
	}
	frontmatter, instructions, err := splitDocument(src)
	if err != nil {
		return Document{}, err
	}

	var node yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(frontmatter))
	if err := decoder.Decode(&node); err != nil {
		return Document{}, newValidationError(ErrInvalidDocument, "frontmatter", err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("contains more than one YAML document")
		}
		return Document{}, newValidationError(ErrInvalidDocument, "frontmatter", err.Error())
	}
	if err := validateFrontmatterTypes(&node); err != nil {
		return Document{}, err
	}

	var raw frontmatterYAML
	if err := node.Decode(&raw); err != nil {
		return Document{}, newValidationError(ErrInvalidDocument, "frontmatter", err.Error())
	}

	name, err := ParseName(raw.Name)
	if err != nil {
		return Document{}, newValidationError(ErrInvalidDocument, "name", err.Error())
	}
	if n := utf8.RuneCountInString(raw.Description); n < 1 || n > 1024 {
		return Document{}, newValidationError(ErrInvalidDocument, "description", "must contain 1 to 1024 Unicode scalar values")
	}
	if raw.Compatibility != nil {
		if n := utf8.RuneCountInString(*raw.Compatibility); n < 1 || n > 500 {
			return Document{}, newValidationError(ErrInvalidDocument, "compatibility", "must contain 1 to 500 Unicode scalar values when present")
		}
	}
	if raw.License != nil && *raw.License == "" {
		return Document{}, newValidationError(ErrInvalidDocument, "license", "must not be empty when present")
	}
	if raw.AllowedTools != nil && *raw.AllowedTools == "" {
		return Document{}, newValidationError(ErrInvalidDocument, "allowed-tools", "must not be empty when present")
	}

	return Document{
		Frontmatter: Frontmatter{
			Name:          name,
			Description:   raw.Description,
			License:       cloneString(raw.License),
			Compatibility: cloneString(raw.Compatibility),
			Metadata:      cloneMetadata(raw.Metadata),
			AllowedTools:  cloneString(raw.AllowedTools),
		},
		Instructions: string(instructions),
	}, nil
}

func validateFrontmatterTypes(document *yaml.Node) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return newValidationError(ErrInvalidDocument, "frontmatter", "must contain one YAML document")
	}
	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return newValidationError(ErrInvalidDocument, "frontmatter", "must be a YAML mapping")
	}
	for i := 0; i < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if key.Kind == yaml.ScalarNode && key.Value == "<<" && (key.Tag == "" || key.Tag == "!" || key.ShortTag() == "!!merge") {
			return newValidationError(ErrInvalidDocument, "frontmatter", "top-level YAML merge keys are not accepted")
		}
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return newValidationError(ErrInvalidDocument, "frontmatter", "top-level keys must be YAML string scalars")
		}
		switch key.Value {
		case "name", "description", "license", "compatibility", "allowed-tools":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return newValidationError(ErrInvalidDocument, key.Value, "must be a YAML string scalar")
			}
		case "metadata":
			if err := validateMetadataTypes(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateMetadataTypes(metadata *yaml.Node) error {
	if metadata.Kind != yaml.MappingNode {
		return newValidationError(ErrInvalidDocument, "metadata", "must be a YAML mapping")
	}
	for i := 0; i < len(metadata.Content); i += 2 {
		key, value := metadata.Content[i], metadata.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return newValidationError(ErrInvalidDocument, "metadata", "keys must be YAML string scalars")
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return newValidationError(ErrInvalidDocument, "metadata", "values must be YAML string scalars")
		}
	}
	return nil
}

func splitDocument(src []byte) ([]byte, []byte, error) {
	first, rest, ok := nextLine(src)
	if !ok || !bytes.Equal(first, []byte("---")) {
		return nil, nil, newValidationError(ErrInvalidDocument, "frontmatter", "must start with a complete --- line")
	}
	frontmatterStart := len(src) - len(rest)
	cursor := frontmatterStart
	for len(rest) > 0 {
		line, next, _ := nextLine(rest)
		consumed := len(rest) - len(next)
		if bytes.Equal(line, []byte("---")) {
			return src[frontmatterStart:cursor], next, nil
		}
		cursor += consumed
		rest = next
	}
	return nil, nil, newValidationError(ErrInvalidDocument, "frontmatter", "missing closing --- line")
}

func nextLine(src []byte) (line, rest []byte, ok bool) {
	if len(src) == 0 {
		return nil, nil, false
	}
	if i := bytes.IndexByte(src, '\n'); i >= 0 {
		line = src[:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		return line, src[i+1:], true
	}
	line = src
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil, true
}

func cloneString(src *string) *string {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func cloneMetadata(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneDocument(src Document) Document {
	src.License = cloneString(src.License)
	src.Compatibility = cloneString(src.Compatibility)
	src.AllowedTools = cloneString(src.AllowedTools)
	src.Metadata = cloneMetadata(src.Metadata)
	return src
}
