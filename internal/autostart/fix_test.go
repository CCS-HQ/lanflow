package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRun is the Run value and Task Manager's switch in memory.
type fakeRun struct {
	cmd      string
	has      bool
	disabled bool // StartupApproved says "off"
	ops      []string
	setErr   error
}

func (f *fakeRun) Get() (string, bool) { return f.cmd, f.has }
func (f *fakeRun) Set(cmd string) error {
	f.ops = append(f.ops, "set")
	if f.setErr != nil {
		return f.setErr
	}
	f.cmd, f.has = cmd, true
	return nil
}
func (f *fakeRun) Delete() error {
	f.ops = append(f.ops, "delete")
	f.cmd, f.has = "", false
	return nil
}
func (f *fakeRun) ClearApproval() bool {
	f.ops = append(f.ops, "approve")
	was := f.disabled
	f.disabled = false
	return was
}

func TestApplyRun(t *testing.T) {
	const want = `"C:\Lanflow\Lanflow.exe" --service --login`
	boom := errors.New("boom")
	for _, tc := range []struct {
		name         string
		st           fakeRun
		on           bool
		wantChanged  bool
		wantErr      error
		wantOps      []string
		wantDisabled bool
		wantHas      bool
	}{
		{name: "turned on: the entry is created and any old Task Manager switch cleared",
			st: fakeRun{disabled: true}, on: true, wantChanged: true, wantOps: []string{"set", "approve"}, wantHas: true},
		// Review item 12: Apply runs on every settings change and every
		// start; an entry the user disabled in Task Manager stays disabled.
		{name: "already on, disabled in Task Manager: left alone",
			st: fakeRun{cmd: want, has: true, disabled: true}, on: true, wantDisabled: true, wantHas: true},
		{name: "already on: nothing written",
			st: fakeRun{cmd: want, has: true}, on: true, wantHas: true},
		{name: "same command in another case",
			st: fakeRun{cmd: strings.ToUpper(want), has: true, disabled: true}, on: true, wantDisabled: true, wantHas: true},
		{name: "the executable moved: rewritten, Task Manager's switch kept",
			st: fakeRun{cmd: `"D:\old\Lanflow.exe" --service --login`, has: true, disabled: true}, on: true,
			wantChanged: true, wantOps: []string{"set"}, wantDisabled: true, wantHas: true},
		{name: "turned off", st: fakeRun{cmd: want, has: true, disabled: true}, wantChanged: true, wantOps: []string{"delete", "approve"}},
		{name: "off and absent", st: fakeRun{}, wantOps: []string{"approve"}},
		{name: "the write fails", st: fakeRun{setErr: boom, disabled: true}, on: true, wantErr: boom, wantOps: []string{"set"}, wantDisabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st
			changed, err := applyRun(&st, want, tc.on)
			if changed != tc.wantChanged || !errors.Is(err, tc.wantErr) {
				t.Fatalf("changed = %v err = %v", changed, err)
			}
			if !slices.Equal(st.ops, tc.wantOps) {
				t.Fatalf("ops = %v, want %v", st.ops, tc.wantOps)
			}
			if st.disabled != tc.wantDisabled || st.has != tc.wantHas {
				t.Fatalf("disabled = %v has = %v", st.disabled, st.has)
			}
			if tc.on && tc.wantErr == nil && !strings.EqualFold(st.cmd, want) {
				t.Fatalf("command = %s", st.cmd)
			}
		})
	}
}

func useXDG(t *testing.T, configHome, home string) {
	t.Helper()
	oldEnv, oldHome := xdgEnv, xdgHome
	xdgEnv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return configHome
		}
		return ""
	}
	xdgHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { xdgEnv, xdgHome = oldEnv, oldHome })
}

func TestXDGAutostart(t *testing.T) {
	dir := t.TempDir()
	useXDG(t, "", dir)
	path := filepath.Join(dir, ".config", "autostart", Label+".desktop")
	exe := "/opt/Lanflow/lanflow"
	if xdgEnabled(exe) {
		t.Fatal("enabled before")
	}
	if changed, err := xdgApply(exe, false); err != nil || changed {
		t.Fatalf("off when absent: %v %v", changed, err)
	}
	if changed, err := xdgApply(exe, true); err != nil || !changed {
		t.Fatalf("apply: %v %v", changed, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Desktop Entry]\n", "Type=Application\n", `Exec="/opt/Lanflow/lanflow" --service --login` + "\n"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("entry %q lacks %q", b, want)
		}
	}
	if !xdgEnabled(exe) || xdgEnabled("/elsewhere/lanflow") {
		t.Fatal("enabled reports wrongly")
	}
	if changed, err := xdgApply(exe, true); err != nil || changed {
		t.Fatalf("second apply wrote again: %v %v", changed, err)
	}
	// The executable moved: rewritten.
	if changed, err := xdgApply("/usr/bin/lanflow", true); err != nil || !changed {
		t.Fatalf("moved: %v %v", changed, err)
	}
	if changed, err := xdgApply(exe, false); err != nil || !changed {
		t.Fatalf("off: %v %v", changed, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("entry left: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 0 {
		t.Fatalf("left in the autostart directory: %v", entries)
	}

	// XDG_CONFIG_HOME, when absolute, is where the entry goes.
	custom := filepath.Join(dir, "custom")
	useXDG(t, custom, dir)
	if _, err := xdgApply(exe, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(custom, "autostart", Label+".desktop")); err != nil {
		t.Fatal(err)
	}
	useXDG(t, "relative/ignored", dir)
	if p, _ := xdgEntryPath(); p != path {
		t.Fatalf("a relative XDG_CONFIG_HOME was used: %s", p)
	}
	if _, err := xdgApply("/opt/bad\npath/lanflow", true); err == nil {
		t.Fatal("a path with a line break was written")
	}
}

func TestXDGExecArg(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/lanflow":      `"/usr/bin/lanflow"`,
		"/opt/My Apps/lanflow":  `"/opt/My Apps/lanflow"`,
		`/opt/a"b/lanflow`:      `"/opt/a\\"b/lanflow"`,
		"/opt/$HOME/`id`/l":     `"/opt/\\$HOME/\\` + "`id\\\\`/l\"",
		`/opt/back\slash/l`:     `"/opt/back\\\\slash/l"`,
		"/opt/100%/lanflow":     `"/opt/100%%/lanflow"`,
		"/opt/澜流/lanflow":       `"/opt/澜流/lanflow"`,
		"/opt/semi;colon&amp/l": `"/opt/semi;colon&amp/l"`,
	} {
		if got := xdgExecArg(in); got != want {
			t.Errorf("xdgExecArg(%q) = %s, want %s", in, got, want)
		}
	}
}
