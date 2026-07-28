//go:build !linux

package install

import "fmt"

func filesystemDevice(path string) (uint64, error) {
	return 0, fmt.Errorf("project transactions are unsupported on this platform: cannot identify filesystem for %q", path)
}
