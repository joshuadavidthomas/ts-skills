package catalog

import (
	"errors"
)

var (
	errNotFound       = errors.New("registry value not found")
	errConflict       = errors.New("registry conflict")
	errCurationDenied = errors.New("curation permission denied")
	errTreeMismatch   = errors.New("stored tree does not match its digest")
)
