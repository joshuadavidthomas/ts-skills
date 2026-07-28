//go:build !windows

package safetree

func invalidPlatformPathComponent(string) bool {
	return false
}

func canonicalPlatformPath(name string) string {
	return name
}
