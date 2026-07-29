//go:build !windows

package main

func rejectPlatformPathComponents(string) error {
	return nil
}
