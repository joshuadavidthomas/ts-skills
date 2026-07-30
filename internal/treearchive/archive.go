package treearchive

import (
	"archive/zip"
	"errors"
)

const (
	zipMethodStore              = zip.Store
	entryOverheadBytes    int64 = 256
	zipDirectoryEndBytes  int64 = 22
	prototypePayloadBytes int64 = 128 << 20
	prototypeEntries      int64 = 2048
	prototypeNameBytes    int64 = 1024

	// MaxBytes bounds v1 archives for PrototypeLimits: 128 MiB payload,
	// 2,048 entries, 1,024-byte names in both headers, and the ZIP end record.
	MaxBytes = prototypePayloadBytes + prototypeEntries*(entryOverheadBytes+2*prototypeNameBytes) + zipDirectoryEndBytes
)

// ErrInvalid identifies an archive that does not conform to the ts-skills v1
// tree transport format.
var ErrInvalid = errors.New("invalid tree archive")
