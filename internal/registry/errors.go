package registry

import (
	"errors"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
)

var (
	ErrNotFound       = protocol.ErrNotFound
	ErrConflict       = errors.New("registry conflict")
	ErrCurationDenied = errors.New("curation permission denied")
	ErrTreeMismatch   = errors.New("stored tree does not match its digest")
)
