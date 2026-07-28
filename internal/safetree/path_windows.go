//go:build windows

package safetree

func invalidPlatformPathComponent(component string) bool {
	return InvalidWindowsPathComponent(component)
}

func canonicalPlatformPath(name string) string {
	return windowsCanonicalPath(name)
}
