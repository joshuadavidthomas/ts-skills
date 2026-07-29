//go:build !windows

package main

import (
	"io/fs"
	"os"
)

func pathInfoIsLink(info fs.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }
