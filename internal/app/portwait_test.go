package app

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
)

func TestWaitTCPPortFree(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS port handoff")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitTCPPortFree(ctx, port, false, time.Second); !errors.Is(err, errPortListening) {
		t.Fatalf("live listener: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitTCPPortFree(ctx, port, false, time.Second); err != nil {
		t.Fatalf("released listener: %v", err)
	}
}

func TestWaitPortRelease(t *testing.T) {
	t.Run("closed sockets drain", func(t *testing.T) {
		checks := 0
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := waitPortRelease(ctx, time.Millisecond, func() bool {
			checks++
			return checks < 3
		}, func() bool { return false })
		if err != nil || checks != 3 {
			t.Fatalf("wait = %v, checks = %d", err, checks)
		}
	})
	t.Run("listener is a conflict", func(t *testing.T) {
		err := waitPortRelease(context.Background(), time.Hour, func() bool { return true }, func() bool { return true })
		if !errors.Is(err, errPortListening) {
			t.Fatalf("wait = %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		err := waitPortRelease(ctx, time.Millisecond, func() bool { return true }, func() bool { return false })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait = %v", err)
		}
	})
	t.Run("shutdown cancels even an available port", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitPortRelease(ctx, time.Millisecond, func() bool { return false }, func() bool { return false }); !errors.Is(err, context.Canceled) {
			t.Fatalf("wait = %v", err)
		}
	})
}

func tunHandoffHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, nil)
	if err := h.s.InstallTUNHelper(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	h.ctrl.configs.Tun.Enable = true
	h.ctrl.mu.Unlock()
	return h
}

// A stopped privileged core can leave its port unbindable for a while.
// After an explicit stop, a later user core must still wait for the port.
func TestStoppedTUNToSystemProxyKeepsPort(t *testing.T) {
	for _, viaSettings := range []bool{false, true} {
		t.Run(map[bool]string{false: "switch", true: "settings"}[viaSettings], func(t *testing.T) {
			h := tunHandoffHarness(t)
			port := h.s.Settings().MixedPort
			starts := mgrStarts(h)
			h.mgr.mu.Lock()
			h.mgr.onStop = func() { h.taken[port] = true }
			h.mgr.mu.Unlock()
			if err := h.s.StopCore(); err != nil {
				t.Fatal(err)
			}
			waited := false
			h.s.d.waitPortFree = func(ctx context.Context, p int, lan bool, timeout time.Duration) error {
				if p != port || h.mgr.Running() || mgrStarts(h) != starts {
					t.Fatal("port handoff did not happen between stop and start")
				}
				if timeout < 30*time.Second {
					t.Fatalf("waits %v after a root core stopped, want the TIME_WAIT period", timeout)
				}
				waited = true
				h.taken[port] = false
				return nil
			}
			var err error
			if viaSettings {
				set := h.s.Settings()
				set.TUN = false
				err = h.s.SaveSettings(set)
			} else {
				err = h.s.SetTUN(false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := h.s.StartCore(); err != nil {
				t.Fatal(err)
			}
			if err := h.s.SetSystemProxy(true); err != nil {
				t.Fatal(err)
			}
			st := h.s.Status()
			proxy, _ := h.sys.Get()
			if !waited || st.Core != model.CoreRunning || st.TUN || !st.SystemProxy || st.MixedPort != port || proxy.Port != port || h.s.Settings().MixedPort != port {
				t.Fatalf("handoff: waited=%v, status=%+v, proxy=%+v", waited, st, proxy)
			}
			h.mgr.mu.Lock()
			runner := h.mgr.runner.(namedRunner).name
			h.mgr.onStop = nil
			h.mgr.mu.Unlock()
			if runner != "exec" {
				t.Fatalf("runner = %q", runner)
			}
		})
	}
}

func TestTUNRestartFailureRestoresSettings(t *testing.T) {
	for _, cause := range []error{errPortListening, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			h := tunHandoffHarness(t)
			port := h.s.Settings().MixedPort
			h.taken[port] = true
			h.s.d.waitPortFree = func(context.Context, int, bool, time.Duration) error { return cause }
			before := mgrStarts(h)
			set := h.s.Settings()
			set.TUN, set.CorePath = false, "/fake/another-mihomo"
			err := h.s.SaveSettings(set) // changing the binary still requires a restart
			if err == nil || !h.s.Settings().TUN || mgrStarts(h) != before {
				t.Fatalf("failed handoff: err=%v, TUN=%v, starts=%d", err, h.s.Settings().TUN, mgrStarts(h))
			}
			conflict := strings.Contains(err.Error(), "已被其他程序")
			if conflict != errors.Is(cause, errPortListening) {
				t.Fatalf("wrong failure reason: %v", err)
			}
			stored, err := loadSettings(h.dir)
			if err != nil || !stored.TUN {
				t.Fatalf("stored TUN=%v, err=%v", stored.TUN, err)
			}
			h.taken[port] = false
		})
	}
}

