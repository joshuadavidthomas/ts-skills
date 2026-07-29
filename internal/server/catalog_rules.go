package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

type captureRequest struct {
	Namespace   agentskill.Namespace
	Staged      *safetree.Snapshot
	Root        string
	Source      string
	SubmittedAt time.Time
}

func (c *catalog) capture(ctx context.Context, curator curator, request captureRequest) (candidate, error) {
	if request.Namespace.String() == "" || request.Staged == nil || request.Source == "" || request.SubmittedAt.IsZero() {
		return candidate{}, fmt.Errorf("capture requires namespace, staged tree, source, and submission time")
	}
	provenance := provenance{Source: request.Source, SubmittedBy: curator.Actor, SubmittedAt: canonicalTime(request.SubmittedAt)}
	inspection, err := agentskill.Inspect(ctx, request.Staged.FS(), request.Root)
	if err != nil {
		return candidate{}, fmt.Errorf("load captured Agent Skill: %w", err)
	}
	skill, err := agentskill.NewSkillID(request.Namespace, inspection.Document().Name)
	if err != nil {
		return candidate{}, err
	}
	id, err := agentskill.NewCandidateID()
	if err != nil {
		return candidate{}, err
	}
	captured := candidate{ID: id, Skill: skill, Tree: inspection.Digest(), Provenance: provenance}
	if err := c.recordCandidate(ctx, captured, inspection.Directory()); err != nil {
		return candidate{}, fmt.Errorf("record candidate: %w", err)
	}
	return captured, nil
}

func (c *catalog) publish(ctx context.Context, id agentskill.CandidateID, curator curator, at time.Time) (publication, error) {
	candidate, err := c.candidate(ctx, id)
	if err != nil {
		return publication{}, err
	}
	publicationID, err := agentskill.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		return publication{}, err
	}
	published := publication{ID: publicationID, Candidate: id, PublishedBy: curator.Actor, PublishedAt: canonicalTime(at)}
	var initialCurrent *currentPublication
	if _, err := c.currentPublication(ctx, candidate.Skill); err != nil {
		if !errors.Is(err, errNotFound) {
			return publication{}, err
		}
		if c.afterMissingCurrentLookup != nil {
			c.afterMissingCurrentLookup()
		}
		selection := currentPublication{Publication: publicationID, SelectedBy: curator.Actor, SelectedAt: canonicalTime(at)}
		initialCurrent = &selection
	}
	inserted, err := c.persistPublication(ctx, published, initialCurrent)
	if err != nil {
		return publication{}, err
	}
	if inserted {
		return published, nil
	}
	return c.publication(ctx, publicationID)
}

func (c *catalog) setCurrent(ctx context.Context, id agentskill.PublicationID, curator curator, at time.Time) error {
	selection := currentPublication{Publication: id, SelectedBy: curator.Actor, SelectedAt: canonicalTime(at)}
	if _, err := c.publication(ctx, id); err != nil {
		return err
	}
	return c.persistCurrent(ctx, selection)
}

func (c *catalog) listSkills(ctx context.Context) ([]skillSummary, error) {
	if c.listSkillsErr != nil {
		return nil, c.listSkillsErr
	}
	return c.listPublishedSkills(ctx)
}

func (c *catalog) resolveCurrent(ctx context.Context, skill agentskill.SkillID) (publication, error) {
	return c.currentPublication(ctx, skill)
}
