package registry

import (
	"strings"
	"testing"
)

func TestNamespaceValidation(t *testing.T) {
	for _, namespace := range []string{"team", "team-1", "a", strings.Repeat("a", 64)} {
		parsed, err := ParseNamespace(namespace)
		if err != nil {
			t.Errorf("ParseNamespace(%q): %v", namespace, err)
		} else if parsed.String() != namespace {
			t.Errorf("ParseNamespace(%q) = %q", namespace, parsed.String())
		}
	}
	for _, namespace := range []string{"", ".", "..", "team/other", "Team", "-team", "team-", "team_name", "téam", "te\u202eam", strings.Repeat("a", 65)} {
		if _, err := ParseNamespace(namespace); err == nil {
			t.Errorf("ParseNamespace(%q) succeeded", namespace)
		}
	}
}
