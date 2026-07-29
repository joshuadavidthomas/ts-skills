package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

type Tree interface {
	fs.FS
	io.Closer
}

type CaptureRequest struct {
	Namespace   agentskill.Namespace
	Staged      *safetree.Snapshot
	Root        string
	Source      string
	SubmittedAt time.Time
}

// CatalogStore persists catalog facts and keeps each write atomic. Registry
// computes catalog transitions; stores only record the facts it supplies.
type CatalogStore interface {
	RecordCandidate(context.Context, Candidate, agentskill.Directory) error
	Candidate(context.Context, agentskill.CandidateID) (Candidate, error)
	OpenCandidateTree(context.Context, agentskill.CandidateID) (Tree, error)
	// PersistPublication inserts publication when absent and, in the same
	// transaction, records initialCurrent when the registry selected it. It
	// reports whether publication was inserted; it never replaces an existing
	// publication or current selection.
	PersistPublication(context.Context, Publication, *CurrentPublication) (bool, error)
	// PersistCurrent atomically replaces a skill's current selection.
	PersistCurrent(context.Context, CurrentPublication) error
	ListPublishedSkills(context.Context) ([]SkillSummary, error)
	CurrentPublication(context.Context, agentskill.SkillID) (Publication, error)
	Publication(context.Context, agentskill.PublicationID) (Publication, error)
	OpenPublicationTree(context.Context, agentskill.PublicationID) (Tree, error)
}

type Catalog struct {
	store         CatalogStore
	stagingParent string
	limits        safetree.Limits
}

func NewCatalog(store CatalogStore, stagingParent string, limits safetree.Limits) (*Catalog, error) {
	if store == nil {
		return nil, fmt.Errorf("catalog store must be provided")
	}
	if err := safetree.ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("catalog staging limits: %w", err)
	}
	info, err := os.Stat(stagingParent)
	if err != nil {
		return nil, fmt.Errorf("stat catalog staging parent: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("catalog staging parent must be a directory")
	}
	return &Catalog{store: store, stagingParent: stagingParent, limits: limits}, nil
}

func (c *Catalog) Capture(ctx context.Context, curator Curator, request CaptureRequest) (Candidate, error) {
	if request.Namespace.String() == "" || request.Staged == nil || request.Source == "" || request.SubmittedAt.IsZero() {
		return Candidate{}, fmt.Errorf("capture requires namespace, staged tree, source, and submission time")
	}
	provenance := Provenance{Source: request.Source, SubmittedBy: curator.Actor, SubmittedAt: canonicalTime(request.SubmittedAt)}
	inspection, err := agentskill.Inspect(ctx, request.Staged.FS(), request.Root)
	if err != nil {
		return Candidate{}, fmt.Errorf("load captured Agent Skill: %w", err)
	}
	skill, err := agentskill.NewSkillID(request.Namespace, inspection.Document().Name)
	if err != nil {
		return Candidate{}, err
	}
	id, err := agentskill.NewCandidateID()
	if err != nil {
		return Candidate{}, err
	}
	candidate := Candidate{ID: id, Skill: skill, Tree: inspection.Digest(), Provenance: provenance}
	if err := c.store.RecordCandidate(ctx, candidate, inspection.Directory()); err != nil {
		return Candidate{}, fmt.Errorf("record candidate: %w", err)
	}
	return candidate, nil
}

func (c *Catalog) Candidate(ctx context.Context, id agentskill.CandidateID) (Candidate, error) {
	return c.store.Candidate(ctx, id)
}
func (c *Catalog) OpenCandidateTree(ctx context.Context, id agentskill.CandidateID) (Tree, error) {
	return c.store.OpenCandidateTree(ctx, id)
}
func (c *Catalog) Publish(ctx context.Context, id agentskill.CandidateID, curator Curator, at time.Time) (Publication, error) {
	candidate, err := c.store.Candidate(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	publicationID, err := agentskill.NewPublicationID(candidate.Skill, candidate.Tree)
	if err != nil {
		return Publication{}, err
	}
	publication := Publication{ID: publicationID, Candidate: id, PublishedBy: curator.Actor, PublishedAt: canonicalTime(at)}
	var initialCurrent *CurrentPublication
	if _, err := c.store.CurrentPublication(ctx, candidate.Skill); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return Publication{}, err
		}
		selection := CurrentPublication{Publication: publicationID, SelectedBy: curator.Actor, SelectedAt: canonicalTime(at)}
		initialCurrent = &selection
	}
	inserted, err := c.store.PersistPublication(ctx, publication, initialCurrent)
	if err != nil {
		return Publication{}, err
	}
	if inserted {
		return publication, nil
	}
	return c.store.Publication(ctx, publicationID)
}
func (c *Catalog) SetCurrent(ctx context.Context, id agentskill.PublicationID, curator Curator, at time.Time) error {
	selection := CurrentPublication{Publication: id, SelectedBy: curator.Actor, SelectedAt: canonicalTime(at)}
	if _, err := c.store.Publication(ctx, id); err != nil {
		return err
	}
	return c.store.PersistCurrent(ctx, selection)
}
func (c *Catalog) ListSkills(ctx context.Context) ([]SkillSummary, error) {
	return c.store.ListPublishedSkills(ctx)
}
func (c *Catalog) ResolveCurrent(ctx context.Context, skill agentskill.SkillID) (Publication, error) {
	return c.store.CurrentPublication(ctx, skill)
}
func (c *Catalog) Publication(ctx context.Context, id agentskill.PublicationID) (Publication, error) {
	return c.store.Publication(ctx, id)
}
func (c *Catalog) OpenPublicationTree(ctx context.Context, id agentskill.PublicationID) (Tree, error) {
	return c.store.OpenPublicationTree(ctx, id)
}
