//go:build windows

package fsx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsCommittedFileNotHiddenAndReadOnlyFailsFast(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "config.yaml")
	if err := WriteFile(dst, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		t.Fatal(err)
	}
	if attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
		t.Fatalf("committed file should not remain hidden (attrs=%#x)", attrs)
	}

	// Mark destination read-only; overwriting must fail fast instead of
	// burning the 3s busyWait retry budget.
	if err := windows.SetFileAttributes(p, attrs|windows.FILE_ATTRIBUTE_READONLY); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.SetFileAttributes(p, attrs) })

	tmp, err := CreateTemp(dst, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	tmpName := tmp.Name()
	tp, _ := windows.UTF16PtrFromString(tmpName)
	if tattrs, err := windows.GetFileAttributes(tp); err == nil && tattrs&windows.FILE_ATTRIBUTE_HIDDEN == 0 {
		t.Errorf("temp file %s was not hidden (attrs=%#x)", tmpName, tattrs)
	}
	_, _ = tmp.WriteString("new")
	_ = tmp.Close()
	t.Cleanup(func() { _ = os.Remove(tmpName) })

	start := time.Now()
	err = Rename(tmpName, dst)
	elapsed := time.Since(start)
	if err == nil {
		// Note: on Windows MoveFileExW with MOVEFILE_REPLACE_EXISTING fails
		// with ERROR_ACCESS_DENIED on FILE_ATTRIBUTE_READONLY.
		t.Fatal("expected error overwriting read-only destination")
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("read-only destination rename took %s, expected fast fail", elapsed)
	}
}
