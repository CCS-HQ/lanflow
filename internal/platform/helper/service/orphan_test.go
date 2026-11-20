//go:build !windows

package service

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// fakeProcs is a process table of one pid for reapOrphan.
type fakeProcs struct {
	pid     int
	exe     string
	alive   bool
	endsOn  syscall.Signal // the signal that ends it (0 = none does)
	reuseOn syscall.Signal // after this signal the pid runs something else
	signals []syscall.Signal
}

func (f *fakeProcs) procs() procs {
	return procs{
		exePath: func(pid int) (string, error) {
			if pid != f.pid || !f.alive {
				return "", errors.New("no such process")
			}
			return f.exe, nil
		},
		alive: func(pid int) bool { return pid == f.pid && f.alive },
		signal: func(pid int, sig syscall.Signal) error {
			f.signals = append(f.signals, sig)
			if sig == f.endsOn {
				f.alive = false
			}
			if sig == f.reuseOn {
				f.exe = "/usr/bin/something-else"
			}
			return nil
		},
		grace: 30 * time.Millisecond,
		poll:  time.Millisecond,
	}
}

func TestReapOrphan(t *testing.T) {
	const core = "/Library/PrivilegedHelperTools/com.lanflow.mihomo"
	for _, tc := range []struct {
		name    string
		pidFile string // content; "" = no file
		proc    fakeProcs
		want    []syscall.Signal
	}{
		{name: "no pid file", proc: fakeProcs{pid: 700, exe: core, alive: true}},
		{name: "garbage in the pid file", pidFile: "not a pid\n", proc: fakeProcs{pid: 700, exe: core, alive: true}},
		{name: "pid 1 is never touched", pidFile: "1\n", proc: fakeProcs{pid: 1, exe: core, alive: true}},
		{name: "the core is gone", pidFile: "700\n", proc: fakeProcs{pid: 700, exe: core}},
		// After a reboot the number belongs to something else.
		{name: "the pid is another program now", pidFile: "700\n", proc: fakeProcs{pid: 700, exe: "/usr/sbin/sshd", alive: true}},
		// Review item 4: the previous helper died and left its core.
		{name: "orphan stops on interrupt", pidFile: "700\n",
			proc: fakeProcs{pid: 700, exe: core, alive: true, endsOn: syscall.SIGINT},
			want: []syscall.Signal{syscall.SIGINT}},
		{name: "orphan ignores the interrupt", pidFile: "700\n",
			proc: fakeProcs{pid: 700, exe: core, alive: true, endsOn: syscall.SIGKILL},
			want: []syscall.Signal{syscall.SIGINT, syscall.SIGKILL}},
		{name: "the pid changes hands during the grace period", pidFile: "700\n",
			proc: fakeProcs{pid: 700, exe: core, alive: true, reuseOn: syscall.SIGINT},
			want: []syscall.Signal{syscall.SIGINT}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "core.pid")
			if tc.pidFile != "" {
				if err := os.WriteFile(path, []byte(tc.pidFile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f := tc.proc
			reapOrphan(path, core, f.procs(), t.Logf)
			if !slices.Equal(f.signals, tc.want) {
				t.Fatalf("signals = %v, want %v", f.signals, tc.want)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pid file left behind: %v", err)
			}
		})
	}
}

func TestPidFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.pid")
	if got := readPidFile(path); got != 0 {
		t.Fatalf("missing file = %d", got)
	}
	if err := writePidFile(path, 4321); err != nil {
		t.Fatal(err)
	}
	if got := readPidFile(path); got != 4321 {
		t.Fatalf("read %d", got)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary file left behind")
	}
}
