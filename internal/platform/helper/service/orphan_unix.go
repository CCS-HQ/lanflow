//go:build !windows

package service

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The supervisor's state lives in memory, and Mihomo runs in a process
// group of its own, so a helper that panics or is killed leaves a root
// Mihomo behind: launchd starts a new helper that knows nothing of it,
// and the user cannot end a root process. The pid file (in a directory
// only root writes) lets the next helper find and end it.

// procs are the process calls of reapOrphan; tests replace them.
type procs struct {
	// exePath is the executable process pid runs.
	exePath func(pid int) (string, error)
	alive   func(pid int) bool
	signal  func(pid int, sig syscall.Signal) error
	grace   time.Duration // wait after SIGINT, and again after SIGKILL
	poll    time.Duration
}

// readPidFile returns the pid recorded in path (0 = none).
func readPidFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return 0
	}
	return pid
}

// reapOrphan ends the Mihomo a previous helper recorded in pidFile and
// left running. The pid is only trusted when that process still runs the
// managed core binary: after a reboot or a long time the number may
// belong to something else, which is left alone. The pid file is removed
// either way.
func reapOrphan(pidFile, core string, p procs, logf Logf) {
	pid := readPidFile(pidFile)
	defer os.Remove(pidFile)
	if pid == 0 || !p.alive(pid) {
		return
	}
	exe, err := p.exePath(pid)
	if err != nil || exe != core {
		logf("pid %d of the previous run is another process now (%s); left alone", pid, exe)
		return
	}
	logf("mihomo pid=%d outlived the previous helper; stopping it", pid)
	gone := func() bool {
		deadline := time.Now().Add(p.grace)
		for p.alive(pid) {
			if !time.Now().Before(deadline) {
				return false
			}
			time.Sleep(p.poll)
		}
		return true
	}
	if err := p.signal(pid, syscall.SIGINT); err != nil {
		logf("interrupt pid %d: %v", pid, err)
	}
	if gone() {
		return
	}
	// Checked again: the grace period is long enough for the pid to end
	// and be taken by another process.
	if exe, err := p.exePath(pid); err != nil || exe != core {
		return
	}
	if err := p.signal(pid, syscall.SIGKILL); err != nil {
		logf("kill pid %d: %v", pid, err)
	}
	if !gone() {
		logf("mihomo pid=%d could not be stopped", pid)
	}
}
