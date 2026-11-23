package main

import (
	"log"
	"os"
	"path/filepath"
)

// dropLegacyInstallEntry is for the Windows installer's registry entry.
func dropLegacyInstallEntry() {}

// dropLegacyLoginItem removes the launch agent MyGo wrote under
// legacyIdentifier on macOS 12 for the versions before the service split
// (on macOS 13 and later the login item belongs to the app bundle, not to
// a file). applyAutostart removes MyGo's login item through mygo.App,
// which now looks under appIdentifier and no longer finds that one.
func dropLegacyLoginItem() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", legacyIdentifier+".plist")
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		log.Printf("lanflow: remove the old login item: %v", err)
	}
}
