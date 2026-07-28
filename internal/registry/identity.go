package registry

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrNotFound     = errors.New("registry value not found")
	ErrConflict     = errors.New("registry conflict")
	ErrTreeMismatch = errors.New("stored tree does not match its digest")
)

type Namespace struct{ canonical string }
type CandidateID [16]byte

type Actor struct {
	id      string
	display string
}

type SkillID struct {
	namespace Namespace
	name      agentskill.Name
}

type PublicationID struct {
	skill SkillID
	tree  agentskill.TreeDigest
}

type UploadSource struct {
	label string
}

type Provenance struct {
	source      UploadSource
	submittedBy Actor
	submittedAt time.Time
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

func NewPublicationID(skill SkillID, tree agentskill.TreeDigest) (PublicationID, error) {
	if skill.String() == "" {
		return PublicationID{}, fmt.Errorf("publication identity requires a valid skill")
	}
	return PublicationID{skill: skill, tree: tree}, nil
}

func (p PublicationID) Skill() SkillID              { return p.skill }
func (p PublicationID) Tree() agentskill.TreeDigest { return p.tree }

func NewCandidateID() (CandidateID, error) {
	var id CandidateID
	if _, err := rand.Read(id[:]); err != nil {
		return CandidateID{}, fmt.Errorf("generate candidate identity: %w", err)
	}
	return id, nil
}

func ParseCandidateID(src string) (CandidateID, error) {
	var id CandidateID
	if len(src) != 32 {
		return id, fmt.Errorf("candidate identity must contain 32 lowercase hexadecimal characters")
	}
	for _, ch := range src {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return id, fmt.Errorf("candidate identity must use lowercase hexadecimal characters")
		}
	}
	if _, err := hex.Decode(id[:], []byte(src)); err != nil {
		return CandidateID{}, fmt.Errorf("parse candidate identity: %w", err)
	}
	if id == (CandidateID{}) {
		return CandidateID{}, fmt.Errorf("candidate identity must not be zero")
	}
	return id, nil
}

func (id CandidateID) String() string { return hex.EncodeToString(id[:]) }

func NewActor(id, display string) (Actor, error) {
	if err := validateBoundedText("actor id", id); err != nil {
		return Actor{}, err
	}
	if err := validateBoundedText("actor display", display); err != nil {
		return Actor{}, err
	}
	return Actor{id: id, display: display}, nil
}

func (a Actor) ID() string      { return a.id }
func (a Actor) Display() string { return a.display }

func NewUploadSource(label string) (UploadSource, error) {
	if err := validateBoundedText("upload source label", label); err != nil {
		return UploadSource{}, err
	}
	if label == "" {
		return UploadSource{}, fmt.Errorf("upload source label must not be empty")
	}
	return UploadSource{label: label}, nil
}

func (s UploadSource) Label() string { return s.label }

func NewProvenance(source UploadSource, actor Actor, submittedAt time.Time) (Provenance, error) {
	if source.label == "" || actor.id == "" || submittedAt.IsZero() {
		return Provenance{}, fmt.Errorf("provenance requires source, actor, and submission time")
	}
	return Provenance{source: source, submittedBy: actor, submittedAt: canonicalTime(submittedAt)}, nil
}

func (p Provenance) Source() UploadSource   { return p.source }
func (p Provenance) SubmittedBy() Actor     { return p.submittedBy }
func (p Provenance) SubmittedAt() time.Time { return p.submittedAt }

func validateBoundedText(field, src string) error {
	if src == "" || !utf8.ValidString(src) || len(src) > 256 {
		return fmt.Errorf("%s must be nonempty valid UTF-8 of at most 256 bytes", field)
	}
	for _, r := range src {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}
	return nil
}

func canonicalTime(src time.Time) time.Time { return time.Unix(0, src.UnixNano()).UTC() }
