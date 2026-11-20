//go:build windows

package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// openLogFile rotates path at LogRotateSize (keeping path.1) and opens it
// for appending. path lies in the user's data directory and is opened by
// SYSTEM, so see openFileNoReparse for the checks.
func openLogFile(path string, _ string) (io.WriteCloser, error) {
	rotateLog(path)
	f, _, err := openFileNoReparse(path)
	return f, err
}

// rotateLog renames a log of LogRotateSize or more to <name>.1.
//
// Nothing here goes by path after the open: the file is opened without
// following a reparse point and checked (regular, one link, final path is
// path itself), and is then renamed through that handle to a bare name,
// which NTFS resolves in the directory the open file is in. A junction
// swapped into the path after validation therefore makes the open fail
// the final-path check; it cannot make SYSTEM rename a file elsewhere.
func rotateLog(path string) {
	h, err := openExistingNoReparse(path, windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return
	}
	if size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow); size < LogRotateSize {
		return
	}
	_ = renameByHandle(h, filepath.Base(path)+".1")
}

// fileRenameInformation is FILE_RENAME_INFORMATION.
type fileRenameInformation struct {
	ReplaceIfExists uint32 // BOOLEAN, padded
	RootDirectory   windows.Handle
	FileNameLength  uint32 // bytes
	FileName        [1]uint16
}

// renameByHandle renames the open file to name within its own directory
// (name has no path separators), replacing an existing entry. With a nil
// RootDirectory and a bare name NtSetInformationFile does not resolve any
// path. The handle needs DELETE access.
func renameByHandle(h windows.Handle, name string) error {
	if strings.ContainsAny(name, `\/:`) {
		return fmt.Errorf("invalid file name %q", name)
	}
	u, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	u = u[:len(u)-1] // no terminating NUL
	off := unsafe.Offsetof(fileRenameInformation{}.FileName)
	buf := make([]byte, int(off)+len(u)*2+2)
	fi := (*fileRenameInformation)(unsafe.Pointer(&buf[0]))
	fi.ReplaceIfExists = 1
	fi.FileNameLength = uint32(len(u) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[off])), len(u)), u)
	var iosb windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(h, &iosb, &buf[0], uint32(len(buf)), windows.FileRenameInformation)
}

// checkOpened verifies an open handle: a regular file with a single link
// whose final path (GetFinalPathNameByHandle) is path itself, so a
// junction swapped into an intermediate directory after validation is
// detected.
func checkOpened(h windows.Handle, path string) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	switch {
	case info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		return errors.New("日志文件是重解析点（符号链接或目录联接）")
	case info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0:
		return errors.New("日志文件是目录")
	case info.NumberOfLinks != 1:
		return fmt.Errorf("日志文件存在 %d 个硬链接", info.NumberOfLinks)
	}
	final, err := finalPath(h)
	if err != nil {
		return err
	}
	if !samePath(final, path) {
		return fmt.Errorf("日志文件实际位置 %s 与预期不符", final)
	}
	return nil
}

// openExistingNoReparse opens the existing file path with access, without
// following a reparse point, and verifies it with checkOpened.
func openExistingNoReparse(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := checkOpened(h, path); err != nil {
		windows.CloseHandle(h)
		return 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return h, nil
}

// deleteByHandle marks the open file for deletion (it goes when the handle
// closes). The handle needs DELETE access.
func deleteByHandle(h windows.Handle) error {
	del := uint32(1) // FILE_DISPOSITION_INFO{DeleteFile: TRUE}
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&del)), 1)
}

