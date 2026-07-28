package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"strings"
	"unicode/utf8"

	"github.com/joshuadavidthomas/ts-skills/internal/safetree"
)

var ErrMalformedUpload = errors.New("malformed skill upload")

type Submission struct {
	snapshot      *safetree.Snapshot
	root          string
	label         string
	closeSnapshot func(*safetree.Snapshot) error
}

func (s *Submission) FS() fs.FS {
	if s == nil || s.snapshot == nil {
		return nil
	}
	return s.snapshot.FS()
}

func (s *Submission) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *Submission) Label() string {
	if s == nil {
		return ""
	}
	return s.label
}

func (s *Submission) Close() error {
	if s == nil || s.snapshot == nil {
		return nil
	}
	closeSnapshot := s.closeSnapshot
	if closeSnapshot == nil {
		closeSnapshot = (*safetree.Snapshot).Close
	}
	if err := closeSnapshot(s.snapshot); err != nil {
		return err
	}
	s.snapshot = nil
	return nil
}

type manifestEntry struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
}

func StageBrowserDirectory(ctx context.Context, parent string, manifest *multipart.Part, body *multipart.Reader, limits safetree.Limits) (_ *Submission, err error) {
	if manifest == nil || body == nil {
		return nil, malformed("multipart directory parts are missing", nil)
	}
	if err := safetree.ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("directory staging limits: %w", err)
	}
	if manifest.FormName() != "manifest" || manifest.FileName() != "" {
		return nil, malformed("manifest must be the first directory part", nil)
	}
	manifestLimit := int64(limits.MaxFiles)*(int64(limits.MaxPathBytes)+96) + 2
	manifestBytes, readErr := io.ReadAll(io.LimitReader(manifest, manifestLimit+1))
	if readErr != nil {
		return nil, malformed("cannot read directory manifest", readErr)
	}
	if int64(len(manifestBytes)) > manifestLimit {
		return nil, &safetree.LimitError{Limit: "manifest bytes", Max: manifestLimit, Actual: int64(len(manifestBytes))}
	}
	entries, root, err := decodeManifest(manifestBytes, limits)
	if err != nil {
		return nil, err
	}

	builder, err := safetree.NewBuilder(parent, limits)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, builder.Close())
		}
	}()
	for _, entry := range entries {
		part, nextErr := body.NextPart()
		if nextErr != nil {
			return nil, malformed(fmt.Sprintf("file-%d is missing", entry.Index), nextErr)
		}
		expectedName := fmt.Sprintf("file-%d", entry.Index)
		if part.FormName() != expectedName || part.FileName() == "" {
			return nil, malformed(fmt.Sprintf("expected file multipart part %s", expectedName), nil)
		}
		counter := &countingReader{source: part}
		addErr := builder.AddFile(ctx, entry.Path, entry.Size, counter)
		if addErr != nil {
			if errors.Is(addErr, safetree.ErrLimitExceeded) {
				return nil, addErr
			}
			return nil, malformed("directory path is unsafe or collides with another path", addErr)
		}
		if counter.count != entry.Size {
			return nil, malformed(fmt.Sprintf("%s size does not match its manifest entry", expectedName), nil)
		}
	}
	if extra, nextErr := body.NextPart(); nextErr != io.EOF {
		if nextErr != nil {
			return nil, malformed("cannot finish multipart directory", nextErr)
		}
		_ = extra.Close()
		return nil, malformed("directory upload contains an extra multipart part", nil)
	}
	snapshot, err := builder.Finish()
	if err != nil {
		return nil, err
	}
	return &Submission{snapshot: snapshot, root: root, label: root}, nil
}

func decodeManifest(src []byte, limits safetree.Limits) ([]manifestEntry, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(src))
	decoder.DisallowUnknownFields()
	var entries []manifestEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, "", malformed("directory manifest is not a valid JSON array", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, "", malformed("directory manifest contains trailing data", err)
	}
	if len(entries) == 0 {
		return nil, "", malformed("directory manifest is empty", nil)
	}
	if len(entries) > limits.MaxFiles {
		return nil, "", &safetree.LimitError{Limit: "files", Max: int64(limits.MaxFiles), Actual: int64(len(entries))}
	}
	root := ""
	for position, entry := range entries {
		if entry.Index != position {
			return nil, "", malformed("manifest indexes must be unique, ordered, and contiguous from zero", nil)
		}
		if entry.Size < 0 {
			return nil, "", malformed(fmt.Sprintf("manifest file-%d has a negative size", entry.Index), nil)
		}
		if entry.Size > limits.MaxFileBytes {
			return nil, "", &safetree.LimitError{Limit: "file bytes", Max: limits.MaxFileBytes, Actual: entry.Size}
		}
		if err := validateUploadPath(entry.Path, limits); err != nil {
			if errors.Is(err, safetree.ErrLimitExceeded) {
				return nil, "", err
			}
			return nil, "", malformed("directory manifest contains an unsafe path", err)
		}
		selectedRoot, _, found := strings.Cut(entry.Path, "/")
		if !found {
			return nil, "", malformed("every directory path must begin with the selected root", nil)
		}
		if root == "" {
			root = selectedRoot
		} else if selectedRoot != root {
			return nil, "", malformed("directory manifest contains more than one selected root", nil)
		}
	}
	return entries, root, nil
}

func validateUploadPath(name string, limits safetree.Limits) error {
	if name == "." || !fs.ValidPath(name) || !utf8.ValidString(name) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || isWindowsAbsolute(name) {
		return fmt.Errorf("%w: %q", safetree.ErrInvalidPath, name)
	}
	if len(name) > limits.MaxPathBytes {
		return &safetree.LimitError{Limit: "path bytes", Max: int64(limits.MaxPathBytes), Actual: int64(len(name))}
	}
	depth := strings.Count(name, "/") + 1
	if depth > limits.MaxDepth {
		return &safetree.LimitError{Limit: "path depth", Max: int64(limits.MaxDepth), Actual: int64(depth)}
	}
	return nil
}

func isWindowsAbsolute(name string) bool {
	return len(name) >= 3 && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) && name[1] == ':' && name[2] == '/'
}

type countingReader struct {
	source io.Reader
	count  int64
}

func (r *countingReader) Read(dst []byte) (int, error) {
	n, err := r.source.Read(dst)
	r.count += int64(n)
	return n, err
}

func malformed(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrMalformedUpload, problem)
	}
	return fmt.Errorf("%w: %s: %w", ErrMalformedUpload, problem, cause)
}
