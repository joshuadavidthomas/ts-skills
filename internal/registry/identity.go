package registry

import (
	"fmt"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
)

type Namespace struct{ canonical string }

type SkillID struct {
	namespace Namespace
	name      agentskill.Name
}

type PublicationID struct {
	skill SkillID
	tree  TreeDigest
}

func ParseNamespace(src string) (Namespace, error) {
	if len(src) < 1 || len(src) > 64 {
		return Namespace{}, fmt.Errorf("namespace must contain 1 to 64 ASCII characters")
	}
	for i, ch := range []byte(src) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			continue
		}
		if ch == '-' && i > 0 && i < len(src)-1 {
			continue
		}
		return Namespace{}, fmt.Errorf("namespace must use lowercase ASCII letters, digits, and internal hyphens")
	}
	return Namespace{canonical: src}, nil
}

func (n Namespace) String() string { return n.canonical }

func NewSkillID(namespace Namespace, name agentskill.Name) (SkillID, error) {
	if namespace.canonical == "" || name.String() == "" {
		return SkillID{}, fmt.Errorf("skill identity requires a namespace and Agent Skill name")
	}
	return SkillID{namespace: namespace, name: name}, nil
}

func ParseSkillID(src string) (SkillID, error) {
	namespaceText, nameText, found := strings.Cut(src, "/")
	if !found || strings.Contains(nameText, "/") {
		return SkillID{}, fmt.Errorf("skill identity must be namespace/name")
	}
	namespace, err := ParseNamespace(namespaceText)
	if err != nil {
		return SkillID{}, fmt.Errorf("parse skill namespace: %w", err)
	}
	name, err := agentskill.ParseName(nameText)
	if err != nil {
		return SkillID{}, fmt.Errorf("parse Agent Skill name: %w", err)
	}
	return NewSkillID(namespace, name)
}

func (s SkillID) Namespace() Namespace  { return s.namespace }
func (s SkillID) Name() agentskill.Name { return s.name }
func (s SkillID) String() string {
	if s.namespace.canonical == "" || s.name.String() == "" {
		return ""
	}
	return s.namespace.String() + "/" + s.name.String()
}

func NewPublicationID(skill SkillID, tree TreeDigest) (PublicationID, error) {
	if skill.String() == "" {
		return PublicationID{}, fmt.Errorf("publication identity requires a valid skill")
	}
	return PublicationID{skill: skill, tree: tree}, nil
}

func (p PublicationID) Skill() SkillID   { return p.skill }
func (p PublicationID) Tree() TreeDigest { return p.tree }
