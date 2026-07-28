package safetree

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	zipDirectoryHeaderLength = 46
	zipDirectoryHeader       = 0x02014b50
	zipDirectoryEndLength    = 22
	zipDirectoryEnd          = 0x06054b50
	zipDirectory64Locator    = 0x07064b50
	zipMaximumCommentLength  = 1<<16 - 1
)

// PreflightZIP checks the on-disk end record and entry count before archive/zip
// allocates and decodes the central directory. ZIP64 and multi-disk archives are
// outside the tree limits and are rejected.
func PreflightZIP(archivePath string, maximumEntries int64) (err error) {
	if maximumEntries <= 0 {
		return fmt.Errorf("ZIP entry maximum must be positive")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open ZIP for preflight: %w", err)
	}
	defer func() { err = errors.Join(err, archive.Close()) }()
	info, err := archive.Stat()
	if err != nil {
		return fmt.Errorf("stat ZIP for preflight: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ZIP spool is not a regular file")
	}
	return preflightZIP(archive, info.Size(), maximumEntries)
}

func preflightZIP(archive io.ReaderAt, size, maximumEntries int64) error {
	if size < zipDirectoryEndLength {
		return fmt.Errorf("ZIP end record is missing")
	}
	searchLength := min(size, int64(zipDirectoryEndLength+zipMaximumCommentLength))
	endBlock := make([]byte, searchLength)
	if _, err := archive.ReadAt(endBlock, size-searchLength); err != nil {
		return fmt.Errorf("read ZIP end record: %w", err)
	}

	endIndex := findZIPEndRecord(endBlock)
	if endIndex < 0 {
		return fmt.Errorf("ZIP end record is missing or truncated")
	}
	endOffset := size - searchLength + int64(endIndex)
	end := endBlock[endIndex:]
	if endOffset >= 20 {
		var locator [4]byte
		if _, err := archive.ReadAt(locator[:], endOffset-20); err != nil {
			return fmt.Errorf("read ZIP64 locator: %w", err)
		}
		if binary.LittleEndian.Uint32(locator[:]) == zipDirectory64Locator {
			return fmt.Errorf("ZIP64 archives are not accepted")
		}
	}

	disk := binary.LittleEndian.Uint16(end[4:6])
	directoryDisk := binary.LittleEndian.Uint16(end[6:8])
	recordsOnDisk := binary.LittleEndian.Uint16(end[8:10])
	records := binary.LittleEndian.Uint16(end[10:12])
	directorySize := binary.LittleEndian.Uint32(end[12:16])
	directoryOffset := binary.LittleEndian.Uint32(end[16:20])
	if recordsOnDisk == ^uint16(0) || records == ^uint16(0) || directorySize == ^uint32(0) || directoryOffset == ^uint32(0) {
		return fmt.Errorf("ZIP64 archives are not accepted")
	}
	if disk != 0 || directoryDisk != 0 || recordsOnDisk != records {
		return fmt.Errorf("multi-disk ZIP archives are not accepted")
	}
	if int64(records) > maximumEntries {
		return &LimitError{Limit: "archive entries", Max: maximumEntries, Actual: int64(records)}
	}

	directoryStart := endOffset - int64(directorySize)
	if directoryStart < 0 {
		return fmt.Errorf("ZIP central directory is outside the archive")
	}
	position := directoryStart
	for range records {
		var header [zipDirectoryHeaderLength]byte
		if _, err := archive.ReadAt(header[:], position); err != nil {
			return fmt.Errorf("read ZIP central directory: %w", err)
		}
		if binary.LittleEndian.Uint32(header[:4]) != zipDirectoryHeader {
			return fmt.Errorf("ZIP central directory entry is malformed")
		}
		nameLength := int64(binary.LittleEndian.Uint16(header[28:30]))
		extraLength := int64(binary.LittleEndian.Uint16(header[30:32]))
		commentLength := int64(binary.LittleEndian.Uint16(header[32:34]))
		position += zipDirectoryHeaderLength + nameLength + extraLength + commentLength
		if position > endOffset {
			return fmt.Errorf("ZIP central directory entry is truncated")
		}
	}
	if position+4 <= endOffset {
		var signature [4]byte
		if _, err := archive.ReadAt(signature[:], position); err != nil {
			return fmt.Errorf("read ZIP central directory tail: %w", err)
		}
		if binary.LittleEndian.Uint32(signature[:]) == zipDirectoryHeader {
			return fmt.Errorf("ZIP central directory has more entries than its end record")
		}
	}
	return nil
}

func findZIPEndRecord(block []byte) int {
	for index := len(block) - zipDirectoryEndLength; index >= 0; index-- {
		if binary.LittleEndian.Uint32(block[index:index+4]) != zipDirectoryEnd {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(block[index+20 : index+22]))
		if index+zipDirectoryEndLength+commentLength > len(block) {
			return -1
		}
		return index
	}
	return -1
}
