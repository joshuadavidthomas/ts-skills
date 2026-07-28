//go:build windows

package install

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func filesystemDevice(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var information syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &information); err != nil {
		return 0, fmt.Errorf("identify volume for %q: %w", path, err)
	}
	return uint64(information.VolumeSerialNumber), nil
}

func pathInfoIsLink(info fs.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
