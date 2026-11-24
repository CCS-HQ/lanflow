package main

import (
	"path/filepath"
	"strings"
)

// appIdentifier is the identifier of mygo.json (TestIdentifierMatchesManifest
// keeps the two together).
const appIdentifier = "com.lanflow.app"

// sameInstallDir reports whether two InstallLocation values of the
// installer name the same folder; it writes them quoted.
func sameInstallDir(a, b string) bool {
	clean := func(s string) string {
		s = strings.Trim(strings.TrimSpace(s), `"`)
		if s == "" {
			return ""
		}
		return strings.ToLower(filepath.Clean(s))
	}
	ca, cb := clean(a), clean(b)
	return ca != "" && ca == cb
}
