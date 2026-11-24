//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func samePath(a, b string) bool { return strings.EqualFold(a, b) }
func hasPathPrefix(p, prefix string) bool {
	return len(p) >= len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
}

// ownerMatches reports whether the directory path belongs to the user SID
// uid. It is used for the data directory at install time (the controller is
// a named pipe on Windows). The directory is opened without following a
// reparse point and must not be a drive root or lie in a system location
// (Windows, Program Files, ProgramData, the Users root): those are never a
// per-user data directory, and a SYSTEM service working there could be
// turned against the machine.
//
// On Windows, a process running elevated (for example "Run as
// administrator", UAC disabled, or the built-in Administrator account) has
// TokenOwner = BUILTIN\Administrators (S-1-5-32-544), so directories it
// creates — including %APPDATA%\Lanflow — are owned by BUILTIN\Administrators
// rather than the user's own SID, while inheriting a Full Control ACE for
// the user from their profile directory. When the owner is
// BUILTIN\Administrators, we accept the directory only if the DACL
// specifically grants uid Full Control (and does not deny it), so another
// user cannot claim an admin-owned or shared directory.
func ownerMatches(path string, uid string) (bool, error) {
	want, err := windows.StringToSid(uid)
	if err != nil {
		return false, fmt.Errorf("用户标识无效: %q", uid)
	}
	if isWellKnownNonUserSID(want) {
		return false, nil
	}
	if err := checkNotSystemDir(path); err != nil {
		return false, err
	}
	h, err := openNoReparse(path, true)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("无法读取目录属主: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false, errors.New("无法读取目录属主")
	}
	if owner.Equals(want) {
		return true, nil
	}
	if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return false, nil
	}
	return daclGrantsFullControl(sd, want), nil
}

// isWellKnownNonUserSID reports whether sid is a built-in system, service,
// or broad group SID rather than a per-user account SID.
func isWellKnownNonUserSID(sid *windows.SID) bool {
	if sid == nil {
		return true
	}
	for _, wk := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinLocalSystemSid,
		windows.WinBuiltinAdministratorsSid,
		windows.WinBuiltinUsersSid,
		windows.WinWorldSid,
		windows.WinAuthenticatedUserSid,
		windows.WinLocalServiceSid,
		windows.WinNetworkServiceSid,
		windows.WinCreatorOwnerSid,
		windows.WinInteractiveSid,
	} {
		if sid.IsWellKnown(wk) {
			return true
		}
	}
	return false
}

// isBroadPrincipal reports whether sid is a broad group that includes
// ordinary interactive users, used when evaluating ACCESS_DENIED ACEs.
func isBroadPrincipal(sid *windows.SID) bool {
	return sid.IsWellKnown(windows.WinWorldSid) ||
		sid.IsWellKnown(windows.WinAuthenticatedUserSid) ||
		sid.IsWellKnown(windows.WinBuiltinUsersSid)
}

// Win32 file/directory access masks (winnt.h).
const (
	fileAllAccess    windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF
	fileGenericRead  windows.ACCESS_MASK = windows.STANDARD_RIGHTS_READ | windows.FILE_READ_DATA | windows.FILE_READ_ATTRIBUTES | windows.FILE_READ_EA | windows.SYNCHRONIZE
	fileGenericWrite windows.ACCESS_MASK = windows.STANDARD_RIGHTS_WRITE | windows.FILE_WRITE_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA | windows.FILE_APPEND_DATA | windows.SYNCHRONIZE
	fileGenericExec  windows.ACCESS_MASK = windows.STANDARD_RIGHTS_EXECUTE | windows.FILE_READ_ATTRIBUTES | windows.FILE_EXECUTE | windows.SYNCHRONIZE
)

func expandFileMask(m windows.ACCESS_MASK) windows.ACCESS_MASK {
	if m&windows.GENERIC_ALL != 0 {
		m |= fileAllAccess
	}
	if m&windows.GENERIC_READ != 0 {
		m |= fileGenericRead
	}
	if m&windows.GENERIC_WRITE != 0 {
		m |= fileGenericWrite
	}
	if m&windows.GENERIC_EXECUTE != 0 {
		m |= fileGenericExec
	}
	return m
}

// daclGrantsFullControl reports whether sd's DACL explicitly grants want
// FILE_ALL_ACCESS on the directory itself (evaluating ACEs in order so an
// earlier deny ACE blocks later allow ACEs). A nil (unrestricted) DACL or
// broad-group-only grants (Everyone, Users, Authenticated Users) do not
// satisfy this check.
func daclGrantsFullControl(sd *windows.SECURITY_DESCRIPTOR, want *windows.SID) bool {
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	var granted, denied windows.ACCESS_MASK
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, windows.ACCESS_DENIED_ACE_TYPE:
		default:
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() {
			continue
		}
		mask := expandFileMask(ace.Mask)
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			if sid.Equals(want) || isBroadPrincipal(sid) {
				denied |= mask &^ granted
			}
			continue
		}
		if sid.Equals(want) {
			granted |= mask &^ denied
		}
	}
	return granted&fileAllAccess == fileAllAccess
}

