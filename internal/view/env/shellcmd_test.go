package env

import (
	"strings"
	"testing"
)

// TestShellCommandFor: the usual shell per system; Linux gets the POSIX
// line, not PowerShell's.
func TestShellCommandFor(t *testing.T) {
	for _, tc := range []struct{ goos, label, prefix string }{
		{"darwin", "bash / zsh", "export https_proxy=http://127.0.0.1:7891 "},
		{"linux", "bash / zsh", "export https_proxy=http://127.0.0.1:7891 "},
		{"windows", "PowerShell", `$env:HTTPS_PROXY="http://127.0.0.1:7891"`},
	} {
		got := ShellCommandFor(7891, tc.goos)
		if got.Label != tc.label || !strings.HasPrefix(got.Cmd, tc.prefix) {
			t.Errorf("%s: %+v", tc.goos, got)
		}
	}
	if cmds := ShellCommands(0); len(cmds) != 4 || !strings.Contains(cmds[1].Cmd, ":7890") {
		t.Errorf("default port: %+v", cmds)
	}
}

// TestThenWithoutShell: an Env without RunThen (a page's test) runs the
// job and done at once, in order, and passes the error on.
func TestThenWithoutShell(t *testing.T) {
	var order []string
	redraws := 0
	e := &Env{Redraw: func() { redraws++ }}
	e.Then(func() error { order = append(order, "job"); return nil }, func(err error) {
		if err != nil {
			t.Errorf("err %v", err)
		}
		order = append(order, "done")
	})
	e.OnUI(func() { order = append(order, "ui") })
	if strings.Join(order, ",") != "job,done,ui" || redraws != 2 {
		t.Fatalf("order %v, %d redraws", order, redraws)
	}
	e.Progress()
	if redraws != 3 {
		t.Fatal("Progress falls back to Redraw")
	}
	limited := 0
	e.RedrawData = func() { limited++ }
	e.Progress()
	if limited != 1 || redraws != 3 {
		t.Fatal("Progress should use RedrawData when there is one")
	}
}
