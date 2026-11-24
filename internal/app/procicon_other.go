//go:build !darwin

package app

import (
	"path/filepath"
	"runtime"
	"strings"
)

// iconSubject is what ProcessIcon draws the icon of for an executable at
// path: the executable itself (its embedded icon on Windows, the icon
// theme's on Linux).
func iconSubject(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}
