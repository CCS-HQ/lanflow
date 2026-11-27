//go:build !windows

package split

import (
	"errors"
	"os/exec"
	"syscall"
)

const terminateIsKill = false

func detachService(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func detachWindow(cmd *exec.Cmd)  { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// AllowForeground is a no-op off Windows.
func AllowForeground(int) {}

// FocusDialog is a no-op off Windows.
func FocusDialog() bool { return false }

// Alive reports whether process pid still runs.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Terminate asks process pid to quit (SIGTERM: the service restores the
// system proxy and stops the core on the way out).
func Terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// Kill ends process pid at once (tests: a crash).
func Kill(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
