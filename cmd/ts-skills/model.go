package main

import (
	"fmt"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

type requirement struct {
	skill  registry.SkillID
	digest registry.TreeDigest
	exact  bool
}

func current(skill registry.SkillID) (requirement, error) {
	if skill.String() == "" {
		return requirement{}, fmt.Errorf("current requirement needs a valid skill")
	}
	return requirement{skill: skill}, nil
}

func exact(skill registry.SkillID, digest registry.TreeDigest) (requirement, error) {
	if skill.String() == "" {
		return requirement{}, fmt.Errorf("exact requirement needs a valid skill")
	}
	return requirement{skill: skill, digest: digest, exact: true}, nil
}

func (r requirement) skillID() registry.SkillID { return r.skill }
func (r requirement) exactDigest() (registry.TreeDigest, bool) {
	return r.digest, r.exact
}

type lockedSkill struct {
	publication registry.PublicationID
}

type fetchedSkill struct {
	publication registry.PublicationID
	tree        *tree.Snapshot
}
