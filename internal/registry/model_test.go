package registry

import (
	"testing"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

func testValues(t *testing.T) (SkillID, CandidateID, Actor, Provenance) {
	t.Helper()
	namespace, _ := ParseNamespace("team")
	name, _ := agentskill.ParseName("sample")
	skill, _ := NewSkillID(namespace, name)
	candidate, err := NewCandidateID()
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := NewActor("user:1", "Person")
	source, _ := NewUploadSource("sample")
	provenance, _ := NewProvenance(source, actor, time.Now())
	return skill, candidate, actor, provenance
}

func TestIdentityAndEntityInvariants(t *testing.T) {
	skill, candidateID, actor, provenance := testValues(t)
	publicationID, err := NewPublicationID(skill, agentskill.TreeDigest{})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := NewCandidate(candidateID, skill, agentskill.TreeDigest{}, provenance)
	if err != nil || candidate.Skill() != skill {
		t.Fatalf("candidate = %#v, %v", candidate, err)
	}
	publication, err := NewPublication(publicationID, candidateID, actor, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPublishResult(publication, false, true); err == nil {
		t.Fatal("existing publication became first current")
	}
	otherName, _ := agentskill.ParseName("other")
	otherSkill, _ := NewSkillID(skill.Namespace(), otherName)
	if _, err := NewSkillSummary(otherSkill, publicationID); err == nil {
		t.Fatal("summary accepted another skill's current publication")
	}
}

func TestNamespaceAndActorValidation(t *testing.T) {
	for _, namespace := range []string{"", ".", "..", " team", "team/other", "team\n"} {
		if _, err := ParseNamespace(namespace); err == nil {
			t.Errorf("ParseNamespace(%q) succeeded", namespace)
		}
	}
	if _, err := NewActor("id\x00", "display"); err == nil {
		t.Fatal("actor accepted NUL")
	}
}
