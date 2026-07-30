//go:build !windows

package client

func rejectPlatformPathComponents(string) error {
	return nil
}
