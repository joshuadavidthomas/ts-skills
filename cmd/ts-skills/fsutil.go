package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

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
