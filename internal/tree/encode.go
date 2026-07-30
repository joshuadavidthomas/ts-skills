package tree

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	source, err := NewSource(ctx, tree, ".", PrototypeLimits())
	if err != nil {
		return fmt.Errorf("validate tree archive source: %w", err)
	}
	return encodeSource(ctx, dst, source)
}

func encodeSource(ctx context.Context, dst io.Writer, source Source) (err error) {
	writer := zip.NewWriter(dst)
	for _, file := range source.Files() {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, writer.Close())
		}
		header := &zip.FileHeader{Name: file.Path, Method: zipMethodStore}
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		header.SetMode(0o644)
		output, err := writer.CreateHeader(header)
		if err != nil {
			return errors.Join(fmt.Errorf("create tree archive entry: %w", err), writer.Close())
		}
		input, err := source.Open(file)
		if err != nil {
			return errors.Join(fmt.Errorf("open tree archive source %q: %w", file.Path, err), writer.Close())
		}
		written, copyErr := io.Copy(output, &contextReader{ctx: ctx, source: input})
		closeInputErr := input.Close()
		if err := errors.Join(copyErr, closeInputErr); err != nil {
			return errors.Join(fmt.Errorf("write tree archive entry %q: %w", file.Path, err), writer.Close())
		}
		if written != file.Size {
			return errors.Join(fmt.Errorf("write tree archive entry %q: %w", file.Path, io.ErrUnexpectedEOF), writer.Close())
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
