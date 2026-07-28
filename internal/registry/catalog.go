package registry

import (
	"context"
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
	Namespace  Namespace
	Source     fs.FS
	Root       string
	Provenance Provenance
}

type CatalogRecords interface {
	RecordCandidate(context.Context, Candidate, agentskill.Directory) error
	Candidate(context.Context, CandidateID) (Candidate, error)
	OpenCandidateTree(context.Context, CandidateID) (Tree, error)
	PublishCandidate(context.Context, CandidateID, Actor, time.Time) (PublishResult, error)
	SelectCurrent(context.Context, PublicationID, Actor, time.Time) (CurrentPublication, error)
	ListPublishedSkills(context.Context) ([]SkillSummary, error)
	ResolveCurrent(context.Context, SkillID) (Publication, error)
	Publication(context.Context, PublicationID) (Publication, error)
	OpenPublicationTree(context.Context, PublicationID) (Tree, error)
}

type Catalog struct {
	records       CatalogRecords
	stagingParent string
	limits        safetree.Limits
}

func NewCatalog(records CatalogRecords, stagingParent string, limits safetree.Limits) (*Catalog, error) {
	if records == nil {
		return nil, fmt.Errorf("catalog records must be provided")
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
	return &Catalog{records: records, stagingParent: stagingParent, limits: limits}, nil
}

func (c *Catalog) Capture(ctx context.Context, request CaptureRequest) (Candidate, error) {
	if request.Namespace.canonical == "" || request.Source == nil || request.Provenance.source.label == "" {
		return Candidate{}, fmt.Errorf("capture requires namespace, source, and provenance")
	}
	snapshot, err := safetree.StageFS(ctx, c.stagingParent, request.Source, request.Root, c.limits)
	if err != nil {
		return Candidate{}, fmt.Errorf("capture candidate tree: %w", err)
	}
	defer func() { _ = snapshot.Close() }()
	inspection, err := agentskill.Inspect(ctx, snapshot.FS(), request.Root)
	if err != nil {
		return Candidate{}, fmt.Errorf("load captured Agent Skill: %w", err)
	}
	skill, err := NewSkillID(request.Namespace, inspection.Document().Name)
	if err != nil {
		return Candidate{}, err
	}
	id, err := NewCandidateID()
	if err != nil {
		return Candidate{}, err
	}
	candidate, err := NewCandidate(id, skill, inspection.Digest(), request.Provenance)
	if err != nil {
		return Candidate{}, err
	}
	if err := c.records.RecordCandidate(ctx, candidate, inspection.Directory()); err != nil {
		return Candidate{}, fmt.Errorf("record candidate: %w", err)
	}
	return candidate, nil
}

func (c *Catalog) Candidate(ctx context.Context, id CandidateID) (Candidate, error) {
	return c.records.Candidate(ctx, id)
}
func (c *Catalog) OpenCandidateTree(ctx context.Context, id CandidateID) (Tree, error) {
	return c.records.OpenCandidateTree(ctx, id)
}
func (c *Catalog) Publish(ctx context.Context, id CandidateID, actor Actor, at time.Time) (PublishResult, error) {
	return c.records.PublishCandidate(ctx, id, actor, at)
}
func (c *Catalog) SetCurrent(ctx context.Context, id PublicationID, actor Actor, at time.Time) (CurrentPublication, error) {
	return c.records.SelectCurrent(ctx, id, actor, at)
}
func (c *Catalog) ListSkills(ctx context.Context) ([]SkillSummary, error) {
	return c.records.ListPublishedSkills(ctx)
}
func (c *Catalog) ResolveCurrent(ctx context.Context, skill SkillID) (Publication, error) {
	return c.records.ResolveCurrent(ctx, skill)
}
func (c *Catalog) Publication(ctx context.Context, id PublicationID) (Publication, error) {
	return c.records.Publication(ctx, id)
}
func (c *Catalog) OpenPublicationTree(ctx context.Context, id PublicationID) (Tree, error) {
	return c.records.OpenPublicationTree(ctx, id)
}
