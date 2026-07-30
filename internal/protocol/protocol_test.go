package protocol

import (
	"errors"
	"net/http"
	"testing"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

func TestPublicationRoundTrip(t *testing.T) {
	skill, err := registry.ParseSkillID("team/sample")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := registry.ParseTreeDigest("sha256:6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d")
	if err != nil {
		t.Fatal(err)
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		t.Fatal(err)
	}

	if got := CurrentPath(skill); got != "/api/v1/skills/team/sample/current" {
		t.Fatalf("CurrentPath = %q", got)
	}
	if got := TreePath(publication); got != "/api/v1/skills/team/sample/publications/"+digest.String()+"/tree.zip" {
		t.Fatalf("TreePath = %q", got)
	}
	if got, err := ParseCurrentResponse(NewCurrentResponse(publication)); err != nil || got != publication {
		t.Fatalf("current response round trip = %v, %v", got, err)
	}
	header := make(http.Header)
	SetPublicationHeaders(header, publication)
	if got, err := ParsePublicationHeaders(header); err != nil || got != publication {
		t.Fatalf("publication header round trip = %v, %v", got, err)
	}
}

func TestRejectsNoncanonicalWireIdentity(t *testing.T) {
	if _, err := ParseSkill("team", "SAMPLE"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseSkill error = %v, want %v", err, ErrInvalid)
	}
	response := CurrentResponse{
		Namespace: "team",
		Name:      "sample",
		Digest:    "SHA256:6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d",
	}
	if _, err := ParseCurrentResponse(response); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Publication error = %v, want %v", err, ErrInvalid)
	}
}

func TestStatusForCodeIncludesTransientFailure(t *testing.T) {
	for code, want := range map[string]int{
		CodeNotFound:       http.StatusNotFound,
		CodeInvalidRequest: http.StatusBadRequest,
		CodeTooLarge:       http.StatusRequestEntityTooLarge,
		CodeInternal:       http.StatusInternalServerError,
		CodeUnavailable:    http.StatusServiceUnavailable,
	} {
		if got, ok := StatusForCode(code); !ok || got != want {
			t.Fatalf("StatusForCode(%q) = %d, %v; want %d, true", code, got, ok, want)
		}
	}
}
