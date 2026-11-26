//go:build windows

package helper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"lanflow/internal/platform/helper/protocol"
	"lanflow/internal/platform/helper/service"
)

// platformDial connects to the helper pipe and verifies who serves it.
// The service creates the pipe as its first instance (go-winio opens it
// with FILE_CREATE), so a process that grabbed the name earlier makes the
// service fail rather than share the name; checking the server process
// here makes sure the app never talks to such a squatter instead.
func platformDial(ctx context.Context) (net.Conn, error) {
	conn, err := winio.DialPipeAccess(ctx, protocol.PipeName, service.PipeClientAccess)
	if err != nil {
		return nil, err
	}
	if err := verifyPipeServer(conn); err != nil {
		_ = conn.Close()
		return nil, &net.OpError{Op: "dial", Net: "pipe", Err: err}
	}
	return conn, nil
}

// verifyPipeServer requires the pipe's server process to run as
// LocalSystem from the installed helper executable.
func verifyPipeServer(conn net.Conn) error {
	fc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("无法获取管道句柄")
	}
	h := windows.Handle(fc.Fd())
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return fmt.Errorf("无法获取权限组件进程: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return verifyHelperServicePipe(h, pid)
		}
		return fmt.Errorf("无法检查权限组件进程 %d: %w", pid, err)
	}
	defer windows.CloseHandle(proc)
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return verifyHelperServicePipe(h, pid)
		}
		return fmt.Errorf("无法检查权限组件进程 %d: %w", pid, err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	if !tu.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return fmt.Errorf("管道 %s 的服务进程 %d 不是以 SYSTEM 运行的权限组件", protocol.PipeName, pid)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(proc, 0, &buf[0], &n); err != nil {
		return err
	}
	if image := windows.UTF16ToString(buf[:n]); !strings.EqualFold(filepath.Clean(image), installedHelperPath()) {
		return fmt.Errorf("管道 %s 的服务进程不是已安装的权限组件: %s", protocol.PipeName, image)
	}
	return nil
}

// verifyHelperServicePipe verifies the helper pipe when a non-elevated caller
// gets ERROR_ACCESS_DENIED from OpenProcess/OpenProcessToken on the LocalSystem
// service: it checks that the pipe kernel object is owned by SYSTEM or
// Administrators, and that Windows SCM reports the installed LanflowHelper
// service running as LocalSystem with matching PID and binary path.
func verifyHelperServicePipe(h windows.Handle, pid uint32) error {
	if err := verifyPipeOwnerPrivileged(h); err != nil {
		return err
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("无法连接服务管理器以校验权限组件: %w", err)
	}
	defer windows.CloseServiceHandle(scm)
	svcName, err := windows.UTF16PtrFromString(service.ServiceName)
	if err != nil {
		return err
	}
	svc, err := windows.OpenService(scm, svcName, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return fmt.Errorf("无法查询权限组件服务 %s: %w", service.ServiceName, err)
	}
	defer windows.CloseServiceHandle(svc)

	var st windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if err := windows.QueryServiceStatusEx(svc, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &needed); err != nil {
		return fmt.Errorf("无法读取权限组件服务状态: %w", err)
	}
	if st.CurrentState != windows.SERVICE_RUNNING || st.ProcessId != pid {
		return fmt.Errorf("管道 %s 的服务进程 %d 不是正在运行的权限组件服务（当前状态 %d，PID %d）", protocol.PipeName, pid, st.CurrentState, st.ProcessId)
	}

	needed = 4096
	for {
		buf := make([]byte, needed)
		cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
		err := windows.QueryServiceConfig(svc, cfg, uint32(len(buf)), &needed)
		if err == nil {
			startName := windows.UTF16PtrToString(cfg.ServiceStartName)
			if !strings.EqualFold(startName, "LocalSystem") && !strings.EqualFold(startName, `NT AUTHORITY\SYSTEM`) {
				return fmt.Errorf("权限组件服务运行账户不是 LocalSystem: %s", startName)
			}
			exe := serviceExeFromCommandLine(windows.UTF16PtrToString(cfg.BinaryPathName))
			if !strings.EqualFold(filepath.Clean(exe), installedHelperPath()) {
				return fmt.Errorf("管道 %s 的服务进程不是已安装的权限组件: %s", protocol.PipeName, exe)
			}
			return nil
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || needed <= uint32(len(buf)) {
			return fmt.Errorf("无法读取权限组件服务配置: %w", err)
		}
	}
}

func verifyPipeOwnerPrivileged(h windows.Handle) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("无法读取权限组件管道属主: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("无法读取权限组件管道属主")
	}
	if !owner.IsWellKnown(windows.WinLocalSystemSid) && !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return fmt.Errorf("管道 %s 属主 %v 不是 SYSTEM 或 Administrators", protocol.PipeName, owner)
	}
	return nil
}

func serviceExeFromCommandLine(cmdLine string) string {
	s := strings.TrimSpace(cmdLine)
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if end := strings.IndexByte(s[1:], '"'); end >= 0 {
			return s[1 : 1+end]
		}
		return strings.Trim(s, `"`)
	}
	lower := strings.ToLower(s)
	if idx := strings.Index(lower, ".exe"); idx >= 0 {
		end := idx + len(".exe")
		if end == len(s) || s[end] == ' ' || s[end] == '\t' {
			return s[:end]
		}
	}
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[0]
	}
	return s
}

// installedHelperPath is where the service executable is installed
// (service.HelperBin).
func installedHelperPath() string {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil || pf == "" {
		pf = os.Getenv("ProgramFiles")
	}
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "Lanflow Helper", "lanflow-helper.exe")
}
