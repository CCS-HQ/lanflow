//go:build windows

package helper

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
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

func TestLockVerifiedHelperShortPathAndJunction(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "Very Long Helper Directory")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(realDir, "lanflow-helper.exe")
	if err := os.WriteFile(bin, []byte("helper bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	old := ExpectedHelperSHA256
	t.Cleanup(func() { ExpectedHelperSHA256 = old })
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	ExpectedHelperSHA256 = sum

	// 8.3 short path should be accepted when it does not traverse a reparse point.
	if p, err := windows.UTF16PtrFromString(bin); err == nil {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		if n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) < len(buf) {
			shortBin := windows.UTF16ToString(buf[:n])
			release, err := lockVerifiedHelper(shortBin)
			if err != nil {
				t.Fatalf("short path %q rejected: %v", shortBin, err)
			}
			release()
		}
	}

	// A directory junction along the path must still be rejected.
	linkDir := filepath.Join(base, "junction-dir")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", linkDir, realDir).CombinedOutput(); err != nil {
		t.Skipf("mklink /J unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = os.Remove(linkDir) })

	if release, err := lockVerifiedHelper(filepath.Join(linkDir, "lanflow-helper.exe")); err == nil {
		release()
		t.Fatal("helper reached through a directory junction was accepted")
	} else if !strings.Contains(err.Error(), "重解析点") {
		t.Fatalf("expected reparse-point error, got: %v", err)
	}
}