// openFileNoReparse opens (creating if needed) path for appending without
// following a reparse point. The opened file must pass checkOpened. A file
// this call created that fails the check (its directory was redirected
// between validation and the open) is deleted again through its handle, so
// no empty file is left where the redirection pointed. The returned SID is
// the file's owner.
func openFileNoReparse(path string) (*os.File, *windows.SID, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, err
	}
	// DELETE is asked for only on a file this call creates (to take it back
	// when the check fails): Windows grants it on an existing file only
	// when every other open handle shares delete, and a log viewer following
	// the file (Get-Content -Wait, most tail tools) does not, which would
	// keep the core from starting.
	const access = windows.FILE_APPEND_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	const flags = windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT
	created := false
	var h windows.Handle
	for attempt := 0; ; attempt++ {
		h, err = windows.CreateFile(p, access, share, nil, windows.OPEN_EXISTING, flags, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		h, err = windows.CreateFile(p, access|windows.DELETE, share, nil, windows.CREATE_NEW, flags, 0)
		if err == nil {
			created = true
			break
		}
		// Created by someone else in between: open that one, once.
		if !errors.Is(err, windows.ERROR_FILE_EXISTS) || attempt > 0 {
			return nil, nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
	}
	fail := func(err error) (*os.File, *windows.SID, error) {
		if created {
			_ = deleteByHandle(h)
		}
		windows.CloseHandle(h)
		return nil, nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := checkOpened(h, path); err != nil {
		return fail(err)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fail(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fail(err)
	}
	return os.NewFile(uintptr(h), path), owner, nil
}

// GetFinalPathNameByHandle flags (both zero: normalized name, DOS volume).
const (
	fileNameNormalized = 0x0
	volumeNameDOS      = 0x0
)

// finalPath returns the normalized DOS path of an open handle, without
// the \\?\ prefix.
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), fileNameNormalized|volumeNameDOS)
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			s := windows.UTF16ToString(buf[:n])
			switch {
			case strings.HasPrefix(s, `\\?\UNC\`):
				s = `\\` + s[len(`\\?\UNC\`):]
			case strings.HasPrefix(s, `\\?\`):
				s = s[len(`\\?\`):]
			}
			return s, nil
		}
		buf = make([]uint16, n)
	}
}

// adminDirSDDL grants SYSTEM and Administrators full control, inherited
// by everything below, and nobody else any access.
const adminDirSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// isAdminOwner reports whether sid is SYSTEM or the Administrators group.
func isAdminOwner(sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	return sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

// PrepareLogDir creates LogDir (%ProgramData%\Lanflow Helper) with a DACL
// for SYSTEM and Administrators only. %ProgramData% lets ordinary users
// create subdirectories, so an existing LogDir not owned by SYSTEM or
// Administrators (or one that is a reparse point) is refused: a user could
// have planted it, with links that redirect the logs SYSTEM writes. An
// existing admin-owned directory gets the DACL re-applied (older versions
// created it with the inherited, user-writable one).
func PrepareLogDir() error { return secureAdminDir(LogDir) }

func secureAdminDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(adminDirSDDL)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	err = windows.CreateDirectory(p, sa)
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("创建 %s 失败: %w", dir, err)
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("打开 %s 失败: %w", dir, err)
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%s 不是普通目录，请删除后重试", dir)
	}
	cur, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := cur.Owner()
	if err != nil {
		return err
	}
	if !isAdminOwner(owner) {
		return fmt.Errorf("%s 的属主 %v 不是 SYSTEM 或 Administrators，可能被其他用户预先创建，请删除后重试", dir, owner)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("设置 %s 权限失败: %w", dir, err)
	}
	return nil
}

// OpenAdminLog opens a log file inside LogDir for appending. The file must
// not be a reparse point or hard link; one not owned by SYSTEM or
// Administrators (planted while the directory was still user-writable) is
// deleted and created afresh.
func OpenAdminLog(path string) (*os.File, error) {
	for attempt := 0; ; attempt++ {
		f, owner, err := openFileNoReparse(path)
		if err != nil {
			return nil, err
		}
		if isAdminOwner(owner) {
			return f, nil
		}
		_ = f.Close()
		if attempt > 0 {
			return nil, fmt.Errorf("%s 的属主不是 SYSTEM 或 Administrators", path)
		}
		// Not a reparse point and singly linked: deleting the name
		// removes only this file.
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
}
