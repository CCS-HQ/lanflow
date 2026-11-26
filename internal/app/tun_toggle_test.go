package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/model"
	"lanflow/internal/platform/sysdns"
)

func TestTUNToggleKeepsHelperCoreAndProxy(t *testing.T) {
	for _, viaSettings := range []bool{false, true} {
		t.Run(map[bool]string{false: "switch", true: "settings"}[viaSettings], func(t *testing.T) {
			h := tunHandoffHarness(t)
			if err := h.s.SetSystemProxy(true); err != nil {
				t.Fatal(err)
			}
			before := h.s.Status()
			starts, stops := h.mgr.starts, h.mgr.stops
			sets, clears := h.sys.counts()
			h.s.d.waitPortFree = func(context.Context, int, bool, time.Duration) error {
				t.Error("a TUN toggle released the proxy listener")
				return errors.New("unexpected port wait")
			}
			for _, on := range []bool{false, true, false} {
				var err error
				if viaSettings {
					set := h.s.Settings()
					set.TUN = on
					err = h.s.SaveSettings(set)
				} else {
					err = h.s.SetTUN(on)
				}
				if err != nil {
					t.Fatalf("TUN %v: %v", on, err)
				}
				st := h.s.Status()
				if st.Core != model.CoreRunning || st.TUN != on || !st.SystemProxy || st.MixedPort != before.MixedPort || st.StartedAt != before.StartedAt {
					t.Fatalf("TUN %v: %+v", on, st)
				}
				if h.mgr.starts != starts || h.mgr.stops != stops || h.mgr.runner.(namedRunner).name != "helper" {
					t.Fatal("the switch replaced or stopped the helper core")
				}
				if gotSets, gotClears := h.sys.counts(); gotSets != sets || gotClears != clears {
					t.Fatal("the switch changed the existing system proxy")
				}
				if got := slices.Equal(h.dns.get(), sysdns.TUNServers); got != on {
					t.Fatalf("TUN %v: system DNS = %v", on, h.dns.get())
				}
				stored, err := loadSettings(h.dir)
				if err != nil || stored.TUN != on {
					t.Fatalf("stored TUN %v: %v", stored.TUN, err)
				}
			}
			if h.ctrl.reloads != 3 {
				t.Fatalf("reloads = %d, want 3", h.ctrl.reloads)
			}
		})
	}
}

func TestTUNToggleFailureKeepsPreviousRuntime(t *testing.T) {
	for _, failure := range []string{"reload", "state", "read state"} {
		t.Run(failure, func(t *testing.T) {
			h := tunHandoffHarness(t)
			before, err := os.ReadFile(h.s.runtime)
			if err != nil {
				t.Fatal(err)
			}
			starts, stops := h.mgr.starts, h.mgr.stops
			calls := 0
			h.ctrl.reloadFn = func(path string) error {
				calls++
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if calls == 1 {
					if bytes.Equal(b, before) {
						t.Error("did not load the new TUN setting")
					}
					if failure == "reload" {
						return errors.New("reload refused")
					}
					// Acknowledged, but TUN remains enabled.
					return nil
				}
				if !bytes.Equal(b, before) {
					t.Error("rollback did not reload the previous config")
				}
				h.ctrl.mu.Lock()
				h.ctrl.configsErr = nil
				h.ctrl.mu.Unlock()
				return nil
			}
			if failure == "read state" {
				h.ctrl.configsErr = errors.New("controller unavailable")
			}
			if err := h.s.SetTUN(false); err == nil {
				t.Fatal("refused TUN change succeeded")
			}
			after, _ := os.ReadFile(h.s.runtime)
			if calls != 2 || !bytes.Equal(after, before) || !h.s.Status().TUN || !h.s.Settings().TUN || h.s.Status().Core != model.CoreRunning {
				t.Fatalf("failed rollback: calls=%d, status=%+v", calls, h.s.Status())
			}
			if h.mgr.starts != starts || h.mgr.stops != stops {
				t.Fatal("failed reload restarted the core")
			}
			if !slices.Equal(h.dns.get(), sysdns.TUNServers) {
				t.Fatal("failed toggle restored DNS prematurely")
			}
		})
	}
}

func TestTUNToggleReportsFailedRollback(t *testing.T) {
	for _, failure := range []string{"reload", "state", "read state"} {
		t.Run(failure, func(t *testing.T) {
			h := tunHandoffHarness(t)
			calls := 0
			h.ctrl.reloadFn = func(string) error {
				calls++
				if calls == 1 || failure == "reload" {
					return errors.New("reload failed")
				}
				h.ctrl.mu.Lock()
				defer h.ctrl.mu.Unlock()
				h.ctrl.configs.Tun.Enable = false
				if failure == "read state" {
					h.ctrl.configsErr = errors.New("state unavailable")
				}
				return nil
			}
			err := h.s.SetTUN(false)
			if err == nil || !strings.Contains(err.Error(), "恢复运行配置也失败") || calls != 2 {
				t.Fatalf("rollback failure was hidden: calls=%d, err=%v", calls, err)
			}
			if failure == "state" && (h.s.Status().TUN || slices.Equal(h.dns.get(), sysdns.TUNServers)) {
				t.Fatal("observed TUN failure left the status or system DNS enabled")
			}
		})
	}
}

