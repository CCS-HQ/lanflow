package main

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestDropInstallEntry(t *testing.T) {
	const parent = `Software\LanflowTest\Uninstall\`
	t.Cleanup(func() {
		for _, k := range []string{parent + "old", parent + "cur", `Software\LanflowTest\Uninstall`, `Software\LanflowTest`} {
			_ = registry.DeleteKey(registry.CURRENT_USER, k)
		}
	})
	write := func(name, loc string) {
		t.Helper()
		k, _, err := registry.CreateKey(registry.CURRENT_USER, parent+name, registry.SET_VALUE)
		if err != nil {
			t.Fatal(err)
		}
		defer k.Close()
		if err := k.SetStringValue("InstallLocation", loc); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(name string) bool {
		k, err := registry.OpenKey(registry.CURRENT_USER, parent+name, registry.QUERY_VALUE)
		if err != nil {
			return false
		}
		k.Close()
		return true
	}
	drop := func() {
		t.Helper()
		if err := dropInstallEntry(registry.CURRENT_USER, parent, "old", "cur"); err != nil {
			t.Fatal(err)
		}
	}

	// Only the old entry (an app updated in place): kept.
	write("old", `"C:\Apps\Lanflow"`)
	drop()
	if !exists("old") {
		t.Fatal("the only uninstall entry was removed")
	}
	// A new entry for another folder is a second installation: kept.
	write("cur", `"D:\Apps\Lanflow"`)
	drop()
	if !exists("old") {
		t.Fatal("the entry of another installation was removed")
	}
	// The same folder: the old entry goes, the new one stays.
	write("cur", `"c:\apps\lanflow"`)
	drop()
	if exists("old") || !exists("cur") {
		t.Fatalf("old exists = %v, cur exists = %v", exists("old"), exists("cur"))
	}
}
