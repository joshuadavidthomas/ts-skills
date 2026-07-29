package install

import (
	"context"
	"fmt"
	"io"
	"io/fs"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

type Requirement struct {
	skill  agentskill.SkillID
	digest agentskill.TreeDigest
	exact  bool
}

func Current(skill agentskill.SkillID) (Requirement, error) {
	if skill.String() == "" {
		return Requirement{}, fmt.Errorf("current requirement needs a valid skill")
	}
	return Requirement{skill: skill}, nil
}

func Exact(skill agentskill.SkillID, digest agentskill.TreeDigest) (Requirement, error) {
	if skill.String() == "" {
		return Requirement{}, fmt.Errorf("exact requirement needs a valid skill")
	}
	return Requirement{skill: skill, digest: digest, exact: true}, nil
}

func (r Requirement) Skill() agentskill.SkillID { return r.skill }
func (r Requirement) ExactDigest() (agentskill.TreeDigest, bool) {
	return r.digest, r.exact
}

type LockedSkill struct {
	Publication agentskill.PublicationID
}

type FetchedTree interface {
	fs.FS
	io.Closer
}

type FetchedSkill struct {
	Publication agentskill.PublicationID
	Tree        FetchedTree
}

type Remote interface {
	Fetch(context.Context, Requirement) (FetchedSkill, error)
}
