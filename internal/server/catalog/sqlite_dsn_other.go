//go:build !windows

package catalog

import (
	"net/url"
	"path/filepath"
)

func sqliteDSN(databasePath string, values url.Values) string {
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(databasePath),
		RawQuery: values.Encode(),
	}).String()
}
