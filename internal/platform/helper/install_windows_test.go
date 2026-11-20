//go:build windows

package helper

import (
	"os"
	"strings"
	"testing"
)

// While the verified helper is held, nobody can rewrite, replace or
// delete it: what the UAC prompt starts is what was hashed.
func TestLockVerifiedHelper(t *testing.T) {
	bin := writeBin(t, "lanflow-helper.exe", "helper bytes")
	old := ExpectedHelperSHA256
	t.Cleanup(func() { ExpectedHelperSHA256 = old })

	ExpectedHelperSHA256 = strings.Repeat("0", 64)
	if _, err := lockVerifiedHelper(bin); err == nil {
		t.Fatal("a helper with another digest than the build recorded was accepted")
	}
	sum, _ := fileSHA256(bin)
	ExpectedHelperSHA256 = sum
	release, err := lockVerifiedHelper(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("swapped"), 0o755); err == nil {
		t.Error("the locked helper could be rewritten")
	}
	if err := os.Rename(bin, bin+".moved"); err == nil {
		t.Error("the locked helper could be renamed")
	}
	if err := os.Remove(bin); err == nil {
		t.Error("the locked helper could be deleted")
	}
	// Reading stays possible (the elevated installer copies itself).
	if b, err := os.ReadFile(bin); err != nil || string(b) != "helper bytes" {
		t.Errorf("read while locked: %q %v", b, err)
	}
	release()
	if err := os.WriteFile(bin, []byte("free again"), 0o755); err != nil {
		t.Errorf("after release: %v", err)
	}
	if _, err := lockVerifiedHelper(bin + ".missing"); err == nil {
		t.Error("a missing helper was locked")
	}
}
