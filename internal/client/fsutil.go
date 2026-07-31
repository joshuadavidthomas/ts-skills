package client

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const managedTempAttempts = 100

func syncDirectory(path string) error {
	if err := rejectPathComponents(path, false); err != nil {
		return err
	}
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

func syncRootDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}

func writeSyncedFile(path string, contents []byte, mode fs.FileMode) error {
	if err := rejectPathComponents(path, true); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	_, writeErr := file.Write(contents)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func (w *projectWriter) writeSyncedFile(path string, contents []byte, mode fs.FileMode) error {
	if err := rejectPathComponents(path, true); err != nil {
		return err
	}
	name, err := w.project.managedName(path)
	if err != nil {
		return err
	}
	file, err := w.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	_, writeErr := file.Write(contents)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func (w *projectWriter) mkdirTemp(parent, prefix string) (string, error) {
	parentName, err := w.project.managedName(parent)
	if err != nil {
		return "", err
	}
	for range managedTempAttempts {
		base := prefix + strings.ToLower(rand.Text())
		name := filepath.Join(parentName, base)
		if err := w.root.Mkdir(name, 0o700); err == nil {
			return filepath.Join(parent, base), nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("create unique temporary directory in %q", parent)
}

func (w *projectWriter) createTemp(parent, prefix string) (*os.File, string, error) {
	parentName, err := w.project.managedName(parent)
	if err != nil {
		return nil, "", err
	}
	for range managedTempAttempts {
		base := prefix + strings.ToLower(rand.Text())
		name := filepath.Join(parentName, base)
		file, err := w.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, filepath.Join(parent, base), nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("create unique temporary file in %q", parent)
}
