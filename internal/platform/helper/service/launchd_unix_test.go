//go:build !windows

package service

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLaunchdPID(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want int
	}{
		{"", 0},
		{"system/com.lanflow.helper = {\n\tactive count = 1\n\tstate = running\n\n\tprogram = /Library/PrivilegedHelperTools/com.lanflow.helper\n\tpid = 4242\n\tlast exit code = (never exited)\n}\n", 4242},
		// Not running: launchd prints no pid line.
		{"system/com.lanflow.helper = {\n\tstate = waiting\n\tlast exit code = 1\n}\n", 0},
		// Nested dictionaries carry other "= " lines; only pid counts.
		{"\tparent pid = 1\n\tpid = 1\n", 0},
		{"\tpid = abc\n", 0},
	} {
		if got := launchdPID(tc.out); got != tc.want {
			t.Errorf("launchdPID(%q) = %d, want %d", tc.out, got, tc.want)
		}
	}
}

func TestLogTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "helper.log")
	if got := logTail(p, 0, 3); got != "" {
		t.Errorf("missing file: %q", got)
	}
	old := "old line 1\nold line 2\n"
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	start := logSize(p)
	if got := logTail(p, start, 3); got != "" {
		t.Errorf("nothing new: %q", got)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("a\n\nb\nc\nd\n")
	f.Close()
	if got := logTail(p, start, 3); got != "b | c | d" {
		t.Errorf("tail = %q", got)
	}
	// A log shorter than where it started was rotated: read it all.
	if err := os.WriteFile(p, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, start, 3); got != "new" {
		t.Errorf("rotated: %q", got)
	}
	// Only the last 8 KiB are read.
	big := strings.Repeat("x", 20<<10) + "\nlast\n"
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(p, 0, 1); got != "last" {
		t.Errorf("big: %q", got)
	}
}

// A helper that stops removes the socket path only while it is still its
// own, not one a newer helper created there in the meantime.
func TestRemoveOwnSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "lf") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "s.sock")
	listen := func() (*net.UnixListener, os.FileInfo) {
		t.Helper()
		_ = os.Remove(p)
		l, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		ul := l.(*net.UnixListener)
		ul.SetUnlinkOnClose(false)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return ul, fi
	}
	oldL, oldFI := listen()
	newL, newFI := listen() // the new helper replaces the path
	defer newL.Close()
	oldL.Close()
	removeOwnSocket(p, oldFI)
	if _, err := os.Lstat(p); err != nil {
		t.Fatalf("the old helper removed the new socket: %v", err)
	}
	removeOwnSocket(p, nil)
	if _, err := os.Lstat(p); err != nil {
		t.Fatalf("removed without a record: %v", err)
	}
	removeOwnSocket(p, newFI)
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("own socket left behind: %v", err)
	}
}

func TestWaitGone(t *testing.T) {
	polls := 0
	p := procs{alive: func(int) bool { polls++; return polls < 3 }, poll: time.Millisecond}
	if !waitGone(context.Background(), p, 7, time.Second) {
		t.Error("ended process reported alive")
	}
	p = procs{alive: func(int) bool { return true }, poll: time.Millisecond, signal: func(int, syscall.Signal) error { return nil }}
	if waitGone(context.Background(), p, 7, 10*time.Millisecond) {
		t.Error("live process reported gone")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitGone(ctx, p, 7, time.Hour) {
		t.Error("canceled wait reported gone")
	}
}
