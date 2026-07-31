//go:build !windows

package catalog

import (
	"fmt"
	"io/fs"
	"os"
)

func setPrivateDirectoryPermissions(name string) error {
	return os.Chmod(name, 0o700)
}

func verifyPrivateDirectoryPermissions(name string, info fs.FileInfo) error {
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%q has mode %04o, want 0700", name, info.Mode().Perm())
	}
	return nil
}
