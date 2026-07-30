package treearchive

import "archive/zip"

const (
	// ZIPMethod is the only ZIP compression method in a v1 tree archive.
	ZIPMethod uint16 = zip.Store

	// EntryOverheadBytes allows ZIP metadata per file beyond its name.
	EntryOverheadBytes int64 = 256

	// MaxBytes bounds v1 archives for PrototypeLimits: 128 MiB payload,
	// 2,048 entries, 1,024-byte names in both headers, and the ZIP end record.
	MaxBytes int64 = 138936342
)
