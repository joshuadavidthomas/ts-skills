package server

import (
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

type actor struct {
	ID      string
	Display string
}

// curator carries an actor authorized to mutate the catalog.
type curator struct{ Actor actor }

type provenance struct {
	Source      string
	SubmittedBy actor
	SubmittedAt time.Time
}

type candidate struct {
	ID         candidateID
	Skill      registry.SkillID
	Tree       registry.TreeDigest
	Provenance provenance
}

type publication struct {
	ID          registry.PublicationID
	Candidate   candidateID
	PublishedBy actor
	PublishedAt time.Time
}

// currentPublication is the stored audit shape of a selection: who moved
// the current pointer and when. Transitions persist it but do not return
// it; a "who selected this" read model would be a deliberate registry read.
type currentPublication struct {
	Publication registry.PublicationID
	SelectedBy  actor
	SelectedAt  time.Time
}

type skillSummary struct {
	Skill   registry.SkillID
	Current registry.PublicationID
}

func canonicalTime(src time.Time) time.Time { return time.Unix(0, src.UnixNano()).UTC() }
