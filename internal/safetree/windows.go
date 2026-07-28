package safetree

import "strings"

const windowsReservedCharacters = `<>:"/\|?*`

// InvalidWindowsPathComponent reports whether Windows may reject a component
// or resolve it as another filesystem name.
func InvalidWindowsPathComponent(component string) bool {
	if component == "" {
		return false
	}
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return true
	}
	for _, character := range component {
		if character <= 31 || strings.ContainsRune(windowsReservedCharacters, character) {
			return true
		}
	}
	return isWindowsReservedDeviceName(component)
}

func isWindowsReservedDeviceName(component string) bool {
	base := component
	if separator := strings.IndexAny(base, ".:"); separator >= 0 {
		base = base[:separator]
	}
	base = strings.TrimRight(base, " ")
	upper := strings.ToUpper(base)
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return true
	}
	if !strings.HasPrefix(upper, "COM") && !strings.HasPrefix(upper, "LPT") {
		return false
	}
	switch upper[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	default:
		return false
	}
}

func canonicalPath(name string) string {
	return strings.ToUpper(name)
}
