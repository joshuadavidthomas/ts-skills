package protocol

import (
	"net/url"

	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

const (
	Version = "v1"

	CurrentPattern = "GET /api/" + Version + "/skills/{namespace}/{name}/current"
	TreePattern    = "GET /api/" + Version + "/skills/{namespace}/{name}/publications/{digest}/tree.zip"
)

func CurrentPath(skill registry.SkillID) string {
	return "/api/" + Version + "/skills/" + url.PathEscape(skill.Namespace().String()) +
		"/" + url.PathEscape(skill.Name().String()) + "/current"
}

func TreePath(publication registry.PublicationID) string {
	skill := publication.Skill()
	return "/api/" + Version + "/skills/" + url.PathEscape(skill.Namespace().String()) +
		"/" + url.PathEscape(skill.Name().String()) + "/publications/" +
		publication.Tree().String() + "/tree.zip"
}
