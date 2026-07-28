package agentskill

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"
)

type TreeDigest [sha256.Size]byte

func ParseTreeDigest(src string) (TreeDigest, error) {
	var digest TreeDigest
	if len(src) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(src, "sha256:") {
		return digest, newValidationError(ErrInvalidTreeDigest, "digest", "must use sha256 followed by 64 lowercase hexadecimal digits")
	}
	hexPart := src[len("sha256:"):]
	for _, ch := range hexPart {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return digest, newValidationError(ErrInvalidTreeDigest, "digest", "must use lowercase hexadecimal digits")
		}
	}
	if _, err := hex.Decode(digest[:], []byte(hexPart)); err != nil {
		return TreeDigest{}, newValidationError(ErrInvalidTreeDigest, "digest", err.Error())
	}
	return digest, nil
}

func (d TreeDigest) String() string { return "sha256:" + hex.EncodeToString(d[:]) }

type treeEntry struct {
	path   string
	digest [sha256.Size]byte
}

// SumTree hashes every regular file under dir into the tree digest. The walk
// and each file stream observe ctx cancellation; cancellation checks never
// contribute to the hash input, so digests are stable.
func SumTree(ctx context.Context, fsys fs.FS, dir string) (TreeDigest, error) {
	if fsys == nil || !fs.ValidPath(dir) {
		return TreeDigest{}, newValidationError(ErrInvalidTree, "directory", "must name a tree in a filesystem")
	}
	var entries []treeEntry
	err := fs.WalkDir(fsys, dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == dir {
			if !entry.IsDir() {
				return newValidationError(ErrInvalidTree, "directory", "tree root must be a directory")
			}
			return nil
		}
		relative := name
		if dir != "." {
			prefix := dir + "/"
			if !strings.HasPrefix(name, prefix) {
				return newValidationError(ErrInvalidTree, "path", fmt.Sprintf("%q is outside the tree root", name))
			}
			relative = strings.TrimPrefix(name, prefix)
		}
		if !validTreePath(relative) {
			return newValidationError(ErrInvalidTree, "path", fmt.Sprintf("%q is unsafe", name))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return newValidationError(ErrInvalidTree, "path", fmt.Sprintf("%q is not a regular file", relative))
		}
		file, err := fsys.Open(name)
		if err != nil {
			return err
		}
		leaf := sha256.New()
		_, _ = leaf.Write([]byte("file\x00"))
		writeUint64(leaf, uint64(len(relative)))
		_, _ = leaf.Write([]byte(relative))
		writeUint64(leaf, uint64(info.Size()))
		copied, copyErr := io.Copy(leaf, &contextReader{ctx: ctx, source: file})
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		if copied != info.Size() {
			return fmt.Errorf("file %q changed while hashing", name)
		}
		var sum [sha256.Size]byte
		copy(sum[:], leaf.Sum(nil))
		entries = append(entries, treeEntry{path: relative, digest: sum})
		return nil
	})
	if err != nil {
		return TreeDigest{}, fmt.Errorf("hash Agent Skill tree %q: %w", dir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	tree := sha256.New()
	_, _ = tree.Write([]byte("ts-skills-tree-v1\x00"))
	for _, entry := range entries {
		_, _ = tree.Write(entry.digest[:])
	}
	var digest TreeDigest
	copy(digest[:], tree.Sum(nil))
	return digest, nil
}

// contextReader aborts a streaming read as soon as ctx is cancelled.
type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func writeUint64(dst io.Writer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = dst.Write(encoded[:])
}

func validTreePath(name string) bool {
	return name != "." && fs.ValidPath(name) && utf8.ValidString(name) && !strings.Contains(name, "\\")
}
