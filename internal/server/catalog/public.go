// Package catalog owns the daemon's persistent publication catalog, candidate
// workflow, and immutable tree storage.
package catalog

import (
	"context"
	"io/fs"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

type (
	Catalog        = catalog
	Actor          = actor
	Curator        = curator
	Provenance     = provenance
	Candidate      = candidate
	Publication    = publication
	SkillSummary   = skillSummary
	CaptureRequest = captureRequest
	CandidateID    = candidateID
	Tree           = treeView
)

var (
	ErrNotFound       = errNotFound
	ErrConflict       = errConflict
	ErrTreesOpen      = errTreesOpen
	ErrCurationDenied = errCurationDenied
)

func Open(ctx context.Context, stateDir string) (*Catalog, error) {
	return openCatalog(ctx, stateDir)
}

func ParseCandidateID(src string) (CandidateID, error) {
	return parseCandidateID(src)
}

func NewActor(id, display string) (Actor, error) {
	actor := Actor{ID: id, Display: display}
	if err := validateActor(actor); err != nil {
		return Actor{}, err
	}
	return actor, nil
}

func (c *catalog) Close() error {
	return c.close()
}

func (c *catalog) Capture(ctx context.Context, curator Curator, request CaptureRequest) (Candidate, error) {
	return c.capture(ctx, curator, request)
}

func (c *catalog) Publish(ctx context.Context, id CandidateID, curator Curator, at time.Time) (Publication, error) {
	return c.publish(ctx, id, curator, at)
}

func (c *catalog) SetCurrent(ctx context.Context, id registry.PublicationID, curator Curator, at time.Time) error {
	return c.setCurrent(ctx, id, curator, at)
}

func (c *catalog) Candidate(ctx context.Context, id CandidateID) (Candidate, error) {
	return c.candidate(ctx, id)
}

func (c *catalog) Publication(ctx context.Context, id registry.PublicationID) (Publication, error) {
	return c.publication(ctx, id)
}

func (c *catalog) CurrentPublication(ctx context.Context, skill registry.SkillID) (Publication, error) {
	return c.currentPublication(ctx, skill)
}

func (c *catalog) ListPublishedSkills(ctx context.Context) ([]SkillSummary, error) {
	return c.listPublishedSkills(ctx)
}

func (c *catalog) OpenTree(ctx context.Context, digest registry.TreeDigest) (*Tree, error) {
	return c.openTree(ctx, digest)
}

var _ interface {
	fs.FS
	Close() error
} = (*Tree)(nil)
