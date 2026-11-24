package ctl

import (
	"os"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a Windows console or a MSYS2/Git Bash/Cygwin PTY pipe.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		return true
	}
	ft, err := windows.GetFileType(h)
	if err != nil || ft != windows.FILE_TYPE_PIPE {
		return false
	}
	// Query FILE_NAME_INFO for MSYS2 / Git Bash / Cygwin mintty PTY pipes:
	// e.g. \msys-1888ae32e00d56aa-pty0-to-master
	var buf [1024]byte
	if err := windows.GetFileInformationByHandleEx(h, windows.FileNameInfo, &buf[0], uint32(len(buf))); err != nil {
		return false
	}
	byteLen := *(*uint32)(unsafe.Pointer(&buf[0]))
	const headerSize = 4
	if byteLen == 0 || int(headerSize+byteLen) > len(buf) {
		return false
	}
	u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[headerSize])), int(byteLen/2))
	name := string(utf16.Decode(u16))
	return isMSYSPtyName(name)
}

func isMSYSPtyName(name string) bool {
	lower := strings.ToLower(name)
	return (strings.Contains(lower, "msys-") || strings.Contains(lower, "cygwin-")) && strings.Contains(lower, "-pty")
}
