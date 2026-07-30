package server

import (
	"testing"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

func TestFlatEntitiesCarryCatalogFacts(t *testing.T) {
	namespace, err := registry.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	name, err := agentskill.ParseName("sample")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := registry.NewSkillID(namespace, name)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := newCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	publication, err := registry.NewPublicationID(skill, registry.TreeDigest{})
	if err != nil {
		t.Fatal(err)
	}
	actor := actor{ID: "user:1", Display: "Person"}
	provenance := provenance{Source: "sample", SubmittedBy: actor, SubmittedAt: time.Now()}
	got := testCandidate(candidate, skill, registry.TreeDigest{}, provenance)
	if got.Skill != skill || got.Provenance != provenance {
		t.Fatalf("candidate = %#v", got)
	}
	current := currentPublication{Publication: publication, SelectedBy: actor, SelectedAt: time.Now()}
	if current.Publication != publication {
		t.Fatalf("current = %#v", current)
	}
}

func TestCuratorCarriesAuthorizedActor(t *testing.T) {
	actor := actor{ID: "user:1", Display: "Person"}
	if got := (curator{Actor: actor}).Actor; got != actor {
		t.Fatalf("curator actor = %#v, want %#v", got, actor)
	}
}
