//go:build windows

package install

func rejectPlatformPathComponents(name string) error {
	return rejectWindowsPathComponents(name)
}
