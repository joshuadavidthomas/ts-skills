//go:build linux

package install

import (
	"fmt"
	"os"
	"syscall"
)

func filesystemDevice(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("filesystem identity is unavailable for %q", path)
	}
	return uint64(stat.Dev), nil
}
