//go:build windows

package client

import "os"

// Windows does not support syncing a directory through an os.File handle.
// Files are flushed by their writable handles before directory barriers.
func syncDirectoryPath(string) error {
	return nil
}

func syncRootDirectoryPath(*os.Root, string) error {
	return nil
}
