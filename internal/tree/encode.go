package tree

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"time"
)

// Encode writes tree as a deterministic, rootless v1 ZIP archive.
func Encode(ctx context.Context, dst io.Writer, tree fs.FS) (err error) {
	if ctx == nil {
		return fmt.Errorf("tree archive context must be provided")
	}
	if dst == nil {
		return fmt.Errorf("tree archive destination must be provided")
	}
	if tree == nil {
		return fmt.Errorf("tree archive filesystem must be provided")
	}

	files := make([]string, 0)
	err = fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tree contains unsupported path %q", name)
		}
		files = append(files, name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("list tree archive files: %w", err)
	}
	sort.Strings(files)

	writer := zip.NewWriter(dst)
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, writer.Close())
		}
		header := &zip.FileHeader{Name: name, Method: zipMethodStore}
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		header.SetMode(0o644)
		output, err := writer.CreateHeader(header)
		if err != nil {
			return errors.Join(fmt.Errorf("create tree archive entry: %w", err), writer.Close())
		}
		input, err := tree.Open(name)
		if err != nil {
			return errors.Join(fmt.Errorf("open tree archive source %q: %w", name, err), writer.Close())
		}
		_, copyErr := io.Copy(output, &contextReader{ctx: ctx, source: input})
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeInputErr); err != nil {
			return errors.Join(fmt.Errorf("write tree archive entry %q: %w", name, err), writer.Close())
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish tree archive: %w", err)
	}
	return nil
}

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
