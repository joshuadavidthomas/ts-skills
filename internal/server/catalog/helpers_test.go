package catalog

import "github.com/joshuadavidthomas/ts-skills/internal/registry"

func testCurator(owner actor) curator {
	return curator{Actor: owner}
}

func testCandidate(id candidateID, skill registry.SkillID, tree registry.TreeDigest, provenance provenance) candidate {
	return candidate{ID: id, Skill: skill, Tree: tree, Provenance: provenance}
}

func testCandidates(first, second candidate) []candidate {
	return []candidate{first, second}
}
