//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !windows

package install

import (
	"fmt"
	"io/fs"
	"os"
)

func filesystemDevice(path string) (uint64, error) {
	return 0, fmt.Errorf("project transactions are unsupported on this platform: cannot identify filesystem for %q", path)
}

func pathInfoIsLink(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}
