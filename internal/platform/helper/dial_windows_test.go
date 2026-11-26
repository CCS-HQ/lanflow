//go:build windows

package helper

import (
	"context"
	"os"
	"strconv"
	"testing"
	"unsafe"

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
	// Only pipes whose owner is LocalSystem or BUILTIN\Administrators are accepted;
	// ordinary users and elevated built-in Administrator (-500) tokens whose
	// default TokenOwner is their user SID are rejected.
	if currentTokenOwnerPrivileged(t) {
		if err != nil {
			t.Fatalf("elevated pipe owner rejected: %v", err)
		}
	} else if err == nil {
		t.Fatal("non-privileged pipe owner accepted")
	}
}

func currentTokenOwnerPrivileged(t *testing.T) bool {
	t.Helper()
	tok := windows.GetCurrentProcessToken()
	var n uint32
	_ = windows.GetTokenInformation(tok, windows.TokenOwner, nil, 0, &n)
	if n == 0 {
		t.Fatal("GetTokenInformation(TokenOwner) returned length 0")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenOwner, &buf[0], n, &n); err != nil {
		t.Fatalf("GetTokenInformation(TokenOwner): %v", err)
	}
	type tokenOwner struct {
		Owner *windows.SID
	}
	owner := (*tokenOwner)(unsafe.Pointer(&buf[0])).Owner
	return owner != nil && (owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid))
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
