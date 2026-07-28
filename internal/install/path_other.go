//go:build !windows

package install

func rejectPlatformPathComponents(string) error {
	return nil
}
