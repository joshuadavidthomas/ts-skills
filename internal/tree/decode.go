package tree

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"math"
)

// Decode validates and stages a v1 tree archive as a bounded portable tree.
func Decode(ctx context.Context, archivePath, stagingParent string, limits Limits) (_ *Snapshot, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("tree archive context must be provided")
	}
	if err := ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("tree archive limits: %w", err)
	}
	maximumEntries := int64(limits.MaxFiles)
	if err := preflight(archivePath, maximumEntries); err != nil {
		if errors.Is(err, ErrLimitExceeded) {
			return nil, err
		}
		return nil, invalid("inspect ZIP directory", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, invalid("open ZIP", err)
	}
	defer func() { err = errors.Join(err, archive.Close()) }()
	if int64(len(archive.File)) > maximumEntries {
		return nil, &LimitError{Limit: "archive entries", Max: maximumEntries, Actual: int64(len(archive.File))}
	}
	builder, err := NewBuilder(stagingParent, limits)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, builder.Close())
		}
	}()
	for _, entry := range archive.File {
		if entry.Method != zipMethodStore || entry.Flags&0x1 != 0 || entry.FileInfo().IsDir() || !entry.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: unsupported entry %q", ErrInvalid, entry.Name)
		}
		if entry.UncompressedSize64 > math.MaxInt64 {
			return nil, &LimitError{Limit: "file bytes", Max: limits.MaxFileBytes, Actual: math.MaxInt64}
		}
		input, openErr := entry.Open()
		if openErr != nil {
			return nil, invalid("open ZIP entry", openErr)
		}
		addErr := builder.AddFile(ctx, entry.Name, int64(entry.UncompressedSize64), input)
		closeErr := input.Close()
		if addErr != nil {
			switch {
			case errors.Is(addErr, ErrLimitExceeded):
				return nil, addErr
			case errors.Is(addErr, context.Canceled), errors.Is(addErr, context.DeadlineExceeded):
				return nil, addErr
			default:
				return nil, invalid("stage unsafe ZIP entry", addErr)
			}
		}
		if closeErr != nil {
			return nil, invalid("close ZIP entry", closeErr)
		}
	}
	snapshot, err := builder.Finish()
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func invalid(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrInvalid, operation, err)
}
