package main

import (
	"errors"
	"strings"
	"testing"

	"lanflow/internal/i18n"
)

// A real service that cannot be created ends the process with an error
// for the user; the fake backend (sample nodes and traffic, switches that
// do nothing) is only ever used with LANFLOW_FAKE=1.
func TestBackendFailureIsToldAndFatal(t *testing.T) {
	oldTell, oldExit, oldLang := tellUser, exit, i18n.Lang()
	t.Cleanup(func() { tellUser, exit = oldTell, oldExit; i18n.SetLang(oldLang) })
	for _, tc := range []struct{ lang, want string }{
		{"zh", "澜流无法启动：mkdir /data: permission denied"},
		{"en", "Lanflow cannot start: mkdir /data: permission denied"},
	} {
		var told []string
		code := -1
		tellUser = func(msg string) { told = append(told, msg) }
		exit = func(c int) { code = c }
		backendFailed(tc.lang, errors.New("mkdir /data: permission denied"))
		if len(told) != 1 || !strings.HasPrefix(told[0], tc.want) {
			t.Errorf("%s: told %q", tc.lang, told)
		}
		if code != 1 {
			t.Errorf("%s: exit code %d, want 1", tc.lang, code)
		}
	}
}
