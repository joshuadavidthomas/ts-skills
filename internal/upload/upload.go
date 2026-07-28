package upload

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"os"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/joshuadavidthomas/ts-skill-registry/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skill-registry/internal/safetree"
)

const maxZIPBytes int64 = 32 << 20

var ErrMalformedUpload = errors.New("malformed skill upload")

type Submission struct {
	snapshot *safetree.Snapshot
	root     string
	label    string
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
	err := s.snapshot.Close()
	s.snapshot = nil
	return err
}

func StageZIP(ctx context.Context, parent string, src io.Reader, filename string, limits safetree.Limits) (submission *Submission, err error) {
	if src == nil {
		return nil, malformed("ZIP reader is missing", nil)
	}
	if err := safetree.ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("ZIP staging limits: %w", err)
	}
	label, err := zipLabel(filename)
	if err != nil {
		return nil, err
	}

	spool, err := os.CreateTemp(parent, ".ts-skills-upload-*.zip")
	if err != nil {
		return nil, fmt.Errorf("create ZIP staging file: %w", err)
	}
	spoolName := spool.Name()
	defer func() {
		removeErr := os.Remove(spoolName)
		if removeErr == nil {
			return
		}
		if submission != nil {
			removeErr = errors.Join(removeErr, submission.Close())
			submission = nil
		}
		err = errors.Join(err, removeErr)
	}()

	written, copyErr := copyWithContext(ctx, spool, io.LimitReader(src, maxZIPBytes+1))
	closeErr := spool.Close()
	if copyErr != nil || closeErr != nil {
		return nil, fmt.Errorf("spool ZIP upload: %w", errors.Join(copyErr, closeErr))
	}
	if written > maxZIPBytes {
		return nil, &safetree.LimitError{Limit: "request bytes", Max: maxZIPBytes, Actual: written}
	}
	archive, err := zip.OpenReader(spoolName)
	if err != nil {
		return nil, malformed("cannot read ZIP archive", err)
	}
	defer archive.Close()

	root, synthetic, err := inspectZIP(archive.File, limits)
	if err != nil {
		return nil, err
	}
	if synthetic {
		skillFile := findZIPFile(archive.File, agentskill.Filename)
		contents, readErr := readZIPFile(skillFile, limits.MaxFileBytes)
		if readErr != nil {
			return nil, readErr
		}
		document, parseErr := agentskill.Parse(contents)
		if parseErr != nil {
			return nil, malformed("root SKILL.md is invalid", parseErr)
		}
		root = document.Name.String()
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
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := entry.Name
		if synthetic {
			name = root + "/" + name
		}
		input, openErr := entry.Open()
		if openErr != nil {
			return nil, malformed("cannot open ZIP entry", openErr)
		}
		if entry.UncompressedSize64 > uint64(^uint64(0)>>1) {
			_ = input.Close()
			return nil, &safetree.LimitError{Limit: "file bytes", Max: limits.MaxFileBytes, Actual: int64(^uint64(0) >> 1)}
		}
		addErr := builder.AddFile(ctx, name, int64(entry.UncompressedSize64), input)
		closeErr := input.Close()
		if addErr != nil {
			if errors.Is(addErr, safetree.ErrLimitExceeded) {
				return nil, addErr
			}
			return nil, malformed("ZIP entry is unsafe or collides with another entry", addErr)
		}
		if closeErr != nil {
			return nil, malformed("cannot finish ZIP entry", closeErr)
		}
	}
	snapshot, err := builder.Finish()
	if err != nil {
		return nil, err
	}
	return &Submission{snapshot: snapshot, root: root, label: label}, nil
}

type manifestEntry struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
}

