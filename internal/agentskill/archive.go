package agentskill

import "archive/zip"

const (
	// TreeArchiveZIPMethod is the only ZIP compression method in a v1 tree archive.
	TreeArchiveZIPMethod uint16 = zip.Store

	// TreeArchiveEntryOverheadBytes allows ZIP metadata per file beyond its name.
	TreeArchiveEntryOverheadBytes int64 = 256

	// TreeArchiveMaxBytes bounds v1 archives for PrototypeLimits: 128 MiB payload,
	// 2,048 entries, 1,024-byte names in both headers, and the ZIP end record.
	TreeArchiveMaxBytes int64 = 138936342
)
