package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAcceptsOnlyOneStrictRegistryOrigin(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantURL string
	}{
		{name: "https", body: `registry = "https://registry.example.ts.net/"`, wantURL: "https://registry.example.ts.net"},
		{name: "IPv4 loopback", body: `registry = "http://127.0.0.1:8080"`, wantURL: "http://127.0.0.1:8080"},
		{name: "IPv6 loopback", body: `registry = "http://[::1]:8080"`, wantURL: "http://[::1]:8080"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Registry == nil || loaded.Registry.String() != test.wantURL {
				t.Fatalf("registry = %v, want %s", loaded.Registry, test.wantURL)
			}
		})
	}
}

func TestLoadRejectsUnknownMissingAndNonOriginRegistryValues(t *testing.T) {
	invalid := []string{
		``,
		`other = "https://registry.example.ts.net"`,
		"registry = \"https://registry.example.ts.net\"\nother = true\n",
		`registry = "http://registry.example.ts.net"`,
		`registry = "ftp://registry.example.ts.net"`,
		`registry = "https://user@registry.example.ts.net"`,
		`registry = "https://registry.example.ts.net/path"`,
		`registry = "https://registry.example.ts.net?query=yes"`,
		`registry = "https://registry.example.ts.net#fragment"`,
		`registry = "/relative"`,
	}
	for _, body := range invalid {
		name := body
		if name == "" {
			name = "empty"
		}
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("accepted config %q", body)
			}
		})
	}
}

func TestDefaultPathUsesTSkillsConfigFile(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "config.toml" || filepath.Base(filepath.Dir(path)) != "ts-skills" {
		t.Fatalf("default path = %q", path)
	}
}
