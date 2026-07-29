package protocol

import (
	"archive/zip"
	"errors"
	"fmt"
	"math"

	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

const Version = "v1"

const (
	// TreeArchiveZIPMethod is the only ZIP compression method accepted for a
	// v1 publication tree archive.
	TreeArchiveZIPMethod uint16 = zip.Store

	// TreeArchiveEntryOverheadBytes is the v1 archive allowance for ZIP
	// metadata per file entry, excluding each filename in its local and
	// central-directory headers. It leaves room for encoder metadata without
	// tying the protocol to one encoder's byte layout.
	TreeArchiveEntryOverheadBytes int64 = 256

	treeArchiveEndBytes int64 = 22
)

// TreeArchiveCeiling returns the largest valid v1 publication tree archive
// for limits. A v1 archive is a rootless, classic single-disk ZIP with only
// regular-file entries using TreeArchiveZIPMethod. It has no directory, link,
// encrypted, special-file, or ZIP64 entries. The ceiling allows each file's
// payload, TreeArchiveEntryOverheadBytes of ZIP metadata, and its filename in
// both ZIP headers.
//
// The result reserves one byte below math.MaxInt64 so clients can safely read
// one extra byte to detect an oversized response.
func TreeArchiveCeiling(limits safetree.Limits) (int64, error) {
	if err := safetree.ValidateLimits(limits); err != nil {
		return 0, fmt.Errorf("tree archive limits: %w", err)
	}

	maximum := int64(math.MaxInt64 - 1)
	pathBytes := int64(limits.MaxPathBytes)
	if pathBytes > (maximum-TreeArchiveEntryOverheadBytes)/2 {
		return 0, fmt.Errorf("tree archive ceiling overflows int64")
	}
	entryBytes := TreeArchiveEntryOverheadBytes + 2*pathBytes
	fileCount := int64(limits.MaxFiles)

	payloadBytes := limits.MaxExpandedBytes
	if fileCount <= limits.MaxExpandedBytes/limits.MaxFileBytes {
		payloadBytes = fileCount * limits.MaxFileBytes
	}
	if payloadBytes > maximum-treeArchiveEndBytes ||
		fileCount > (maximum-treeArchiveEndBytes-payloadBytes)/entryBytes {
		return 0, fmt.Errorf("tree archive ceiling overflows int64")
	}
	return payloadBytes + fileCount*entryBytes + treeArchiveEndBytes, nil
}

const (
	HeaderPublicationNamespace = "X-TS-Skills-Publication-Namespace"
	HeaderPublicationName      = "X-TS-Skills-Publication-Name"
	HeaderPublicationDigest    = "X-TS-Skills-Publication-Digest"
)

type CurrentResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Digest    string `json:"digest"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	CodeNotFound       = "not_found"
	CodeInvalidRequest = "invalid_request"
	CodeTooLarge       = "too_large"
	CodeInternal       = "internal"
)

var ErrProtocol = errors.New("invalid registry protocol response")

var ErrNotFound = errors.New("registry value not found")

var ErrInvalidRequest = errors.New("invalid registry request")

var ErrInternal = errors.New("registry internal error")

func StatusForCode(code string) (int, bool) {
	switch code {
	case CodeNotFound:
		return 404, true
	case CodeInvalidRequest:
		return 400, true
	case CodeTooLarge:
		return 413, true
	case CodeInternal:
		return 500, true
	default:
		return 0, false
	}
}
