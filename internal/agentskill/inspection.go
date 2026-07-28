package agentskill

import (
	"context"
	"fmt"
	"io/fs"
)

// Inspection is a loaded Agent Skill tree with its identity facts bound: the
// parsed SKILL.md and the digest of the whole tree. Inspect verifies both in
// one pass, so a non-zero Inspection proves the tree parses and hashes
// cleanly. Like Directory it is read-only; Document clones on access.
type Inspection struct {
	directory Directory
	digest    TreeDigest
}

// Inspect loads the Agent Skill at dir and hashes its full tree, returning
// document and digest as one bound value. Trust boundaries that need both
// facts should use Inspect rather than composing Load and SumTree by hand.
func Inspect(ctx context.Context, fsys fs.FS, dir string) (Inspection, error) {
	directory, err := Load(fsys, dir)
	if err != nil {
		return Inspection{}, err
	}
	digest, err := SumTree(ctx, fsys, dir)
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{directory: directory, digest: digest}, nil
}

// Directory returns the loaded directory: the document and the tree
// filesystem rooted at the inspected directory.
func (i Inspection) Directory() Directory { return i.directory }

// Document returns a clone of the parsed SKILL.md.
func (i Inspection) Document() Document { return i.directory.Document() }

// Digest returns the tree digest computed during Inspect.
func (i Inspection) Digest() TreeDigest { return i.digest }

// FS returns the tree rooted at the inspected directory.
func (i Inspection) FS() fs.FS { return i.directory.FS() }

// RequireName fails when the tree's SKILL.md names a skill other than
// expected.
func (i Inspection) RequireName(expected Name) error {
	actual := i.directory.document.Name
	if actual != expected {
		return newValidationError(ErrInvalidTree, Filename, fmt.Sprintf("names %q, want %q", actual.String(), expected.String()))
	}
	return nil
}
