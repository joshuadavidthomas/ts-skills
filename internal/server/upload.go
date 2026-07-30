package server

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

	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

var errMalformedUpload = errors.New("malformed skill upload")

// Submission owns one validated, staged upload tree. Snapshot lends that tree
// to a caller; the caller must not close it. Close remains Submission's job.
type submission struct {
	snapshot      *tree.Snapshot
	root          string
	label         string
	closeSnapshot func(*tree.Snapshot) error
}

// Snapshot lends the validated staged tree. The Submission retains ownership;
// callers must not close the returned snapshot.
func (s *submission) Snapshot() *tree.Snapshot {
	if s == nil {
		return nil
	}
	return s.snapshot
}

func (s *submission) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *submission) Label() string {
	if s == nil {
		return ""
	}
	return s.label
}

func (s *submission) Close() error {
	if s == nil || s.snapshot == nil {
		return nil
	}
	closeSnapshot := s.closeSnapshot
	if closeSnapshot == nil {
		closeSnapshot = (*tree.Snapshot).Close
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

func stageBrowserDirectory(ctx context.Context, parent string, body *multipart.Reader, limits tree.Limits) (_ *submission, err error) {
	if body == nil {
		return nil, malformed("multipart directory parts are missing", nil)
	}
	if err := tree.ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("directory staging limits: %w", err)
	}
	manifest, err := body.NextPart()
	if err != nil {
		return nil, malformed("a directory manifest must be the first part", err)
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
		return nil, &tree.LimitError{Limit: "manifest bytes", Max: manifestLimit, Actual: int64(len(manifestBytes))}
	}
	entries, root, err := decodeManifest(manifestBytes, limits)
	if err != nil {
		return nil, err
	}

	builder, err := tree.NewBuilder(parent, limits)
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
		addErr := builder.AddFile(ctx, entry.Path, entry.Size, part)
		if addErr != nil {
			switch {
			case errors.Is(addErr, tree.ErrLimitExceeded):
				return nil, addErr
			case errors.Is(addErr, context.Canceled), errors.Is(addErr, context.DeadlineExceeded):
				return nil, addErr
			case errors.Is(addErr, tree.ErrInvalidPath):
				return nil, malformed("directory path is unsafe or collides with another path", addErr)
			case errors.Is(addErr, tree.ErrSizeMismatch):
				return nil, malformed(fmt.Sprintf("%s size does not match its manifest entry", expectedName), addErr)
			default:
				return nil, fmt.Errorf("stage uploaded file %q: %w", entry.Path, addErr)
			}
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
	return &submission{snapshot: snapshot, root: root, label: root}, nil
}

func decodeManifest(src []byte, limits tree.Limits) ([]manifestEntry, string, error) {
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
		return nil, "", &tree.LimitError{Limit: "files", Max: int64(limits.MaxFiles), Actual: int64(len(entries))}
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
			return nil, "", &tree.LimitError{Limit: "file bytes", Max: limits.MaxFileBytes, Actual: entry.Size}
		}
		if err := validateUploadPath(entry.Path, limits); err != nil {
			if errors.Is(err, tree.ErrLimitExceeded) {
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

func validateUploadPath(name string, limits tree.Limits) error {
	if name == "." || !fs.ValidPath(name) || !utf8.ValidString(name) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || isWindowsAbsolute(name) {
		return fmt.Errorf("%w: %q", tree.ErrInvalidPath, name)
	}
	if len(name) > limits.MaxPathBytes {
		return &tree.LimitError{Limit: "path bytes", Max: int64(limits.MaxPathBytes), Actual: int64(len(name))}
	}
	depth := strings.Count(name, "/") + 1
	if depth > limits.MaxDepth {
		return &tree.LimitError{Limit: "path depth", Max: int64(limits.MaxDepth), Actual: int64(depth)}
	}
	return nil
}

func isWindowsAbsolute(name string) bool {
	return len(name) >= 3 && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z')) && name[1] == ':' && name[2] == '/'
}

func malformed(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", errMalformedUpload, problem)
	}
	return fmt.Errorf("%w: %s: %w", errMalformedUpload, problem, cause)
}
