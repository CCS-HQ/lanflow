//go:build windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func winTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// junction makes link a directory junction to target, or skips the test.
func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v: %s", err, out)
	}
}

// Review item 5: the log is rotated through its open handle, to a bare
// name in its own directory.
func TestRotateLogByHandle(t *testing.T) {
	d := winTempDir(t)
	log := filepath.Join(d, "core.log")
	big := strings.Repeat("x", LogRotateSize)
	if err := os.WriteFile(log, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log+".1", []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := openLogFile(log, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if fi, err := os.Stat(log + ".1"); err != nil || fi.Size() != int64(len(big)) {
		t.Fatalf("rotated file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(log); string(b) != "new" {
		t.Fatalf("log = %q", b)
	}

	// A small log is appended to, not rotated.
	w, err = openLogFile(log, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("+more"))
	_ = w.Close()
	if b, _ := os.ReadFile(log); string(b) != "new+more" {
		t.Fatalf("log = %q", b)
	}
}

// A junction swapped into the log's directory after validation: SYSTEM
// neither renames the big file it points at nor leaves a new file there.
func TestRotateLogIgnoresRedirectedDirectory(t *testing.T) {
	d := winTempDir(t)
	victim := filepath.Join(d, "victim")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(victim, "core.log")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", LogRotateSize)), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(d, "logs")
	junction(t, logs, victim)

	if w, err := openLogFile(filepath.Join(logs, "core.log"), ""); err == nil {
		_ = w.Close()
		t.Fatal("opened a log through a junction")
	}
	if _, err := os.Stat(big); err != nil {
		t.Fatalf("the file behind the junction was renamed: %v", err)
	}
	if _, err := os.Stat(big + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a .1 file appeared behind the junction: %v", err)
	}

	// Nothing is created behind the junction either.
	if w, err := openLogFile(filepath.Join(logs, "fresh.log"), ""); err == nil {
		_ = w.Close()
		t.Fatal("created a log through a junction")
	}
	if _, err := os.Stat(filepath.Join(victim, "fresh.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an empty file was left behind the junction: %v", err)
	}
}

func TestRenameByHandleRefusesPaths(t *testing.T) {
	for _, name := range []string{`..\x`, `a\b`, `a/b`, `C:x`, `:stream`} {
		if err := renameByHandle(windows.InvalidHandle, name); err == nil {
			t.Errorf("renameByHandle accepted %q", name)
		}
	}
}

// Review item 1 on Windows: the copy is checked against the digest and a
// link is not followed.
func TestStageFileWindows(t *testing.T) {
	d := winTempDir(t)
	dst := filepath.Join(d, "privileged")
	if err := os.Mkdir(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(d, "mihomo.exe")
	if err := os.WriteFile(src, []byte("core"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum, err := FileSHA256(src)
	if err != nil {
		t.Fatal(err)
	}
	tmp, got, err := stageFile(src, dst, 0o755, sum)
	if err != nil || got != sum {
		t.Fatalf("stage: %s %v", got, err)
	}
	_ = os.Remove(tmp)

	if err := os.WriteFile(src, []byte("evil"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageFile(src, dst, 0o755, sum); !errors.Is(err, errDigest) {
		t.Fatalf("a replaced core: %v", err)
	}
	link := filepath.Join(d, "link.exe")
	if err := os.Symlink(src, link); err == nil {
		if _, _, err := stageFile(link, dst, 0o755, ""); err == nil {
			t.Fatal("staged through a symbolic link")
		}
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

// Review item 2 (the part done): reparse points below the data directory.
func TestVerifyNoLinksWindows(t *testing.T) {
	d := winTempDir(t)
	work := filepath.Join(d, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(d, "runtime.yaml")
	if err := os.WriteFile(config, []byte("mixed-port: 7890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyNoLinks(d, work, true); err != nil {
		t.Fatal(err)
	}
	if err := verifyNoLinks(d, config, false); err != nil {
		t.Fatal(err)
	}
	if err := verifyNoLinks(d, d, true); err != nil {
		t.Fatal(err)
	}
	outside := winTempDir(t)
	linked := filepath.Join(d, "linked")
	junction(t, linked, outside)
	if err := verifyNoLinks(d, linked, true); err == nil {
		t.Fatal("a junction passed as the work dir")
	}
	if err := os.WriteFile(filepath.Join(outside, "c.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyNoLinks(d, filepath.Join(linked, "c.yaml"), false); err == nil {
		t.Fatal("a config below a junction passed")
	}
}

// Review item 4: closing the job's only handle (what happens when the
// helper dies) ends the process in it.
func TestKillOnCloseJob(t *testing.T) {
	cmd := exec.Command("ping", "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	job, err := killOnCloseJob(uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("the process ended before the job was closed")
	case <-time.After(300 * time.Millisecond):
	}
	if err := windows.CloseHandle(job); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the process outlived its job")
	}
}
