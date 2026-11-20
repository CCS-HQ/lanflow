//go:build windows

package split

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
}

// Alive reports whether process pid still runs.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// Terminate ends process pid. Windows has no polite signal for a process
// without windows; the service's next start finds the system proxy left on
// and offers to restore it (app recovery notice).
func Terminate(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// Kill ends process pid at once (tests: a crash).
func Kill(pid int) error { return Terminate(pid) }

// pinProcess holds a handle to process pid: while it is open the system
// does not give the pid to another process, so the pid-based calls of
// StopService keep meaning this process. same reports whether it still
// runs.
func pinProcess(pid int) (same func() bool, release func()) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// Cannot be opened (gone, or not ours): nothing is pinned, and
		// Terminate will fail the same way.
		return func() bool { return true }, func() {}
	}
	return func() bool {
		ev, err := windows.WaitForSingleObject(h, 0)
		return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
	}, func() { windows.CloseHandle(h) }
}