func TestTUNToggleStagesThroughHelper(t *testing.T) {
	h := tunHandoffHarness(t)
	var staged []core.Spec
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	h.s.d.core = stagingCore{fakeCore: h.core, runner: stagingRunner{namedRunner: namedRunner{"helper"}, specs: &staged, path: path}}
	h.ctrl.reloadFn = func(got string) error {
		if got != path {
			t.Fatalf("reloaded %s, want the helper's config %s", got, path)
		}
		h.ctrl.mu.Lock()
		h.ctrl.configs.Tun.Enable = false
		h.ctrl.mu.Unlock()
		return nil
	}
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 || staged[0].Config != h.s.runtime {
		t.Fatalf("staged: %+v", staged)
	}
	// Turning it back on must still respect the helper's validation.
	h.s.d.core = stagingCore{fakeCore: h.core, runner: stagingRunner{namedRunner: namedRunner{"helper"}, specs: &staged, err: errors.New("stage refused")}}
	if err := h.s.SetTUN(true); err == nil || !strings.Contains(err.Error(), "stage refused") {
		t.Fatalf("stage failure: %v", err)
	}
	if h.s.Settings().TUN || h.s.Status().TUN || h.ctrl.reloads != 1 {
		t.Fatal("refused config reached the core")
	}
}

func TestHelperWithTUNOffDoesNotHideOtherTUN(t *testing.T) {
	h := tunHandoffHarness(t)
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	h.stray.found, h.stray.tun = true, true
	h.tunOf = "utun-other"
	if err := h.s.SetTUN(true); err == nil {
		t.Fatal("another client's TUN was ignored")
	}
	if h.s.Status().TUN || h.s.Settings().TUN {
		t.Fatal("a conflicting TUN was enabled")
	}
}

func TestHelperPortWaitSurvivesAppRestart(t *testing.T) {
	h := tunHandoffHarness(t)
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	h.s.Shutdown() // quits the helper core even though TUN is now off
	if got := readRootStopped(h.dir, time.Now()); got.IsZero() {
		t.Fatal("helper exit was not saved")
	}
	next, err := newServiceWith(h.s.opt, h.s.d)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Shutdown()
	port := next.Settings().MixedPort
	h.taken[port] = true
	waited := false
	next.d.waitPortFree = func(_ context.Context, p int, _ bool, timeout time.Duration) error {
		if p != port || timeout < 30*time.Second || timeout > rootTimeWait {
			t.Fatalf("restart wait = %v, port = %d", timeout, p)
		}
		waited = true
		h.taken[port] = false
		return nil
	}
	if err := next.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	if !waited || next.Status().TUN || !next.Status().SystemProxy || next.Status().MixedPort != port {
		t.Fatalf("restart: %+v", next.Status())
	}
	if h.mgr.runner.(namedRunner).name != "exec" {
		t.Fatal("an app restart kept privileges with TUN off")
	}
}

func TestRootStoppedRecordExpires(t *testing.T) {
	for _, value := range []string{"bad", time.Now().Add(-time.Hour).Format(time.RFC3339Nano), time.Now().Add(time.Hour).Format(time.RFC3339Nano)} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, rootStoppedFile), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readRootStopped(dir, time.Now()); !got.IsZero() {
			t.Fatalf("accepted stale/invalid timestamp %q", value)
		}
	}
}

func TestHelperCrashAfterTUNOffUsesHelperCleanup(t *testing.T) {
	h := tunHandoffHarness(t)
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.stray.found, h.stray.tun = true, true
	h.mgr.mu.Lock()
	h.mgr.running = false
	h.mgr.mu.Unlock()
	h.s.handleCrash(core.ErrStateUnknown)
	if h.stray.stops != 1 || !h.stray.stopTUN {
		t.Fatal("helper core was treated as an ordinary child")
	}
	if st := h.s.Status(); st.Core != model.CoreRunning || st.TUN || !st.SystemProxy {
		t.Fatalf("recovery: %+v", st)
	}
	if got := readRootStopped(h.dir, time.Now()); got.IsZero() {
		t.Fatal("helper crash lost its port wait")
	}
}

func TestUninstallHelperAfterTUNOffMovesCoreToUser(t *testing.T) {
	h := tunHandoffHarness(t)
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.UninstallTUNHelper(); err != nil {
		t.Fatal(err)
	}
	if h.mgr.runner.(namedRunner).name != "exec" || h.helper.installed {
		t.Fatal("removed a helper that still owns the running proxy")
	}
	if st := h.s.Status(); st.Core != model.CoreRunning || st.TUN || !st.SystemProxy {
		t.Fatalf("uninstall: %+v", st)
	}
}

func TestCoreSwitchAfterTUNOffWaitsForHelperPort(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.core.findFn = realFind
		h.helper.installed = true
	})
	_, newer := twoCores(t, h)
	if err := h.s.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetTUN(false); err != nil {
		t.Fatal(err)
	}
	port := h.s.Settings().MixedPort
	h.mgr.mu.Lock()
	h.mgr.onStop = func() { h.taken[port] = true }
	h.mgr.mu.Unlock()
	waited := false
	h.s.d.waitPortFree = func(_ context.Context, p int, _ bool, timeout time.Duration) error {
		if p != port || timeout <= portWaitShort {
			t.Fatalf("binary switch wait = %v, port = %d", timeout, p)
		}
		waited = true
		h.taken[port] = false
		return nil
	}
	if err := h.s.UseCore("v1.19.33"); err != nil {
		t.Fatal(err)
	}
	if !waited || h.mgr.specs[len(h.mgr.specs)-1].Bin != newer || h.mgr.runner.(namedRunner).name != "exec" {
		t.Fatal("core replacement skipped the privileged port handoff")
	}
}
