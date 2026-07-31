package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

type captureRequest struct {
	Namespace   registry.Namespace
	Staged      *tree.Snapshot
	Root        string
	Source      string
	SubmittedAt time.Time
}

func (c *catalog) capture(ctx context.Context, curator curator, request captureRequest) (candidate, error) {
	if err := validateActor(curator.Actor); err != nil {
		return candidate{}, fmt.Errorf("validate curator: %w", err)
	}
	if request.Namespace.String() == "" || request.Staged == nil || request.SubmittedAt.IsZero() {
		return candidate{}, fmt.Errorf("capture requires namespace, staged tree, and submission time")
	}
	if err := validateRecordText("candidate source", request.Source); err != nil {
		return candidate{}, fmt.Errorf("validate capture: %w", err)
	}
	provenance := provenance{Source: request.Source, SubmittedBy: curator.Actor, SubmittedAt: canonicalTime(request.SubmittedAt)}
	inspection, err := registry.Inspect(ctx, request.Staged, request.Root)
	if err != nil {
		return candidate{}, fmt.Errorf("load captured Agent Skill: %w", err)
	}
	skill, err := registry.NewSkillID(request.Namespace, inspection.Document().Name)
	if err != nil {
		return candidate{}, err
	}
	id, err := newCandidateID()
	if err != nil {
		return candidate{}, err
	}
	captured := candidate{ID: id, Skill: skill, Tree: inspection.Digest(), Provenance: provenance}
	if err := c.recordCandidate(ctx, captured, inspection.Directory()); err != nil {
		return candidate{}, fmt.Errorf("record candidate: %w", err)
	}
	return captured, nil
}

func (c *catalog) publish(ctx context.Context, id candidateID, curator curator, at time.Time) (publication, error) {
	if err := validateActor(curator.Actor); err != nil {
		return publication{}, fmt.Errorf("validate curator: %w", err)
	}
	candidate, err := c.candidate(ctx, id)
	if err != nil {
		return publication{}, err
	}
	publicationID, err := registry.NewPublicationID(candidate.Skill, candidate.Tree)
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

func (c *catalog) setCurrent(ctx context.Context, id registry.PublicationID, curator curator, at time.Time) error {
	if err := validateActor(curator.Actor); err != nil {
		return fmt.Errorf("validate curator: %w", err)
	}
	selection := currentPublication{Publication: id, SelectedBy: curator.Actor, SelectedAt: canonicalTime(at)}
	if _, err := c.publication(ctx, id); err != nil {
		return err
	}
	return c.persistCurrent(ctx, selection)
}
