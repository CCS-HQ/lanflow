//go:build windows

package ctl

import "testing"

func TestIsMSYSPtyName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{`\msys-1888ae32e00d56aa-pty0-to-master`, true},
		{`\cygwin-e022582115c10879-pty1-from-master`, true},
		{`\MSYS-abcd-PTY2-from-master`, true},
		{`\Device\NamedPipe\lanflow-123`, false},
		{`\\.\pipe\regular-pipe`, false},
		{``, false},
	} {
		if got := isMSYSPtyName(tc.name); got != tc.want {
			t.Errorf("isMSYSPtyName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
