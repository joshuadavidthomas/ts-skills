//go:build !windows

package safetree

func invalidPlatformPathComponent(string) bool {
	return false
}
