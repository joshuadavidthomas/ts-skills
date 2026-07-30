//go:build windows

package client

func rejectPlatformPathComponents(name string) error {
	return rejectWindowsPathComponents(name)
}
