//go:build windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func pathKey(s string) string { return strings.ToLower(s) }

// prepareWorkDir creates dir (below LogDir in %ProgramData%) with a protected
// DACL for SYSTEM and Administrators only, or re-applies it recursively.
func prepareWorkDir(dir string) error {
	if err := PrepareLogDir(); err != nil {
		return err
	}
	return secureAdminDirWithSDDL(dir, workDirSDDL, true)
}

// openUserFile opens a file of the installing user's for reading. path
// (links resolved at validation) lies in the user's data directory and is
// opened by SYSTEM: a reparse point is opened itself, not followed, and
// refused; the file must have a single link, its final path must be path
// itself (checkOpened), and its owner must match uid (or Administrators
// with user full-control).
func openUserFile(path string, uid string) (*os.File, error) {
	h, err := openExistingNoReparse(path, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return nil, err
	}
	if uid != "" {
		want, err := windows.StringToSid(uid)
		if err != nil {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("用户标识无效: %q", uid)
		}
		sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("无法读取文件属主: %w", err)
		}
		owner, _, err := sd.Owner()
		if err != nil || owner == nil {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("无法读取文件属主")
		}
		ok := owner.Equals(want)
		if !ok && owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !isWellKnownNonUserSID(want) && daclGrantsFullControl(sd, want) {
			ok = true
		}
		if !ok {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("文件属主 %v 不属于用户 %s", owner, uid)
		}
	}
	return os.NewFile(uintptr(h), path), nil
}

// listUserDir names the entries of a directory of the user's, at most
// maxSeedEntries of them; none when it cannot be read or is a reparse
// point.
func listUserDir(dir string) []string {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(h)
		return nil
	}
	final, err := finalPath(h)
	if err != nil || !samePath(final, filepath.Clean(dir)) {
		windows.CloseHandle(h)
		return nil
	}
	f := os.NewFile(uintptr(h), dir)
	defer f.Close()
	names, _ := f.Readdirnames(maxSeedEntries)
	return names
}
