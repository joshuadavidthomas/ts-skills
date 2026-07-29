package agentskill

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type Namespace struct{ canonical string }
type CandidateID struct{ id [16]byte }

type SkillID struct {
	namespace Namespace
	name      Name
}

type PublicationID struct {
	skill SkillID
	tree  TreeDigest
}

func ParseNamespace(src string) (Namespace, error) {
	if !utf8.ValidString(src) {
		return Namespace{}, fmt.Errorf("namespace must be valid UTF-8")
	}
	canonical := norm.NFKC.String(src)
	if n := utf8.RuneCountInString(canonical); n < 1 || n > 64 {
		return Namespace{}, fmt.Errorf("namespace must contain 1 to 64 Unicode scalar values")
	}
	if canonical == "." || canonical == ".." || strings.ContainsAny(canonical, "/\\") {
		return Namespace{}, fmt.Errorf("namespace %q is reserved or contains a path separator", canonical)
	}
	if strings.TrimSpace(canonical) != canonical {
		return Namespace{}, fmt.Errorf("namespace must not have leading or trailing whitespace")
	}
	for _, r := range canonical {
		if unicode.IsControl(r) {
			return Namespace{}, fmt.Errorf("namespace must not contain control characters")
		}
	}
	return Namespace{canonical: canonical}, nil
}

func (n Namespace) String() string { return n.canonical }

func NewSkillID(namespace Namespace, name Name) (SkillID, error) {
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
	name, err := ParseName(nameText)
	if err != nil {
		return SkillID{}, fmt.Errorf("parse Agent Skill name: %w", err)
	}
	return NewSkillID(namespace, name)
}

func (s SkillID) Namespace() Namespace { return s.namespace }
func (s SkillID) Name() Name           { return s.name }
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

func NewCandidateID() (CandidateID, error) {
	var id CandidateID
	if _, err := rand.Read(id.id[:]); err != nil {
		return CandidateID{}, fmt.Errorf("generate candidate identity: %w", err)
	}
	return id, nil
}

// CandidateIDFromBytes constructs a candidate identity from its persisted
// 16-byte form. It is the persistence seam for storage; other construction
// should go through NewCandidateID or ParseCandidateID.
func CandidateIDFromBytes(raw [16]byte) (CandidateID, error) {
	id := CandidateID{id: raw}
	if id.IsZero() {
		return CandidateID{}, fmt.Errorf("candidate identity must not be zero")
	}
	return id, nil
}

func ParseCandidateID(src string) (CandidateID, error) {
	var id CandidateID
	if len(src) != 32 {
		return id, fmt.Errorf("candidate identity must contain 32 lowercase hexadecimal characters")
	}
	for _, ch := range src {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return id, fmt.Errorf("candidate identity must use lowercase hexadecimal characters")
		}
	}
	if _, err := hex.Decode(id.id[:], []byte(src)); err != nil {
		return CandidateID{}, fmt.Errorf("parse candidate identity: %w", err)
	}
	if id.IsZero() {
		return CandidateID{}, fmt.Errorf("candidate identity must not be zero")
	}
	return id, nil
}

func (id CandidateID) String() string { return hex.EncodeToString(id.id[:]) }

// Bytes returns the identity's persisted 16-byte form.
func (id CandidateID) Bytes() [16]byte { return id.id }

func (id CandidateID) IsZero() bool { return id.id == ([16]byte{}) }
