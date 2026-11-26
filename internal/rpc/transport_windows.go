//go:build windows

package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Endpoint is the named pipe of the current user,
// \\.\pipe\lanflow-<user SID>, or $LANFLOW_SOCKET when set. dataDir is
// unused on Windows.
func Endpoint(string) string {
	if v := os.Getenv("LANFLOW_SOCKET"); v != "" {
		return v
	}
	sid, err := currentSID()
	if err != nil {
		return fmt.Sprintf(`\\.\pipe\lanflow-pid-%d`, os.Getpid())
	}
	return `\\.\pipe\lanflow-` + sid
}

// Listen creates the pipe with a DACL for the current user only. go-winio
// opens the first instance with FILE_FLAG_FIRST_PIPE_INSTANCE, so a name
// another process holds makes Listen fail instead of sharing it. Accepted
// connections whose client process runs as another user are closed.
func Listen(dataDir string) (net.Listener, error) {
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	l, err := winio.ListenPipe(Endpoint(dataDir), &winio.PipeConfig{
		SecurityDescriptor: "D:P(D;;GA;;;NU)(A;;GA;;;" + sid + ")",
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, ErrInUse
		}
		return nil, err
	}
	return &peerListener{Listener: l}, nil
}

func dialEndpoint(ctx context.Context, dataDir string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, Endpoint(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, ErrNotRunning
		}
		return nil, err
	}
	return c, nil
}

func currentSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}

var (
	modadvapi32                    = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateNamedPipeClient = modadvapi32.NewProc("ImpersonateNamedPipeClient")
)

// verifyClient checks the client process on a server-side pipe handle.
// If OpenProcess fails with ERROR_ACCESS_DENIED (for example, a Medium-IL
// service accepting a High-IL client of the same user), it falls back to
// ImpersonateNamedPipeClient to read the client's token user SID.
func verifyClient(conn net.Conn) error {
	fc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("无法获取管道句柄")
	}
	h := windows.Handle(fc.Fd())
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(h, &pid); err != nil {
		return fmt.Errorf("无法获取对端进程: %w", err)
	}
	want, err := currentSID()
	if err != nil {
		return err
	}
	got, err := processSID(pid)
	if err != nil {
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("无法检查对端进程 %d: %w", pid, err)
		}
		got, err = impersonatePipeClientSID(h)
		if err != nil {
			return fmt.Errorf("无法检查对端进程 %d: %w", pid, err)
		}
	}
	if got != want {
		return fmt.Errorf("对端进程 %d 属于 %s，不是当前用户", pid, got)
	}
	return nil
}

func impersonatePipeClientSID(h windows.Handle) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, _, err := procImpersonateNamedPipeClient.Call(uintptr(h))
	if r == 0 {
		return "", fmt.Errorf("ImpersonateNamedPipeClient: %w", err)
	}
	defer func() { _ = windows.RevertToSelf() }()
	var tok windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok); err != nil {
		return "", fmt.Errorf("OpenThreadToken: %w", err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}

// verifyServer checks the server process on a client-side pipe handle.
func verifyServer(conn net.Conn) error {
	return verifyPipePID(conn, windows.GetNamedPipeServerProcessId)
}

func verifyPipePID(conn net.Conn, getPID func(windows.Handle, *uint32) error) error {
	fc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("无法获取管道句柄")
	}
	var pid uint32
	if err := getPID(windows.Handle(fc.Fd()), &pid); err != nil {
		return fmt.Errorf("无法获取对端进程: %w", err)
	}
	want, err := currentSID()
	if err != nil {
		return err
	}
	got, err := processSID(pid)
	if err != nil {
		return fmt.Errorf("无法检查对端进程 %d: %w", pid, err)
	}
	if got != want {
		return fmt.Errorf("对端进程 %d 属于 %s，不是当前用户", pid, got)
	}
	return nil
}

// peerProc returns the client process of a pipe connection the server
// accepted, and its executable.
func peerProc(conn net.Conn) (pid int, exe string, err error) {
	fc, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, "", errors.New("无法获取管道句柄")
	}
	var id uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(fc.Fd()), &id); err != nil {
		return 0, "", err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, id)
	if err != nil {
		return int(id), "", err
	}
	defer windows.CloseHandle(proc)
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(proc, 0, &buf[0], &n); err != nil {
		return int(id), "", err
	}
	return int(id), windows.UTF16ToString(buf[:n]), nil
}

func processSID(pid uint32) (string, error) {
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(proc)
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}
