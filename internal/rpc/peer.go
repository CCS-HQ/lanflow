package rpc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// The peer process (docs/service-split.md §4). A connection says who it
// is in the handshake; what the server can check itself is which program
// is at the other end. The window, `lanflow ctl` and `lanflow mcp` are
// all Lanflow's own executable, so:
//
//   - "ui" is accepted only from that executable: another program of the
//     user cannot skip the permission levels by calling itself the window;
//   - a ctl connection counts as a person at a terminal, and a remembered
//     "always allow" counts, only when it comes from that executable.
//
// This keeps agents inside the permission levels; it is not a defence
// against a hostile local program, which can run Lanflow's own executable
// under its control (a pty, an inherited socket, injected code).

var selfExeOnce = sync.OnceValue(func() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
})

// selfExeOverride replaces the server's own executable, for tests.
var selfExeOverride atomic.Pointer[string]

// selfExe is the executable of this process ("" when unknown: no peer
// matches then).
func selfExe() string {
	if p := selfExeOverride.Load(); p != nil {
		return *p
	}
	return selfExeOnce()
}

// sameExe reports whether the executables a and b are one file.
func sameExe(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b || (runtime.GOOS == "windows" && strings.EqualFold(a, b)) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}