// openNoReparse opens path for reading its attributes and security
// without following a final reparse point (symlink, junction, mount
// point) and fails when path is one. dir selects backup semantics, needed
// to open a directory.
func openNoReparse(path string, dir bool) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if dir {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return 0, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("路径是重解析点（符号链接或目录联接）: %s", path)
	}
	if isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0; isDir != dir {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("路径类型不符: %s", path)
	}
	return h, nil
}

// verifyNoLinks opens every directory from dataDir down to path (inside
// it, symlinks already resolved) and path itself without following a
// reparse point, and fails on the first one that is a symbolic link,
// junction or mount point.
func verifyNoLinks(dataDir, path string, dir bool) error {
	rel, err := filepath.Rel(dataDir, path)
	if err != nil {
		return err
	}
	cur := dataDir
	parts := strings.Split(rel, string(filepath.Separator))
	if rel == "." {
		parts = nil
	}
	check := func(p string, isDir bool) error {
		h, err := openNoReparse(p, isDir)
		if err != nil {
			return err
		}
		return windows.CloseHandle(h)
	}
	if err := check(cur, true); err != nil {
		return err
	}
	for i, name := range parts {
		cur = filepath.Join(cur, name)
		if err := check(cur, dir || i < len(parts)-1); err != nil {
			return err
		}
	}
	return nil
}

// systemDirs are locations a per-user data directory must never be in.
func systemDirs() []string {
	var dirs []string
	for _, id := range []*windows.KNOWNFOLDERID{
		windows.FOLDERID_Windows,
		windows.FOLDERID_ProgramFiles,
		windows.FOLDERID_ProgramFilesX86,
		windows.FOLDERID_ProgramData,
	} {
		if d, err := windows.KnownFolderPath(id, 0); err == nil && d != "" {
			dirs = append(dirs, d)
		}
	}
	for _, env := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		if d := os.Getenv(env); d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// checkNotSystemDir refuses a drive root, the Users root itself and
// anything inside a system location.
func checkNotSystemDir(path string) error {
	p := filepath.Clean(path)
	vol := filepath.VolumeName(p)
	if vol == "" || p == vol || p == vol+`\` {
		return fmt.Errorf("数据目录不能是盘符根目录: %s", path)
	}
	if samePath(p, filepath.Join(vol+`\`, "Users")) {
		return fmt.Errorf("数据目录不能是用户根目录: %s", path)
	}
	if d, err := windows.KnownFolderPath(windows.FOLDERID_UserProfiles, 0); err == nil && samePath(p, filepath.Clean(d)) {
		return fmt.Errorf("数据目录不能是用户根目录: %s", path)
	}
	for _, d := range systemDirs() {
		if isWithin(d, p) {
			return fmt.Errorf("数据目录不能位于系统目录 %s 内: %s", d, path)
		}
	}
	return nil
}

// checkController validates the controller pipe name. Mihomo creates the
// pipe itself; the helper restricts it with LISTEN_NAMEDPIPE_SDDL.
func checkController(_ *Policy, controller string) (string, error) {
	if controller == "" {
		return "", errors.New("控制器地址未指定")
	}
	const prefix = `\\.\pipe\lanflow-`
	if !strings.HasPrefix(controller, prefix) || len(controller) == len(prefix) {
		return "", fmt.Errorf(`控制器管道名必须以 %s 开头: %s`, prefix, controller)
	}
	if strings.ContainsAny(controller[len(prefix):], `\/`) {
		return "", fmt.Errorf("控制器管道名无效: %s", controller)
	}
	if pipeExists(controller) {
		// Another process already serves this name: Mihomo could not
		// create it as first instance, and the app would talk to that
		// process instead.
		return "", fmt.Errorf("控制器管道已被其他进程占用: %s", controller)
	}
	return controller, nil
}

var procWaitNamedPipeW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")

// pipeExists reports whether a pipe named name exists. WaitNamedPipe
// fails with ERROR_FILE_NOT_FOUND when no instance exists and does not
// connect to the pipe.
func pipeExists(name string) bool {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	r, _, e := procWaitNamedPipeW.Call(uintptr(unsafe.Pointer(p)), 1)
	return r != 0 || !errors.Is(e, windows.ERROR_FILE_NOT_FOUND)
}