func TestTUNStopFailureDoesNotStartAnotherCore(t *testing.T) {
	h := tunHandoffHarness(t)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	before := mgrStarts(h)
	mgr := h.s.d.mgr
	h.s.d.mgr = stopFailMgr{mgr}
	err := h.s.RestartCore()
	h.s.d.mgr = mgr
	if err == nil || !strings.Contains(err.Error(), "helper did not answer") || mgrStarts(h) != before {
		t.Fatalf("stop failure: err=%v, starts=%d", err, mgrStarts(h))
	}
	if st := h.s.Status(); st.Core != model.CoreError || !st.TUN || !st.SystemProxy || !h.s.Settings().TUN {
		t.Fatalf("lost the old core's state: %+v", st)
	}
	if err := h.s.SetTUN(false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if st := h.s.Status(); st.Core != model.CoreRunning || st.TUN || !st.SystemProxy || h.s.Settings().TUN || mgrStarts(h) != before+1 {
		t.Fatalf("retry did not switch the core: %+v", st)
	}
}

func TestPortWaitTimeout(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name    string
		stopped time.Time
		want    time.Duration
	}{
		{"no root core stopped", time.Time{}, portWaitShort},
		{"root core just stopped", now, rootTimeWait},
		{"root core stopped 10 s ago", now.Add(-10 * time.Second), rootTimeWait - 10*time.Second},
		{"root core stopped long ago", now.Add(-time.Hour), portWaitShort},
	} {
		if got := portWaitTimeout(now, c.stopped); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A busy port with no root core gone lately gets only the short wait.
func TestPlainStartWaitsShort(t *testing.T) {
	h := newHarness(t, nil)
	port := h.s.Settings().MixedPort
	h.taken[port] = true
	var got time.Duration
	h.s.d.waitPortFree = func(_ context.Context, _ int, _ bool, timeout time.Duration) error {
		got = timeout
		return context.DeadlineExceeded
	}
	if err := h.s.StartCore(); err == nil {
		t.Fatal("started on a busy port")
	}
	if got != portWaitShort {
		t.Fatalf("waited %v, want %v", got, portWaitShort)
	}
}

// Restarting with TUN on: the next core runs as root and binds over the
// old one's TIME_WAIT sockets, so there is nothing to wait for; a real
// listener still stops it.
func TestRootRestartDoesNotWait(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS port handoff")
	}
	h := tunHandoffHarness(t)
	port := h.s.Settings().MixedPort
	h.mgr.mu.Lock()
	h.mgr.onStop = func() { h.taken[port] = true }
	h.mgr.mu.Unlock()
	h.s.d.waitPortFree = func(context.Context, int, bool, time.Duration) error {
		t.Fatal("waited for the port before starting a root core")
		return nil
	}
	listening := false
	h.s.d.portListening = func(int) bool { return listening }
	before := mgrStarts(h)
	if err := h.s.RestartCore(); err != nil {
		t.Fatal(err)
	}
	if st := h.s.Status(); st.Core != model.CoreRunning || !st.TUN || mgrStarts(h) != before+1 {
		t.Fatalf("restart: %+v", st)
	}
	listening = true
	if err := h.s.RestartCore(); err == nil || !strings.Contains(err.Error(), "已被其他程序") {
		t.Fatalf("restart over a listener: %v", err)
	}
}
