package agentskill

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalidName     = errors.New("invalid Agent Skill name")
	ErrInvalidDocument = errors.New("invalid SKILL.md")
	ErrInvalidTree     = errors.New("invalid Agent Skill tree")
)

// ValidationError identifies one invalid field while preserving its validation class.
type ValidationError struct {
	Field   string
	Problem string
	cause   error
}

func newValidationError(cause error, field, problem string) *ValidationError {
	switch cause {
	case ErrInvalidName, ErrInvalidDocument, ErrInvalidTree:
	default:
		panic("agentskill: undeclared validation cause")
	}
	return &ValidationError{Field: field, Problem: problem, cause: cause}
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%v: %s", e.cause, e.Problem)
	}
	return fmt.Sprintf("%v: %s: %s", e.cause, e.Field, e.Problem)
}

func (e *ValidationError) Unwrap() error { return e.cause }

// Name is the NFKC-canonical Agent Skill name.
type Name struct {
	canonical string
}

func ParseName(src string) (Name, error) {
	if !utf8.ValidString(src) {
		return Name{}, newValidationError(ErrInvalidName, "name", "must be valid UTF-8")
	}
	canonical := norm.NFKC.String(src)
	count := utf8.RuneCountInString(canonical)
	if count < 1 || count > 64 {
		return Name{}, newValidationError(ErrInvalidName, "name", "must contain 1 to 64 Unicode scalar values")
	}
	previousHyphen := false
	for index, r := range canonical {
		if r == '-' {
			if index == 0 || previousHyphen {
				return Name{}, newValidationError(ErrInvalidName, "name", "hyphens must be single and internal")
			}
			previousHyphen = true
			continue
		}
		if !unicode.IsLower(r) && !unicode.IsNumber(r) {
			return Name{}, newValidationError(ErrInvalidName, "name", "must contain only lowercase Unicode letters, numbers, and hyphens")
		}
		previousHyphen = false
	}
	if previousHyphen {
		return Name{}, newValidationError(ErrInvalidName, "name", "hyphens must be single and internal")
	}
	return Name{canonical: canonical}, nil
}

func (n Name) String() string { return n.canonical }
