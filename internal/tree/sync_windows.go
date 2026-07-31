//go:build windows

package tree

import (
	"errors"
	"os"
)

func syncFile(name string) error {
	file, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

// Windows does not support syncing a directory through an os.File handle.
// Regular files are flushed by Sync before this barrier is reached.
func syncDirectory(string) error {
	return nil
}
