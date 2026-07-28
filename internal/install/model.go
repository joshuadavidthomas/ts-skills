package install

import (
	"context"
	"fmt"
	"io"
	"io/fs"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

type Requirement struct {
	skill  registry.SkillID
	digest agentskill.TreeDigest
	exact  bool
}

func Current(skill registry.SkillID) (Requirement, error) {
	if skill.String() == "" {
		return Requirement{}, fmt.Errorf("current requirement needs a valid skill")
	}
	return Requirement{skill: skill}, nil
}

func Exact(skill registry.SkillID, digest agentskill.TreeDigest) (Requirement, error) {
	if skill.String() == "" {
		return Requirement{}, fmt.Errorf("exact requirement needs a valid skill")
	}
	return Requirement{skill: skill, digest: digest, exact: true}, nil
}

func (r Requirement) Skill() registry.SkillID { return r.skill }
func (r Requirement) ExactDigest() (agentskill.TreeDigest, bool) {
	return r.digest, r.exact
}

type LockedSkill struct {
	publication registry.PublicationID
}

func NewLockedSkill(publication registry.PublicationID) (LockedSkill, error) {
	if publication.Skill().String() == "" {
		return LockedSkill{}, fmt.Errorf("locked skill needs a valid publication")
	}
	return LockedSkill{publication: publication}, nil
}

func (l LockedSkill) Publication() registry.PublicationID { return l.publication }

type FetchedTree interface {
	fs.FS
	io.Closer
}

type FetchedSkill struct {
	publication registry.PublicationID
	tree        FetchedTree
}

func NewFetchedSkill(publication registry.PublicationID, tree FetchedTree) (FetchedSkill, error) {
	if publication.Skill().String() == "" || tree == nil {
		return FetchedSkill{}, fmt.Errorf("fetched skill needs a valid publication and tree")
	}
	return FetchedSkill{publication: publication, tree: tree}, nil
}

func (f FetchedSkill) Publication() registry.PublicationID { return f.publication }
func (f FetchedSkill) Tree() FetchedTree                   { return f.tree }

type Remote interface {
	Fetch(context.Context, Requirement) (FetchedSkill, error)
}
