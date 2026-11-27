//go:build windows

package split

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	terminateIsKill = true
	swRestore       = 9
)

var (
	user32DLL                    = windows.NewLazySystemDLL("user32.dll")
	procAllowSetForegroundWindow = user32DLL.NewProc("AllowSetForegroundWindow")
	procFindWindowExW            = user32DLL.NewProc("FindWindowExW")
	procGetWindowThreadProcessId = user32DLL.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32DLL.NewProc("IsWindowVisible")
	procIsIconic                 = user32DLL.NewProc("IsIconic")
	procShowWindow               = user32DLL.NewProc("ShowWindow")
	procSetForegroundWindow      = user32DLL.NewProc("SetForegroundWindow")
	dialogClassPtr               = windows.StringToUTF16Ptr("#32770")
)

func detachService(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

func detachWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// AllowForeground permits pid (or any process when pid <= 0) to bring its
// window to the foreground on Windows. Best effort.
func AllowForeground(pid int) {
	target := uintptr(0xFFFFFFFF) // ASFW_ANY
	if pid > 0 {
		target = uintptr(pid)
	}
	if procAllowSetForegroundWindow.Find() == nil {
		_, _, _ = procAllowSetForegroundWindow.Call(target)
	}
}

// FocusDialog restores and focuses a visible system dialog (#32770) owned by
// the current process so repeated tray quit clicks bring an already-open
// confirmation box back to the front when no main window exists.
func FocusDialog() bool {
	if procFindWindowExW.Find() != nil || procGetWindowThreadProcessId.Find() != nil ||
		procIsWindowVisible.Find() != nil || procSetForegroundWindow.Find() != nil {
		return false
	}
	self := uint32(os.Getpid())
	classPtr := uintptr(unsafe.Pointer(dialogClassPtr))
	var prev uintptr
	for {
		hwnd, _, _ := procFindWindowExW.Call(0, prev, classPtr, 0)
		if hwnd == 0 {
			return false
		}
		prev = hwnd
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			continue
		}
		var pid uint32
		_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid != self {
			continue
		}
		if procIsIconic.Find() == nil && procShowWindow.Find() == nil {
			iconic, _, _ := procIsIconic.Call(hwnd)
			if iconic != 0 {
				_, _, _ = procShowWindow.Call(hwnd, swRestore)
			}
		}
		_, _, _ = procSetForegroundWindow.Call(hwnd)
		return true
	}
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
