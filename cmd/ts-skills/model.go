package main

import (
	"fmt"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

type requirement struct {
	skill  agentskill.SkillID
	digest agentskill.TreeDigest
	exact  bool
}

func current(skill agentskill.SkillID) (requirement, error) {
	if skill.String() == "" {
		return requirement{}, fmt.Errorf("current requirement needs a valid skill")
	}
	return requirement{skill: skill}, nil
}

func exact(skill agentskill.SkillID, digest agentskill.TreeDigest) (requirement, error) {
	if skill.String() == "" {
		return requirement{}, fmt.Errorf("exact requirement needs a valid skill")
	}
	return requirement{skill: skill, digest: digest, exact: true}, nil
}

func (r requirement) skillID() agentskill.SkillID { return r.skill }
func (r requirement) exactDigest() (agentskill.TreeDigest, bool) {
	return r.digest, r.exact
}

type lockedSkill struct {
	publication agentskill.PublicationID
}

type fetchedSkill struct {
	publication agentskill.PublicationID
	tree        *fetchedTree
}
