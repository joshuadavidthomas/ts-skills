package registry

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

func stageInspectionTree(t *testing.T, files fstest.MapFS) *tree.Snapshot {
	t.Helper()
	snapshot, err := tree.Stage(context.Background(), t.TempDir(), ".inspect-", files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	return snapshot
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
	inspection, err := Inspect(context.Background(), stageInspectionTree(t, files), "right")
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
	skill, err := ParseSkillID("team/right")
	if err != nil {
		t.Fatal(err)
	}
	publication, err := NewPublicationID(skill, inspection.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Verify(publication); err != nil {
		t.Fatalf("Verify(tree's publication) = %v", err)
	}
	other, err := ParseSkillID("team/other")
	if err != nil {
		t.Fatal(err)
	}
	mismatch, err := NewPublicationID(other, inspection.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Verify(mismatch); !errors.Is(err, ErrPublicationMismatch) {
		t.Fatalf("Verify(other publication) error = %v, want ErrPublicationMismatch", err)
	}
}

func TestInspectRejectsLoadAndTreeFailures(t *testing.T) {
	document := &fstest.MapFile{Data: []byte("---\nname: root\ndescription: test\n---\n")}
	missing := stageInspectionTree(t, fstest.MapFS{"root/other.md": document})
	if _, err := Inspect(context.Background(), missing, "root"); err == nil {
		t.Fatal("Inspect without SKILL.md error = nil")
	}
	if _, err := Inspect(context.Background(), nil, "root"); err == nil {
		t.Fatal("Inspect with nil snapshot error = nil")
	}
}

func TestSumTreeUsesPortableTreeValidation(t *testing.T) {
	files := fstest.MapFS{
		"root/A.txt": &fstest.MapFile{Data: []byte("first")},
		"root/a.txt": &fstest.MapFile{Data: []byte("second")},
	}
	if _, err := SumTree(context.Background(), files, "root"); !errors.Is(err, agentskill.ErrInvalidTree) {
		t.Fatalf("SumTree portable-path collision error = %v, want ErrInvalidTree", err)
	}
}

func TestInspectRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files := fstest.MapFS{"root/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: root\ndescription: test\n---\n")}}
	if _, err := Inspect(ctx, stageInspectionTree(t, files), "root"); !errors.Is(err, context.Canceled) {
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
