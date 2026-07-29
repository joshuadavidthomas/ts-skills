package server

import "github.com/joshuadavidthomas/ts-skills/internal/agentskill"

func testCurator(owner actor) curator {
	return curator{Actor: owner}
}

func testCandidate(id agentskill.CandidateID, skill agentskill.SkillID, tree agentskill.TreeDigest, provenance provenance) candidate {
	return candidate{ID: id, Skill: skill, Tree: tree, Provenance: provenance}
}

func testCandidates(first, second candidate) []candidate {
	return []candidate{first, second}
}
