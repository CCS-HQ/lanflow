//go:build windows

package split

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"lanflow/internal/i18n"
)

// IsElevated reports whether the current process has an elevated token.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// ProcessElevated reports whether process pid runs with an elevated token.
func ProcessElevated(pid int) (bool, error) {
	if pid <= 0 {
		return false, errors.New("invalid pid")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return false, err
	}
	defer tok.Close()
	return tok.IsElevated(), nil
}

type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

const (
	seeMaskNoAsync  = 0x00000100
	seeMaskFlagNoUI = 0x00000400
)

var (
	modshell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modshell32.NewProc("ShellExecuteExW")
)

// RunElevated launches exe with args through the "runas" verb (UAC prompt)
// without waiting for the elevated process to exit.
func RunElevated(exe string, args ...string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if cerr := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); cerr == nil || errors.Is(cerr, syscall.Errno(1)) {
		defer windows.CoUninitialize()
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return err
	}
	nShow := int32(windows.SW_SHOWNORMAL)
	if slices.Contains(args, ServiceFlag) {
		nShow = windows.SW_HIDE
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        nShow,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return ErrElevationCanceled
		}
		return fmt.Errorf("%s: %w", i18n.T("无法以管理员身份启动"), e)
	}
	return nil
}
