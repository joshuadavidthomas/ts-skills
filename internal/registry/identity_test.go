package registry

import (
	"testing"
	"unicode"
)

func TestNamespaceValidation(t *testing.T) {
	for _, namespace := range []string{"", ".", "..", "team/other"} {
		if _, err := ParseNamespace(namespace); err == nil {
			t.Errorf("ParseNamespace(%q) succeeded", namespace)
		}
	}
}

func TestNamespaceRejectsUnicodeWhitespace(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.IsSpace(r) {
			continue
		}
		for _, namespace := range []string{string(r) + "team", "te" + string(r) + "am", "team" + string(r)} {
			if _, err := ParseNamespace(namespace); err == nil {
				t.Errorf("ParseNamespace(%q) succeeded", namespace)
			}
		}
	}
}
