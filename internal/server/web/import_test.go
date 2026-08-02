package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRepositoryURL(t *testing.T) {
	for _, valid := range []string{
		"https://github.com/example/skills.git",
		"https://git.example.test/team/skills",
	} {
		if err := validateRepositoryURL(valid); err != nil {
			t.Errorf("validateRepositoryURL(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"", "http://github.com/example/skills", "file:///tmp/skills", "git@github.com:example/skills.git",
		"https://user:secret@example.test/skills", "https://example.test/skills#main",
	} {
		if err := validateRepositoryURL(invalid); err == nil {
			t.Errorf("validateRepositoryURL(%q) succeeded, want error", invalid)
		}
	}
}

func TestDiscoverRepositorySkillsFindsOnlyValidSkills(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "skills/alpha/SKILL.md", "---\nname: alpha\ndescription: First skill\n---\n# Alpha\n")
	writeTestFile(t, root, "nested/beta/SKILL.md", "---\nname: beta\ndescription: Second skill\n---\n# Beta\n")
	writeTestFile(t, root, "broken/SKILL.md", "not a skill")
	writeTestFile(t, root, "node_modules/ignored/SKILL.md", "---\nname: ignored\ndescription: Ignored skill\n---\n# Ignored\n")

	skills, err := discoverRepositorySkills(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills = %#v, want two valid skills", skills)
	}
	if skills[0].Path != "nested/beta" || skills[1].Path != "skills/alpha" {
		t.Fatalf("skills = %#v, want path-sorted discoveries", skills)
	}
	if skills[1].Description != "First skill" {
		t.Fatalf("alpha description = %q", skills[1].Description)
	}
}

func TestDiscoverRepositorySkillsRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := discoverRepositorySkills(ctx, t.TempDir()); err == nil {
		t.Fatal("cancelled discovery succeeded")
	}
}

func writeTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
