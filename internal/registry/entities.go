package registry

import (
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

type Actor struct {
	ID      string
	Display string
}

// Curator is an Actor authorized to mutate the catalog.
type Curator struct{ Actor Actor }

type Provenance struct {
	Source      string
	SubmittedBy Actor
	SubmittedAt time.Time
}

type Candidate struct {
	ID         agentskill.CandidateID
	Skill      agentskill.SkillID
	Tree       agentskill.TreeDigest
	Provenance Provenance
}

type Publication struct {
	ID          agentskill.PublicationID
	Candidate   agentskill.CandidateID
	PublishedBy Actor
	PublishedAt time.Time
}

// CurrentPublication is the stored audit shape of a selection: who moved
// the current pointer and when. Transitions persist it but do not return
// it; a "who selected this" read model would be a deliberate registry read.
type CurrentPublication struct {
	Publication agentskill.PublicationID
	SelectedBy  Actor
	SelectedAt  time.Time
}

type SkillSummary struct {
	Skill   agentskill.SkillID
	Current agentskill.PublicationID
}

func canonicalTime(src time.Time) time.Time { return time.Unix(0, src.UnixNano()).UTC() }
