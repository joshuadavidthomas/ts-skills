package registry

import (
	"fmt"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

type Candidate struct {
	id         CandidateID
	skill      SkillID
	tree       agentskill.TreeDigest
	provenance Provenance
}

type Publication struct {
	id          PublicationID
	candidate   CandidateID
	publishedBy Actor
	publishedAt time.Time
}

type CurrentPublication struct {
	publication PublicationID
	selectedBy  Actor
	selectedAt  time.Time
}

type PublishResult struct {
	publication   Publication
	created       bool
	becameCurrent bool
}

type SkillSummary struct {
	skill   SkillID
	current PublicationID
}

func NewCandidate(id CandidateID, skill SkillID, tree agentskill.TreeDigest, provenance Provenance) (Candidate, error) {
	if id.IsZero() || skill.String() == "" || provenance.source.label == "" {
		return Candidate{}, fmt.Errorf("candidate requires valid identity, skill, and provenance")
	}
	return Candidate{id: id, skill: skill, tree: tree, provenance: provenance}, nil
}

func (c Candidate) ID() CandidateID             { return c.id }
func (c Candidate) Skill() SkillID              { return c.skill }
func (c Candidate) Tree() agentskill.TreeDigest { return c.tree }
func (c Candidate) Provenance() Provenance      { return c.provenance }

func NewPublication(id PublicationID, candidate CandidateID, actor Actor, publishedAt time.Time) (Publication, error) {
	if id.skill.String() == "" || candidate.IsZero() || actor.id == "" || publishedAt.IsZero() {
		return Publication{}, fmt.Errorf("publication requires valid identity, candidate, actor, and publication time")
	}
	return Publication{id: id, candidate: candidate, publishedBy: actor, publishedAt: canonicalTime(publishedAt)}, nil
}

func (p Publication) ID() PublicationID      { return p.id }
func (p Publication) Candidate() CandidateID { return p.candidate }
func (p Publication) PublishedBy() Actor     { return p.publishedBy }
func (p Publication) PublishedAt() time.Time { return p.publishedAt }

func NewCurrentPublication(publication PublicationID, actor Actor, selectedAt time.Time) (CurrentPublication, error) {
	if publication.skill.String() == "" || actor.id == "" || selectedAt.IsZero() {
		return CurrentPublication{}, fmt.Errorf("current publication requires publication, actor, and selection time")
	}
	return CurrentPublication{publication: publication, selectedBy: actor, selectedAt: canonicalTime(selectedAt)}, nil
}

func (c CurrentPublication) Publication() PublicationID { return c.publication }
func (c CurrentPublication) SelectedBy() Actor          { return c.selectedBy }
func (c CurrentPublication) SelectedAt() time.Time      { return c.selectedAt }

func NewPublishResult(publication Publication, created, becameCurrent bool) (PublishResult, error) {
	if publication.id.skill.String() == "" {
		return PublishResult{}, fmt.Errorf("publish result requires a publication")
	}
	if becameCurrent && !created {
		return PublishResult{}, fmt.Errorf("an existing publication cannot become first current during publish")
	}
	return PublishResult{publication: publication, created: created, becameCurrent: becameCurrent}, nil
}

func (r PublishResult) Publication() Publication { return r.publication }
func (r PublishResult) Created() bool            { return r.created }
func (r PublishResult) BecameCurrent() bool      { return r.becameCurrent }

func NewSkillSummary(skill SkillID, current PublicationID) (SkillSummary, error) {
	if skill.String() == "" || current.skill.String() == "" || skill != current.skill {
		return SkillSummary{}, fmt.Errorf("skill summary current publication must name the same valid skill")
	}
	return SkillSummary{skill: skill, current: current}, nil
}

func (s SkillSummary) Skill() SkillID         { return s.skill }
func (s SkillSummary) Current() PublicationID { return s.current }
