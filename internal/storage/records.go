package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

func queryCandidate(ctx context.Context, queryRow func(context.Context, string, ...any) *sql.Row, id registry.CandidateID) (registry.Candidate, error) {
	row := queryRow(ctx, `
		SELECT id, namespace, name, tree_digest, source_label,
		       submitted_actor_id, submitted_actor_display, submitted_at_ns
		FROM candidates WHERE id = ?`, candidateIDBlob(id))
	candidate, err := scanCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return registry.Candidate{}, fmt.Errorf("candidate %s: %w", id, registry.ErrNotFound)
	}
	if err != nil {
		return registry.Candidate{}, fmt.Errorf("read candidate %s: %w", id, err)
	}
	return candidate, nil
}

func scanCandidate(row *sql.Row) (registry.Candidate, error) {
	var (
		idBlob, digestBlob                   []byte
		namespaceText, nameText, sourceLabel string
		actorID, actorDisplay                string
		submittedAtNanoseconds               int64
	)
	if err := row.Scan(
		&idBlob, &namespaceText, &nameText, &digestBlob, &sourceLabel,
		&actorID, &actorDisplay, &submittedAtNanoseconds,
	); err != nil {
		return registry.Candidate{}, err
	}
	id, err := candidateIDFromBlob(idBlob)
	if err != nil {
		return registry.Candidate{}, err
	}
	skill, err := skillFromText(namespaceText, nameText)
	if err != nil {
		return registry.Candidate{}, err
	}
	digest, err := digestFromBlob(digestBlob)
	if err != nil {
		return registry.Candidate{}, err
	}
	source, err := registry.NewUploadSource(sourceLabel)
	if err != nil {
		return registry.Candidate{}, fmt.Errorf("decode candidate source: %w", err)
	}
	actor, err := registry.NewActor(actorID, actorDisplay)
	if err != nil {
		return registry.Candidate{}, fmt.Errorf("decode candidate actor: %w", err)
	}
	provenance, err := registry.NewProvenance(source, actor, time.Unix(0, submittedAtNanoseconds).UTC())
	if err != nil {
		return registry.Candidate{}, fmt.Errorf("decode candidate provenance: %w", err)
	}
	candidate, err := registry.NewCandidate(id, skill, digest, provenance)
	if err != nil {
		return registry.Candidate{}, fmt.Errorf("decode candidate: %w", err)
	}
	return candidate, nil
}

func queryPublication(ctx context.Context, queryRow func(context.Context, string, ...any) *sql.Row, id registry.PublicationID) (registry.Publication, error) {
	row := queryRow(ctx, `
		SELECT namespace, name, tree_digest, candidate_id,
		       published_actor_id, published_actor_display, published_at_ns
		FROM publications
		WHERE namespace = ? AND name = ? AND tree_digest = ?`,
		id.Skill().Namespace().String(), id.Skill().Name().String(), digestBlob(id.Tree()))
	publication, err := scanPublication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return registry.Publication{}, fmt.Errorf("publication %s at %s: %w", id.Skill(), id.Tree(), registry.ErrNotFound)
	}
	if err != nil {
		return registry.Publication{}, fmt.Errorf("read publication %s at %s: %w", id.Skill(), id.Tree(), err)
	}
	return publication, nil
}

func scanPublication(row *sql.Row) (registry.Publication, error) {
	var (
		digestBytes, candidateBytes               []byte
		namespaceText, nameText, actorID, display string
		publishedAtNanoseconds                    int64
	)
	if err := row.Scan(
		&namespaceText, &nameText, &digestBytes, &candidateBytes,
		&actorID, &display, &publishedAtNanoseconds,
	); err != nil {
		return registry.Publication{}, err
	}
	skill, err := skillFromText(namespaceText, nameText)
	if err != nil {
		return registry.Publication{}, err
	}
	digest, err := digestFromBlob(digestBytes)
	if err != nil {
		return registry.Publication{}, err
	}
	id, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		return registry.Publication{}, fmt.Errorf("decode publication identity: %w", err)
	}
	candidate, err := candidateIDFromBlob(candidateBytes)
	if err != nil {
		return registry.Publication{}, err
	}
	actor, err := registry.NewActor(actorID, display)
	if err != nil {
		return registry.Publication{}, fmt.Errorf("decode publication actor: %w", err)
	}
	publication, err := registry.NewPublication(id, candidate, actor, time.Unix(0, publishedAtNanoseconds).UTC())
	if err != nil {
		return registry.Publication{}, fmt.Errorf("decode publication: %w", err)
	}
	return publication, nil
}

