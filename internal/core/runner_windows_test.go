//go:build windows

package core

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// A pipe served by a process this app did not start as a core (here the
// test itself) is refused; once the server is a tracked core it is accepted.
func TestDialChecksControllerServer(t *testing.T) {
	addr := fmt.Sprintf(`\\.\pipe\lanflow-test-%d`, time.Now().UnixNano())
	l, err := winio.ListenPipe(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c, err := dial(ctx, addr); err == nil {
		c.Close()
		t.Fatal("dial accepted a pipe served by an unknown process")
	} else if !strings.Contains(err.Error(), "不是澜流启动的 Mihomo 内核") {
		t.Fatalf("dial error: %v", err)
	}
	trackServer(os.Getpid())
	defer untrackServer(os.Getpid())
	c, err := dial(ctx, addr)
	if err != nil {
		t.Fatalf("dial to a tracked core: %v", err)
	}
	c.Close()
}

func TestTrustWorkDir(t *testing.T) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	wd := t.TempDir()
	writePID(wd, os.Getpid())
	if trustedCoreServer(os.Getpid(), tu.User.Sid, `C:\Users\alice\mihomo.exe`) {
		t.Fatal("untrusted work dir was accepted")
	}
	TrustWorkDir(wd)
	if !trustedCoreServer(os.Getpid(), tu.User.Sid, `C:\Users\alice\mihomo.exe`) {
		t.Fatal("trusted work dir with matching mihomo.pid was rejected")
	}
	if trustedCoreServer(os.Getpid(), tu.User.Sid, `C:\Users\alice\notepad.exe`) {
		t.Fatal("non-mihomo executable was accepted")
	}
	if trustedCoreServer(os.Getpid()+99999, tu.User.Sid, `C:\Users\alice\mihomo.exe`) {
		t.Fatal("mismatched PID was accepted")
	}
}

// TestUserPipeSDDL verifies that the SDDL passed to a user-mode Mihomo
// process lets go-winio create multiple pipe instances and accept repeated
// connections even when running as a standard (non-elevated) user.
func TestUserPipeSDDL(t *testing.T) {
	sid, err := currentSID()
	if err != nil || sid == "" {
		t.Fatalf("currentSID: %q, %v", sid, err)
	}
	sddl := userPipeSDDL(sid)
	var gotEnv string
	for _, kv := range extraEnv() {
		if v, ok := strings.CutPrefix(kv, "LISTEN_NAMEDPIPE_SDDL="); ok {
			gotEnv = v
		}
	}
	if gotEnv != sddl {
		t.Fatalf("extraEnv LISTEN_NAMEDPIPE_SDDL = %q, want %q", gotEnv, sddl)
	}
	if !strings.Contains(sddl, "(D;;GA;;;NU)") || !strings.Contains(sddl, "(A;;GA;;;"+sid+")") {
		t.Fatalf("unexpected userPipeSDDL: %q", sddl)
	}

	addr := fmt.Sprintf(`\\.\pipe\lanflow-test-sddl-%d`, time.Now().UnixNano())
	l, err := winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		t.Fatalf("ListenPipe with userPipeSDDL: %v", err)
	}
	defer l.Close()

	acceptErr := make(chan error, 1)
	go func() {
		for range 3 {
			c, err := l.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			_ = c.Close()
		}
		acceptErr <- nil
	}()

	trackServer(os.Getpid())
	defer untrackServer(os.Getpid())
	for i := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := dial(ctx, addr)
		cancel()
		if err != nil {
			t.Fatalf("dial #%d: %v", i+1, err)
		}
		// Wait for the server to accept and close this instance before
		// closing it: a client that disconnects before ConnectNamedPipe
		// returns makes go-winio drop the connection (ERROR_NO_DATA) and
		// wait for another, so the accept loop would never see three.
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatalf("read #%d: got data, want the server's close", i+1)
		}
		_ = c.Close()
	}
	select {
	case err := <-acceptErr:
		if err != nil {
			t.Fatalf("Accept failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not accept all three connections")
	}
}

// ownsConsole reports whether this process is attached to a console:
// AttachConsole then fails with ERROR_ACCESS_DENIED. If it succeeds the
// process had none and is detached again.
func ownsConsole() bool {
	r, _, err := procAttachConsole.Call(uintptr(^uint32(0))) // ATTACH_PARENT_PROCESS
	if r != 0 {
		procFreeConsole.Call()
		return false
	}
	return err == windows.ERROR_ACCESS_DENIED
}
