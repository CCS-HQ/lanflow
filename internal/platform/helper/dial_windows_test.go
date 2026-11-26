//go:build windows

package helper

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// A pipe served by an ordinary process (this test) must not be accepted
// as the helper.
func TestVerifyPipeServerRefusesNonSystem(t *testing.T) {
	name := `\\.\pipe\lanflow-verify-test-` + strconv.Itoa(os.Getpid())
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			defer c.Close()
			var b [1]byte
			_, _ = c.Read(b[:])
		}
	}()
	conn, err := winio.DialPipeContext(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := verifyPipeServer(conn); err == nil {
		t.Fatal("non-SYSTEM pipe server accepted")
	}
}

func TestVerifyPipeOwnerPrivilegedRejectsCurrentUser(t *testing.T) {
	name := `\\.\pipe\lanflow-owner-test-` + strconv.Itoa(os.Getpid())
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			defer c.Close()
			var b [1]byte
			_, _ = c.Read(b[:])
		}
	}()
	conn, err := winio.DialPipeContext(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fc := conn.(interface{ Fd() uintptr })
	err = verifyPipeOwnerPrivileged(windows.Handle(fc.Fd()))
	// When the test itself runs non-elevated, the pipe owner is the user's SID,
	// so verifyPipeOwnerPrivileged must reject it. If the test runs elevated,
	// owner is Builtin\Administrators and it is accepted.
	if windows.GetCurrentProcessToken().IsElevated() {
		if err != nil {
			t.Fatalf("elevated pipe owner rejected: %v", err)
		}
	} else if err == nil {
		t.Fatal("non-privileged pipe owner accepted")
	}
}

func TestServiceExeFromCommandLine(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{`"C:\Program Files\Lanflow Helper\lanflow-helper.exe" run`, `C:\Program Files\Lanflow Helper\lanflow-helper.exe`},
		{`C:\Program Files\Lanflow Helper\lanflow-helper.exe run`, `C:\Program Files\Lanflow Helper\lanflow-helper.exe`},
		{`C:\helper\lanflow-helper.exe`, `C:\helper\lanflow-helper.exe`},
		{``, ``},
	} {
		if got := serviceExeFromCommandLine(tc.in); got != tc.want {
			t.Errorf("serviceExeFromCommandLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
