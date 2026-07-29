package registry

import (
	"testing"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

func TestFlatEntitiesCarryCatalogFacts(t *testing.T) {
	namespace, err := agentskill.ParseNamespace("team")
	if err != nil {
		t.Fatal(err)
	}
	name, err := agentskill.ParseName("sample")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := agentskill.NewSkillID(namespace, name)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := agentskill.NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	publication, err := agentskill.NewPublicationID(skill, agentskill.TreeDigest{})
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{ID: "user:1", Display: "Person"}
	provenance := Provenance{Source: "sample", SubmittedBy: actor, SubmittedAt: time.Now()}
	got := Candidate{ID: candidate, Skill: skill, Tree: agentskill.TreeDigest{}, Provenance: provenance}
	if got.Skill != skill || got.Provenance != provenance {
		t.Fatalf("candidate = %#v", got)
	}
	current := CurrentPublication{Publication: publication, SelectedBy: actor, SelectedAt: time.Now()}
	if current.Publication != publication {
		t.Fatalf("current = %#v", current)
	}
}

func TestCuratorCarriesAuthorizedActor(t *testing.T) {
	actor := Actor{ID: "user:1", Display: "Person"}
	if got := (Curator{Actor: actor}).Actor; got != actor {
		t.Fatalf("curator actor = %#v, want %#v", got, actor)
	}
}
