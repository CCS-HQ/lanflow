//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

var procAttachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

// attachParentConsole attaches a GUI-subsystem (`-H windowsgui`) process
// to its parent cmd.exe/PowerShell console for `lanflow ctl`, opening
// CONIN$/CONOUT$ only for standard handles that are not already connected
// to a pipe or file redirection.
func attachParentConsole() {
	inUnknown := stdHandleUnknown(windows.STD_INPUT_HANDLE)
	outUnknown := stdHandleUnknown(windows.STD_OUTPUT_HANDLE)
	errUnknown := stdHandleUnknown(windows.STD_ERROR_HANDLE)
	if !inUnknown && !outUnknown && !errUnknown {
		return
	}
	if procAttachConsole.Find() != nil {
		return
	}
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	r, _, _ := procAttachConsole.Call(attachParentProcess)
	if r == 0 {
		return
	}
	if inUnknown {
		if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
			os.Stdin = f
			_ = windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(f.Fd()))
		}
	}
	var outFile *os.File
	if outUnknown {
		if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
			outFile = f
			os.Stdout = f
			_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(f.Fd()))
		}
	}
	if errUnknown {
		if outFile == nil {
			outFile, _ = os.OpenFile("CONOUT$", os.O_RDWR, 0)
		}
		if outFile != nil {
			os.Stderr = outFile
			_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(outFile.Fd()))
		}
	}
}

func stdHandleUnknown(stdHandle uint32) bool {
	h, err := windows.GetStdHandle(stdHandle)
	if err != nil || h == 0 || h == windows.InvalidHandle {
		return true
	}
	ft, err := windows.GetFileType(h)
	return err != nil || ft == windows.FILE_TYPE_UNKNOWN
}
