package safetree

func invalidPlatformPathComponent(component string) bool {
	return hasWindowsAlternateDataStream(component)
}
