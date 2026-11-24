//go:build windows

package service

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestOwnerMatchesWindows(t *testing.T) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := tu.User.Sid.String()
	d := t.TempDir()
	ok, err := ownerMatches(d, me)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("temp dir %s not accepted for current user %s", d, me)
	}
	for _, nonUser := range []string{"S-1-5-18", "S-1-5-32-544", "S-1-1-0", "S-1-5-11", "S-1-5-32-545"} {
		if ok, err := ownerMatches(d, nonUser); err != nil || ok {
			t.Fatalf("well-known non-user SID %s reported as owner of %s (ok=%v, err=%v)", nonUser, d, ok, err)
		}
	}
	// Another arbitrary user SID must not match.
	otherSID := "S-1-5-21-1000-2000-3000-9999"
	if otherSID == me {
		otherSID = "S-1-5-21-1000-2000-3000-9998"
	}
	if ok, err := ownerMatches(d, otherSID); err != nil || ok {
		t.Fatalf("unrelated user %s reported as owner (ok=%v, err=%v)", otherSID, ok, err)
	}
	// A junction / symlink is refused rather than followed.
	link := filepath.Join(d, "link")
	if err := os.Symlink(d, link); err == nil {
		if ok, err := ownerMatches(link, me); ok || err == nil {
			t.Fatal("followed a reparse point")
		}
	}
}

func TestDACLGrantsFullControl(t *testing.T) {
	const (
		userA = "S-1-5-21-1111111111-2222222222-3333333333-1001"
		userB = "S-1-5-21-1111111111-2222222222-3333333333-1002"
	)
	want, err := windows.StringToSid(userA)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		sddl string
		want bool
	}{
		{
			name: "user has FILE_ALL_ACCESS",
			sddl: "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + userA + ")",
			want: true,
		},
		{
			name: "user has GENERIC_ALL",
			sddl: "D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)(A;OICI;GA;;;" + userA + ")",
			want: true,
		},
		{
			name: "admin-only directory",
			sddl: "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
			want: false,
		},
		{
			name: "another user has full control",
			sddl: "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + userB + ")",
			want: false,
		},
		{
			name: "broad groups only (Everyone/Users/Authenticated Users)",
			sddl: "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;WD)(A;OICI;FA;;;BU)(A;OICI;FA;;;AU)",
			want: false,
		},
		{
			name: "read and execute only",
			sddl: "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;" + userA + ")",
			want: false,
		},
		{
			name: "modify only (missing WRITE_DAC and WRITE_OWNER)",
			sddl: "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x001301bf;;;" + userA + ")",
			want: false,
		},
		{
			name: "inherit-only ACE does not apply to directory itself",
			sddl: "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICIIO;FA;;;" + userA + ")",
			want: false,
		},
		{
			name: "explicit deny before allow blocks user",
			sddl: "D:P(D;;FW;;;" + userA + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + userA + ")",
			want: false,
		},
		{
			name: "deny Everyone before allow blocks user",
			sddl: "D:P(D;;FW;;;WD)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + userA + ")",
			want: false,
		},
		{
			name: "null DACL refused",
			sddl: "D:NO_ACCESS_CONTROL",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatalf("SecurityDescriptorFromString(%q): %v", tc.sddl, err)
			}
			if got := daclGrantsFullControl(sd, want); got != tc.want {
				t.Errorf("daclGrantsFullControl(%q) = %v, want %v", tc.sddl, got, tc.want)
			}
		})
	}
}

func TestOwnerMatchesAdminOwnedDirWindows(t *testing.T) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := tu.User.Sid.String()
	root := t.TempDir()

	// Verify whether this test run creates directories owned by
	// BUILTIN\Administrators (elevated admin token). On a non-elevated run
	// the temp dir is already owned by me directly, so the admin-owned DACL
	// path is covered by TestDACLGrantsFullControl instead.
	h, err := openNoReparse(root, true)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		t.Skipf("directory owner is %v, not BUILTIN\\Administrators (non-elevated run)", owner)
	}

	applySDDL := func(t *testing.T, dir, sddl string) {
		t.Helper()
		sec, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err := sec.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			nil, nil, dacl, nil); err != nil {
			t.Fatal(err)
		}
	}

	restoreSDDL := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + me + ")"
	cases := []struct {
		name string
		sddl string
		want bool
	}{
		{"user full control", restoreSDDL, true},
		{"admin only", "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"user read only", "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;" + me + ")", false},
		{"everyone only", "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;WD)", false},
		// Deny FILE_WRITE_DATA (0x2) without denying READ_CONTROL so the
		// elevated test process can still open the directory and inspect its DACL.
		{"user denied write", "D:P(D;;0x00000002;;;" + me + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + me + ")", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := filepath.Join(root, tc.name)
			if err := os.Mkdir(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			applySDDL(t, sub, tc.sddl)
			t.Cleanup(func() { applySDDL(t, sub, restoreSDDL) })

			got, err := ownerMatches(sub, me)
			if err != nil {
				t.Fatalf("ownerMatches: %v", err)
			}
			if got != tc.want {
				t.Errorf("ownerMatches(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestCheckNotSystemDir(t *testing.T) {
	for _, bad := range []string{`C:\`, `C:\Windows\Temp\x`, `C:\Program Files\Lanflow`, `C:\ProgramData\Lanflow`, `C:\Users`} {
		if err := checkNotSystemDir(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if err := checkNotSystemDir(`C:\Users\someone\AppData\Roaming\Lanflow`); err != nil {
		t.Errorf("user dir refused: %v", err)
	}
}

func TestOpenLogFileWindowsRefusesLinks(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(d, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(d, "hard.log")
	if err := os.Link(target, hard); err != nil {
		t.Fatal(err)
	}
	if w, err := openLogFile(hard, ""); err == nil {
		_ = w.Close()
		t.Fatal("opened a hard-linked log")
	}
	sym := filepath.Join(d, "sym.log")
	if err := os.Symlink(target, sym); err == nil {
		if w, err := openLogFile(sym, ""); err == nil {
			_ = w.Close()
			t.Fatal("opened a symlinked log")
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("target changed: %q", b)
	}
	ok := filepath.Join(d, "core.log")
	w, err := openLogFile(ok, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
}

func TestCheckControllerRefusesExistingPipe(t *testing.T) {
	name := `\\.\pipe\lanflow-test-` + strconv.Itoa(os.Getpid())
	if _, err := checkController(nil, name); err != nil {
		t.Fatalf("free pipe name refused: %v", err)
	}
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := checkController(nil, name); err == nil {
		t.Fatal("squatted pipe name accepted")
	}
}
