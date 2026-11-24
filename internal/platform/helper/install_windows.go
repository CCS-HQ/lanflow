//go:build windows

package helper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errCanceled is returned when the user dismisses the UAC prompt.
var errCanceled = errors.New("已取消授权")

func install(ctx context.Context, o InstallOptions) error {
	for _, p := range []struct{ name, path string }{{"权限组件", o.HelperBin}, {"Mihomo 内核", o.CoreBin}, {"数据目录", o.DataDir}} {
		if p.path == "" || !filepath.IsAbs(p.path) {
			return fmt.Errorf("%s路径必须是绝对路径: %q", p.name, p.path)
		}
	}
	if fi, err := os.Stat(o.HelperBin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("找不到权限组件程序: %s", o.HelperBin)
	}
	if real, err := filepath.EvalSymlinks(o.CoreBin); err == nil {
		o.CoreBin = real
	}
	if fi, err := os.Stat(o.DataDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("数据目录不可用: %s", o.DataDir)
	}
	if err := checkDowngrade(ctx, o.HelperBin); err != nil {
		return err
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	// The helper is held open against writers from before it is hashed
	// until the elevated process has ended, so what UAC runs is what was
	// checked here.
	release, err := lockVerifiedHelper(o.HelperBin)
	if err != nil {
		return err
	}
	defer release()
	coreSum, err := coreDigest(o.CoreBin, o.CoreSHA256)
	if err != nil {
		return err
	}
	if err := runElevated(ctx, o.HelperBin, []string{"install", "--core", o.CoreBin, "--core-sha256", coreSum, "--data-dir", o.DataDir, "--uid", sid}); err != nil {
		return err
	}
	return waitInstalled(ctx, 10*time.Second)
}

// lockVerifiedHelper opens the helper executable without following a
// reparse point so that nobody can write, replace, rename or delete it
// (sharing allows reading only; a directory above it cannot be renamed
// while the file is open either), hashes it through that handle and
// checks the digest recorded at build time (ExpectedHelperSHA256).
func lockVerifiedHelper(bin string) (release func(), err error) {
	p, err := windows.UTF16PtrFromString(bin)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("无法读取权限组件程序: %w", err)
	}
	f := os.NewFile(uintptr(h), bin)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		f.Close()
		return nil, fmt.Errorf("无法检查权限组件程序: %w", err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		f.Close()
		return nil, fmt.Errorf("权限组件路径不是普通文件: %s", bin)
	}
	if final, err := finalDosPath(h); err == nil && !samePathFold(final, filepath.Clean(bin)) {
		f.Close()
		return nil, fmt.Errorf("权限组件路径经过重解析点: %s", bin)
	}
	sum, err := readerSHA256(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("无法读取权限组件程序: %w", err)
	}
	if _, err := checkHelperDigest(sum); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

func finalDosPath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil || int(n) >= len(buf) {
		return "", fmt.Errorf("GetFinalPathNameByHandle: %w", err)
	}
	s := windows.UTF16ToString(buf[:n])
	switch {
	case strings.HasPrefix(s, `\\?\UNC\`):
		s = `\\` + s[len(`\\?\UNC\`):]
	case strings.HasPrefix(s, `\\?\`):
		s = s[len(`\\?\`):]
	}
	return s, nil
}

func uninstall(ctx context.Context) error {
	bin, err := helperBinary()
	if err != nil {
		return err
	}
	if !samePathFold(bin, installedHelperPath()) {
		// Not the installed copy (which only administrators can write):
		// verify and lock it like install does.
		release, err := lockVerifiedHelper(bin)
		if err != nil {
			return err
		}
		defer release()
	}
	if err := runElevated(ctx, bin, []string{"uninstall"}); err != nil {
		return err
	}
	return waitUninstalled(ctx, 5*time.Second)
}

// helperBinary prefers the bundled copy (next to the app or in resources/)
// so uninstall does not run the installed binary that it tries to remove,
// falling back to the installed copy when no bundled binary is present.
func helperBinary() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = real
		}
		dir := filepath.Dir(exe)
		for _, c := range []string{
			filepath.Join(dir, "lanflow-helper.exe"),
			filepath.Join(dir, "resources", "lanflow-helper.exe"),
		} {
			if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
				return c, nil
			}
		}
	}
	installedBin := installedHelperPath()
	if fi, err := os.Stat(installedBin); err == nil && fi.Mode().IsRegular() {
		return installedBin, nil
	}
	return "", errors.New("找不到权限组件程序，无法移除")
}

func samePathFold(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// currentUserSID returns the SID of the user running the app, which the
// elevated installer records (the UAC prompt may run as another account).
func currentUserSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("无法获取当前用户标识: %w", err)
	}
	return tu.User.Sid.String(), nil
}

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
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
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
)

var (
	modshell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modshell32.NewProc("ShellExecuteExW")
)

// runElevated runs exe with args through the "runas" verb (UAC prompt)
// and waits for it to exit; a non-zero exit code is an error.
func runElevated(ctx context.Context, exe string, args []string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return err
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        windows.SW_HIDE,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return errCanceled
		}
		return fmt.Errorf("无法以管理员身份运行权限组件: %v", e)
	}
	if info.hProcess == 0 {
		return errors.New("无法获取权限组件进程")
	}
	defer windows.CloseHandle(info.hProcess)
	for {
		ev, err := windows.WaitForSingleObject(info.hProcess, 200)
		if err != nil {
			return err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("权限组件操作失败（退出码 %d），详情见 %s", code, filepath.Join(programData(), "Lanflow Helper", "install.log"))
	}
	return nil
}

func programData() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0); err == nil && d != "" {
		return d
	}
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}
	return `C:\ProgramData`
}
