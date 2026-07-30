package registry

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/joshuadavidthomas/ts-skills/internal/agentskill"
	"github.com/joshuadavidthomas/ts-skills/internal/tree"
)

var ErrInvalidTreeDigest = errors.New("invalid tree digest")

type TreeDigest [sha256.Size]byte

func ParseTreeDigest(src string) (TreeDigest, error) {
	var digest TreeDigest
	if len(src) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(src, "sha256:") {
		return digest, invalidTreeDigest("must use sha256 followed by 64 lowercase hexadecimal digits")
	}
	hexPart := src[len("sha256:"):]
	for _, ch := range hexPart {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return digest, invalidTreeDigest("must use lowercase hexadecimal digits")
		}
	}
	if _, err := hex.Decode(digest[:], []byte(hexPart)); err != nil {
		return TreeDigest{}, invalidTreeDigest(err.Error())
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
		return TreeDigest{}, invalidTree("directory", "must name a tree in a filesystem")
	}
	source, err := tree.NewSource(ctx, fsys, dir, tree.PrototypeLimits())
	if err != nil {
		return TreeDigest{}, fmt.Errorf("%w: tree: %w", agentskill.ErrInvalidTree, err)
	}
	manifest := source.Files()
	entries := make([]treeEntry, 0, len(manifest))
	for _, entry := range manifest {
		file, err := source.Open(entry)
		if err != nil {
			return TreeDigest{}, fmt.Errorf("hash Agent Skill tree %q: %w", dir, err)
		}
		leaf := sha256.New()
		_, _ = leaf.Write([]byte("file\x00"))
		writeUint64(leaf, uint64(len(entry.Path)))
		_, _ = leaf.Write([]byte(entry.Path))
		writeUint64(leaf, uint64(entry.Size))
		copied, copyErr := io.Copy(leaf, &contextReader{ctx: ctx, source: file})
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return TreeDigest{}, fmt.Errorf("hash Agent Skill tree %q: %w", dir, err)
		}
		if copied != entry.Size {
			return TreeDigest{}, fmt.Errorf("file %q changed while hashing", entry.Path)
		}
		var sum [sha256.Size]byte
		copy(sum[:], leaf.Sum(nil))
		entries = append(entries, treeEntry{path: entry.Path, digest: sum})
	}
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

func invalidTreeDigest(problem string) error {
	return fmt.Errorf("%w: digest: %s", ErrInvalidTreeDigest, problem)
}

func invalidTree(field, problem string) error {
	return fmt.Errorf("%w: %s: %s", agentskill.ErrInvalidTree, field, problem)
}
