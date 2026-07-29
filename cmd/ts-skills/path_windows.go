//go:build windows

package main

func rejectPlatformPathComponents(name string) error {
	return rejectWindowsPathComponents(name)
}
