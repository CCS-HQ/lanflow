package app

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func shortPortRelease(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldPoll := portReleaseWait, portReleasePoll
	portReleaseWait, portReleasePoll = wait, time.Millisecond
	t.Cleanup(func() { portReleaseWait, portReleasePoll = oldWait, oldPoll })
}

// Turning TUN off on macOS: the root core is gone and nothing listens on
// the mixed port, but the connections it had accepted there still linger
// (TIME_WAIT), and the kernel refuses the user's bind until they end. The
// start waits for that instead of reporting the port taken.
func TestStartWaitsForLingeringPort(t *testing.T) {
	shortPortRelease(t, 5*time.Second)
	h := newHarness(t, nil)
	var checks atomic.Int32
	h.s.d.portInUse = func(int, bool) bool { return checks.Add(1) <= 4 }
	h.s.d.portListening = func(int) bool { return false }
	if err := h.s.StartCore(); err != nil {
		t.Fatalf("start with a port that frees itself: %v", err)
	}
	if n := checks.Load(); n < 5 {
		t.Fatalf("port checked %d times, want it polled until free", n)
	}
}

// A port another program listens on is reported at once, without waiting.
func TestStartDoesNotWaitForListenedPort(t *testing.T) {
	shortPortRelease(t, time.Hour)
	h := newHarness(t, nil)
	h.s.d.portInUse = func(int, bool) bool { return true }
	h.s.d.portListening = func(int) bool { return true }
	start := time.Now()
	err := h.s.StartCore()
	if err == nil || !strings.Contains(err.Error(), "占用") {
		t.Fatalf("err = %v, want the port reported taken", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("waited %s for a port another program listens on", took)
	}
}

// A port that never frees (or that something starts listening on while
// waiting) is reported taken once the wait is over.
func TestStartGivesUpOnPortThatStaysTaken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		listen func(n int32) bool
	}{
		{"never released", func(int32) bool { return false }},
		{"another program takes it", func(n int32) bool { return n > 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shortPortRelease(t, 300*time.Millisecond)
			h := newHarness(t, nil)
			var n atomic.Int32
			h.s.d.portInUse = func(int, bool) bool { return true }
			h.s.d.portListening = func(int) bool { return tc.listen(n.Add(1)) }
			if err := h.s.StartCore(); err == nil || !strings.Contains(err.Error(), "占用") {
				t.Fatalf("err = %v, want the port reported taken", err)
			}
		})
	}
}

func TestTCPPortListening(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if !tcpPortListening(port) {
		t.Error("a listened port reported free of listeners")
	}
	l.Close()
	if tcpPortListening(port) {
		t.Error("a closed port reported listened on")
	}
}
