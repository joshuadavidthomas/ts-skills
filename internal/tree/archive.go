package tree

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
)

const (
	zipMethodStore             = zip.Store
	entryOverheadBytes   int64 = 256
	zipDirectoryEndBytes int64 = 22
)

// ErrInvalid identifies an archive that does not conform to the ts-skills v1
// tree transport format.
var ErrInvalid = errors.New("invalid tree archive")

// MaxArchiveBytes returns a conservative upper bound for a rootless v1 ZIP
// carrying a tree accepted by limits.
func MaxArchiveBytes(limits Limits) (int64, error) {
	if err := ValidateLimits(limits); err != nil {
		return 0, err
	}
	if int64(limits.MaxPathBytes) > (math.MaxInt64-entryOverheadBytes)/2 {
		return 0, fmt.Errorf("tree path limit is too large to bound an archive")
	}
	perEntry := entryOverheadBytes + 2*int64(limits.MaxPathBytes)
	if int64(limits.MaxFiles) > (math.MaxInt64-zipDirectoryEndBytes)/perEntry {
		return 0, fmt.Errorf("tree file limit is too large to bound an archive")
	}
	metadata := int64(limits.MaxFiles)*perEntry + zipDirectoryEndBytes
	if limits.MaxExpandedBytes > math.MaxInt64-metadata {
		return 0, fmt.Errorf("tree expanded-byte limit is too large to bound an archive")
	}
	return limits.MaxExpandedBytes + metadata, nil
}

// Archive owns a bounded temporary ZIP file. Close closes and removes it.
type Archive struct {
	file *os.File
	path string
	size int64
}

// EncodeArchive validates source, writes a durable temporary archive beneath
// parent, enforces maximum bytes, and rewinds it for reading.
func EncodeArchive(ctx context.Context, parent string, source fs.FS, limits Limits, maximum int64) (_ *Archive, err error) {
	validated, err := NewSource(ctx, source, ".", limits)
	if err != nil {
		return nil, fmt.Errorf("validate archive source: %w", err)
	}
	archive, err := newArchive(parent)
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			err = errors.Join(err, archive.Close())
		}
	}()
	if err := encodeSource(ctx, archive.file, validated); err != nil {
		return nil, err
	}
	if err := archive.finish(maximum); err != nil {
		return nil, err
	}
	owned = false
	return archive, nil
}

// ReceiveArchive durably spools a bounded archive stream beneath parent.
func ReceiveArchive(ctx context.Context, parent string, source io.Reader, declared, maximum int64) (_ *Archive, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("archive receive context must be provided")
	}
	if source == nil || maximum <= 0 {
		return nil, fmt.Errorf("archive source and positive byte limit must be provided")
	}
	if declared > maximum {
		return nil, &LimitError{Limit: "download bytes", Max: maximum, Actual: declared}
	}
	archive, err := newArchive(parent)
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			err = errors.Join(err, archive.Close())
		}
	}()
	written, err := copyContext(ctx, archive.file, io.LimitReader(source, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("stage publication archive: %w", err)
	}
	if written > maximum {
		return nil, &LimitError{Limit: "download bytes", Max: maximum, Actual: written}
	}
	if declared >= 0 && written != declared {
		return nil, fmt.Errorf("%w: archive response was truncated", ErrInvalid)
	}
	if err := archive.finish(maximum); err != nil {
		return nil, err
	}
	owned = false
	return archive, nil
}

func newArchive(parent string) (*Archive, error) {
	file, err := os.CreateTemp(parent, ".ts-skills-archive-*.zip")
	if err != nil {
		return nil, fmt.Errorf("create tree archive: %w", err)
	}
	return &Archive{file: file, path: file.Name()}, nil
}

func (a *Archive) finish(maximum int64) error {
	if a == nil || a.file == nil || maximum <= 0 {
		return fmt.Errorf("archive and positive byte limit must be provided")
	}
	info, err := a.file.Stat()
	if err != nil {
		return fmt.Errorf("stat tree archive: %w", err)
	}
	if info.Size() > maximum {
		return &LimitError{Limit: "archive bytes", Max: maximum, Actual: info.Size()}
	}
	if err := a.file.Sync(); err != nil {
		return fmt.Errorf("sync tree archive: %w", err)
	}
	if _, err := a.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind tree archive: %w", err)
	}
	a.size = info.Size()
	return nil
}

func (a *Archive) Read(p []byte) (int, error) {
	if a == nil || a.file == nil {
		return 0, fs.ErrClosed
	}
	return a.file.Read(p)
}

func (a *Archive) Seek(offset int64, whence int) (int64, error) {
	if a == nil || a.file == nil {
		return 0, fs.ErrClosed
	}
	return a.file.Seek(offset, whence)
}

func (a *Archive) Size() int64 {
	if a == nil {
		return 0
	}
	return a.size
}

func (a *Archive) Close() error {
	if a == nil || (a.file == nil && a.path == "") {
		return nil
	}
	var closeErr error
	if a.file != nil {
		closeErr = a.file.Close()
		a.file = nil
	}
	removeErr := os.Remove(a.path)
	if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
		a.path = ""
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}
