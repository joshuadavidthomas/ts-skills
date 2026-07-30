// Package protocol defines the private HTTP contract shared by the ts-skills
// client and registry daemon.
package protocol

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/registry"
)

const (
	Version = "v1"

	JSONMediaType = "application/json"
	ZIPMediaType  = "application/zip"

	HeaderPublicationNamespace = "X-TS-Skills-Publication-Namespace"
	HeaderPublicationName      = "X-TS-Skills-Publication-Name"
	HeaderPublicationDigest    = "X-TS-Skills-Publication-Digest"

	CodeNotFound       = "not_found"
	CodeInvalidRequest = "invalid_request"
	CodeTooLarge       = "too_large"
	CodeInternal       = "internal"
	CodeUnavailable    = "unavailable"

	CurrentPattern = "GET /api/" + Version + "/skills/{namespace}/{name}/current"
	TreePattern    = "GET /api/" + Version + "/skills/{namespace}/{name}/publications/{digest}/tree.zip"
)

var ErrInvalid = errors.New("invalid registry protocol value")

type CurrentResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Digest    string `json:"digest"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

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

func NewCurrentResponse(publication registry.PublicationID) CurrentResponse {
	return CurrentResponse{
		Namespace: publication.Skill().Namespace().String(),
		Name:      publication.Skill().Name().String(),
		Digest:    publication.Tree().String(),
	}
}

func ParseCurrentResponse(response CurrentResponse) (registry.PublicationID, error) {
	skill, err := ParseSkill(response.Namespace, response.Name)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: current response identity: %v", ErrInvalid, err)
	}
	digest, err := parseDigest(response.Digest)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: current response digest: %v", ErrInvalid, err)
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: current response publication: %v", ErrInvalid, err)
	}
	return publication, nil
}

func ParseSkill(namespaceText, nameText string) (registry.SkillID, error) {
	namespace, err := registry.ParseNamespace(namespaceText)
	if err != nil || namespace.String() != namespaceText {
		return registry.SkillID{}, fmt.Errorf("%w: noncanonical namespace", ErrInvalid)
	}
	name, err := agentskill.ParseName(nameText)
	if err != nil || name.String() != nameText {
		return registry.SkillID{}, fmt.Errorf("%w: noncanonical Agent Skill name", ErrInvalid)
	}
	skill, err := registry.NewSkillID(namespace, name)
	if err != nil {
		return registry.SkillID{}, fmt.Errorf("%w: skill identity: %v", ErrInvalid, err)
	}
	return skill, nil
}

func ParsePublicationHeaders(header http.Header) (registry.PublicationID, error) {
	namespace, err := requiredHeader(header, HeaderPublicationNamespace)
	if err != nil {
		return registry.PublicationID{}, err
	}
	name, err := requiredHeader(header, HeaderPublicationName)
	if err != nil {
		return registry.PublicationID{}, err
	}
	digestText, err := requiredHeader(header, HeaderPublicationDigest)
	if err != nil {
		return registry.PublicationID{}, err
	}
	skill, err := ParseSkill(namespace, name)
	if err != nil {
		return registry.PublicationID{}, err
	}
	digest, err := parseDigest(digestText)
	if err != nil {
		return registry.PublicationID{}, err
	}
	publication, err := registry.NewPublicationID(skill, digest)
	if err != nil {
		return registry.PublicationID{}, fmt.Errorf("%w: publication headers: %v", ErrInvalid, err)
	}
	return publication, nil
}

func SetPublicationHeaders(header http.Header, publication registry.PublicationID) {
	header.Set(HeaderPublicationNamespace, publication.Skill().Namespace().String())
	header.Set(HeaderPublicationName, publication.Skill().Name().String())
	header.Set(HeaderPublicationDigest, publication.Tree().String())
}

func StatusForCode(code string) (int, bool) {
	switch code {
	case CodeNotFound:
		return http.StatusNotFound, true
	case CodeInvalidRequest:
		return http.StatusBadRequest, true
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge, true
	case CodeInternal:
		return http.StatusInternalServerError, true
	case CodeUnavailable:
		return http.StatusServiceUnavailable, true
	default:
		return 0, false
	}
}

func requiredHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" {
		return "", fmt.Errorf("%w: requires exactly one %s header", ErrInvalid, name)
	}
	return values[0], nil
}

func parseDigest(text string) (registry.TreeDigest, error) {
	digest, err := registry.ParseTreeDigest(text)
	if err != nil || digest.String() != text {
		return registry.TreeDigest{}, fmt.Errorf("%w: noncanonical publication digest", ErrInvalid)
	}
	return digest, nil
}
