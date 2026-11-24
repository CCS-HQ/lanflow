//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var helperLogDir = filepath.Join(programDataDir(), "Lanflow Helper")

func programDataDir() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0); err == nil && d != "" {
		return d
	}
	if p := os.Getenv("ProgramData"); p != "" {
		return p
	}
	return `C:\ProgramData`
}

func osVersion() string {
	maj, min, build := windows.RtlGetNtVersionNumbers()
	return fmt.Sprintf("Windows %d.%d.%d", maj, min, build)
}

var procMultiByteToWideChar = windows.NewLazySystemDLL("kernel32.dll").NewProc("MultiByteToWideChar")

// oemToUTF8 converts Windows OEM-codepage command output (e.g. CP936 GBK on
// Simplified Chinese Windows) to UTF-8 when out is not already valid UTF-8.
func oemToUTF8(out []byte) string {
	if len(out) == 0 || utf8.Valid(out) {
		return string(out)
	}
	const cpOEMCP = 1
	r, _, _ := procMultiByteToWideChar.Call(cpOEMCP, 0, uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), 0, 0)
	runtime.KeepAlive(out)
	if r == 0 {
		return string(out)
	}
	wbuf := make([]uint16, r)
	r2, _, _ := procMultiByteToWideChar.Call(cpOEMCP, 0, uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&wbuf[0])), r)
	runtime.KeepAlive(out)
	runtime.KeepAlive(wbuf)
	if r2 == 0 {
		return string(out)
	}
	return windows.UTF16ToString(wbuf[:r2])
}

// systemDiagnostics runs read-only commands describing the network.
func systemDiagnostics(ctx context.Context) map[string]string {
	run := func(name string, args ...string) string {
		cmd := exec.CommandContext(ctx, sys32Exe(name), args...)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		text := oemToUTF8(out)
		if err != nil {
			return text + "\n(" + err.Error() + ")"
		}
		return text
	}
	return map[string]string{
		"routes.txt":     run("route.exe", "print", "-4") + "\n" + run("route.exe", "print", "-6"),
		"interfaces.txt": run("ipconfig.exe", "/all"),
		"proxy.txt":      run("reg.exe", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`) + "\n" + run("reg.exe", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings\Connections`),
		"winhttp.txt":    run("netsh.exe", "winhttp", "show", "proxy"),
	}
}