func (c *Catalog) Publication(ctx context.Context, id registry.PublicationID) (registry.Publication, error) {
	done, err := c.withOpenState()
	if err != nil {
		return registry.Publication{}, err
	}
	defer done()
	return queryPublication(ctx, c.db.QueryRowContext, id)
}

func (c *Catalog) ResolveCurrent(ctx context.Context, skill registry.SkillID) (registry.Publication, error) {
	done, err := c.withOpenState()
	if err != nil {
		return registry.Publication{}, err
	}
	defer done()
	row := c.db.QueryRowContext(ctx, `
		SELECT p.namespace, p.name, p.tree_digest, p.candidate_id,
		       p.published_actor_id, p.published_actor_display, p.published_at_ns
		FROM current_publications AS current
		JOIN publications AS p
		  ON p.namespace = current.namespace
		 AND p.name = current.name
		 AND p.tree_digest = current.tree_digest
		WHERE current.namespace = ? AND current.name = ?`,
		skill.Namespace().String(), skill.Name().String())
	publication, err := scanPublication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return registry.Publication{}, fmt.Errorf("current publication for %s: %w", skill, registry.ErrNotFound)
	}
	if err != nil {
		return registry.Publication{}, fmt.Errorf("resolve current publication for %s: %w", skill, err)
	}
	return publication, nil
}

func (c *Catalog) ListPublishedSkills(ctx context.Context) (summaries []registry.SkillSummary, err error) {
	done, err := c.withOpenState()
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := c.db.QueryContext(ctx, `
		SELECT namespace, name, tree_digest
		FROM current_publications
		ORDER BY namespace, name`)
	if err != nil {
		return nil, fmt.Errorf("list published skills: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var namespaceText, nameText string
		var digestBytes []byte
		if err := rows.Scan(&namespaceText, &nameText, &digestBytes); err != nil {
			return nil, fmt.Errorf("scan published skill: %w", err)
		}
		skill, err := skillFromText(namespaceText, nameText)
		if err != nil {
			return nil, err
		}
		digest, err := digestFromBlob(digestBytes)
		if err != nil {
			return nil, err
		}
		publication, err := registry.NewPublicationID(skill, digest)
		if err != nil {
			return nil, fmt.Errorf("decode current publication: %w", err)
		}
		summary, err := registry.NewSkillSummary(skill, publication)
		if err != nil {
			return nil, fmt.Errorf("decode published skill: %w", err)
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list published skills: %w", err)
	}
	// SQLite's text collation and Go's bytewise order agree for valid UTF-8,
	// but sorting here makes the interface's canonical order independent of
	// database collation settings.
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Skill().String() < summaries[j].Skill().String()
	})
	return summaries, nil
}

func skillFromText(namespaceText, nameText string) (registry.SkillID, error) {
	namespace, err := registry.ParseNamespace(namespaceText)
	if err != nil {
		return registry.SkillID{}, fmt.Errorf("decode namespace: %w", err)
	}
	name, err := agentskill.ParseName(nameText)
	if err != nil {
		return registry.SkillID{}, fmt.Errorf("decode Agent Skill name: %w", err)
	}
	skill, err := registry.NewSkillID(namespace, name)
	if err != nil {
		return registry.SkillID{}, fmt.Errorf("decode skill identity: %w", err)
	}
	return skill, nil
}

func candidateIDBlob(id registry.CandidateID) []byte {
	blob := make([]byte, len(id))
	copy(blob, id[:])
	return blob
}

func candidateIDFromBlob(blob []byte) (registry.CandidateID, error) {
	if len(blob) != 16 {
		return registry.CandidateID{}, fmt.Errorf("stored candidate identity has %d bytes, want 16", len(blob))
	}
	id, err := registry.ParseCandidateID(hex.EncodeToString(blob))
	if err != nil {
		return registry.CandidateID{}, fmt.Errorf("decode candidate identity: %w", err)
	}
	return id, nil
}

func digestBlob(digest agentskill.TreeDigest) []byte {
	blob := make([]byte, len(digest))
	copy(blob, digest[:])
	return blob
}

func digestFromBlob(blob []byte) (agentskill.TreeDigest, error) {
	if len(blob) != 32 {
		return agentskill.TreeDigest{}, fmt.Errorf("stored tree digest has %d bytes, want 32", len(blob))
	}
	digest, err := agentskill.ParseTreeDigest("sha256:" + hex.EncodeToString(blob))
	if err != nil {
		return agentskill.TreeDigest{}, fmt.Errorf("decode tree digest: %w", err)
	}
	return digest, nil
}
