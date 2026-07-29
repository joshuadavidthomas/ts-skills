package server

import (
	"errors"

	"github.com/joshuadavidthomas/ts-skills/internal/protocol"
)

var (
	errNotFound       = protocol.ErrNotFound
	errConflict       = errors.New("registry conflict")
	errCurationDenied = errors.New("curation permission denied")
	errTreeMismatch   = errors.New("stored tree does not match its digest")
)
