package main

import (
	"log"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows/registry"
)

const uninstallKeys = `Software\Microsoft\Windows\CurrentVersion\Uninstall\`

// dropLegacyInstallEntry removes the 「应用和功能」 entry the installers
// up to 0.1.9 wrote under legacyIdentifier, once an installer of this
// version has written its own: both install to the same folder, so the
// old entry would list (and uninstall) the same files a second time. An
// app that was only updated in place still has just the old entry, which
// stays: it is the one way left to uninstall it.
func dropLegacyInstallEntry() {
	if !mygo.App.IsPackaged() {
		return
	}
	if err := dropInstallEntry(registry.CURRENT_USER, uninstallKeys, legacyIdentifier, appIdentifier); err != nil {
		log.Printf("lanflow: remove the old uninstall entry: %v", err)
	}
}

// dropInstallEntry deletes parent\old when parent\cur exists and both
// point at the same InstallLocation.
func dropInstallEntry(root registry.Key, parent, old, cur string) error {
	loc := func(name string) (string, bool) {
		k, err := registry.OpenKey(root, parent+name, registry.QUERY_VALUE)
		if err != nil {
			return "", false
		}
		defer k.Close()
		v, _, err := k.GetStringValue("InstallLocation")
		return v, err == nil
	}
	was, ok := loc(old)
	if !ok {
		return nil
	}
	now, ok := loc(cur)
	if !ok || !sameInstallDir(was, now) {
		return nil
	}
	return registry.DeleteKey(root, parent+old)
}

const (
	loginRunKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	loginApprovedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// dropLegacyLoginItem removes the Run entry MyGo wrote under
// legacyIdentifier for the versions before the service split. applyAutostart
// removes MyGo's login item through mygo.App, which now looks under
// appIdentifier and no longer finds that one; left there it would open the
// window at every login beside the service's own entry.
func dropLegacyLoginItem() {
	for _, path := range []string{loginRunKey, loginApprovedKey} {
		k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
		if err != nil {
			continue
		}
		if err := k.DeleteValue(legacyIdentifier); err != nil && err != registry.ErrNotExist {
			log.Printf("lanflow: remove the old login item: %v", err)
		}
		k.Close()
	}
}