func StageBrowserDirectory(ctx context.Context, parent string, body *multipart.Reader, limits safetree.Limits) (_ *Submission, err error) {
	if body == nil {
		return nil, malformed("multipart reader is missing", nil)
	}
	if err := safetree.ValidateLimits(limits); err != nil {
		return nil, fmt.Errorf("directory staging limits: %w", err)
	}
	part, err := body.NextPart()
	if err != nil {
		return nil, malformed("manifest must be the first directory part", err)
	}
	if part.FormName() != "manifest" || part.FileName() != "" {
		return nil, malformed("manifest must be the first directory part", nil)
	}
	manifestLimit := int64(limits.MaxFiles)*(int64(limits.MaxPathBytes)+96) + 2
	manifestBytes, readErr := io.ReadAll(io.LimitReader(part, manifestLimit+1))
	if readErr != nil {
		return nil, malformed("cannot read directory manifest", readErr)
	}
	if int64(len(manifestBytes)) > manifestLimit {
		return nil, &safetree.LimitError{Limit: "manifest bytes", Max: manifestLimit, Actual: int64(len(manifestBytes))}
	}
	manifest, root, err := decodeManifest(manifestBytes, limits)
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
	for _, entry := range manifest {
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

func inspectZIP(entries []*zip.File, limits safetree.Limits) (root string, synthetic bool, err error) {
	type node struct {
		name string
		dir  bool
	}
	nodes := make([]node, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	rootSkill := false
	wrappers := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Flags&0x1 != 0 {
			return "", false, malformed("encrypted ZIP entries are not accepted", nil)
		}
		isDir := entry.FileInfo().IsDir()
		name := entry.Name
		if isDir {
			name = strings.TrimSuffix(name, "/")
		}
		if name == "" {
			return "", false, malformed("ZIP contains an empty path", nil)
		}
		if err := validateUploadPath(name, limits); err != nil {
			if errors.Is(err, safetree.ErrLimitExceeded) {
				return "", false, err
			}
			return "", false, malformed("ZIP contains an unsafe path", err)
		}
		mode := entry.Mode()
		if !isDir && !mode.IsRegular() {
			return "", false, malformed("ZIP contains a link or special file", nil)
		}
		if _, exists := seen[name]; exists {
			return "", false, malformed("ZIP contains duplicate paths", nil)
		}
		seen[name] = struct{}{}
		nodes = append(nodes, node{name: name, dir: isDir})
		if !isDir && name == agentskill.Filename {
			rootSkill = true
		}
		if !isDir {
			first, rest, found := strings.Cut(name, "/")
			if found && rest == agentskill.Filename {
				wrappers[first] = struct{}{}
			}
		}
	}
	for i, left := range nodes {
		if left.dir {
			continue
		}
		for j, right := range nodes {
			if i != j && strings.HasPrefix(right.name, left.name+"/") {
				return "", false, malformed("ZIP contains a file/directory prefix collision", nil)
			}
		}
	}
	possibilities := len(wrappers)
	if rootSkill {
		possibilities++
	}
	if possibilities != 1 {
		return "", false, malformed("ZIP must contain one unambiguous SKILL.md root", nil)
	}
	if rootSkill {
		return "", true, nil
	}
	for wrapper := range wrappers {
		for _, node := range nodes {
			if node.name != wrapper && !strings.HasPrefix(node.name, wrapper+"/") {
				return "", false, malformed("ZIP wrapper must contain every archive entry", nil)
			}
		}
		return wrapper, false, nil
	}
	panic("unreachable")
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

func zipLabel(filename string) (string, error) {
	if filename == "" {
		return "upload.zip", nil
	}
	if !utf8.ValidString(filename) {
		return "", malformed("ZIP filename must be valid UTF-8", nil)
	}
	for _, r := range filename {
		if unicode.IsControl(r) {
			return "", malformed("ZIP filename must not contain control characters", nil)
		}
	}
	label := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if label == "" || label == "." || label == "/" {
		return "", malformed("ZIP filename must have a basename", nil)
	}
	if len(label) > 256 {
		return "", malformed("ZIP filename basename is longer than 256 bytes", nil)
	}
	return label, nil
}

func findZIPFile(entries []*zip.File, name string) *zip.File {
	for _, entry := range entries {
		if entry.Name == name && !entry.FileInfo().IsDir() {
			return entry
		}
	}
	return nil
}

func readZIPFile(entry *zip.File, maximum int64) ([]byte, error) {
	if entry == nil {
		return nil, malformed("ZIP entry is missing", nil)
	}
	input, err := entry.Open()
	if err != nil {
		return nil, malformed("cannot open ZIP entry", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(input, maximum+1))
	closeErr := input.Close()
	if readErr != nil || closeErr != nil {
		return nil, malformed("cannot read ZIP entry", errors.Join(readErr, closeErr))
	}
	if int64(len(contents)) > maximum {
		return nil, &safetree.LimitError{Limit: "file bytes", Max: maximum, Actual: int64(len(contents))}
	}
	return contents, nil
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

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			written, writeErr := destination.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func malformed(problem string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrMalformedUpload, problem)
	}
	return fmt.Errorf("%w: %s: %w", ErrMalformedUpload, problem, cause)
}
