//go:build windows

package catalog

// Windows does not support syncing a directory through an os.File handle.
// Files are flushed by their writable handles before directory barriers.
func syncDirectory(string) error {
	return nil
}
