package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
	"lanflow/internal/platform/atrust"
	"lanflow/internal/platform/sysproxy"
)

// Regression tests of the runtime fixes: what happens to the system proxy
// and DNS when the core goes away, stale crash reports, the PAC mode beside
// aTrust, the probe group's lock, SetMode against reloads, the statistics'
// last flush and the bounded tables. All on the harness's fakes.

// stuckSys is a system proxy that cannot be released while stuck is set.
type stuckSys struct {
	sysProxy
	stuck atomic.Bool
}

var errStuck = errors.New("permission denied")

func (f *stuckSys) Clear() error {
	if f.stuck.Load() {
		return errStuck
	}
	return f.sysProxy.Clear()
}
func (f *stuckSys) Restore(s sysproxy.Saved) error {
	if f.stuck.Load() {
		return errStuck
	}
	return f.sysProxy.Restore(s)
}
func (f *stuckSys) SetPAC(url string) error {
	if f.stuck.Load() && url == "" {
		return errStuck
	}
	return f.sysProxy.SetPAC(url)
}

// stopFailMgr is a manager whose Stop fails.
type stopFailMgr struct{ coreManager }

func (stopFailMgr) Stop(context.Context) error { return errors.New("helper did not answer") }

// patchFailCtrl is a controller whose PATCH /configs fails while fail is set.
type patchFailCtrl struct {
	ctrlClient
	fail atomic.Bool
}

func (c *patchFailCtrl) PatchConfigs(ctx context.Context, p controller.ConfigPatch) error {
	if c.fail.Load() {
		return errors.New("controller unreachable")
	}
	return c.ctrlClient.PatchConfigs(ctx, p)
}

// cancelCtrl counts SelectProxy calls and fails those whose context ended.
type cancelCtrl struct {
	ctrlClient
	selects atomic.Int32
}

func (c *cancelCtrl) SelectProxy(ctx context.Context, group, name string) error {
	c.selects.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.ctrlClient.SelectProxy(ctx, group, name)
}

func setStartErr(h *harness, err error) {
	h.mgr.mu.Lock()
	h.mgr.startErr = err
	h.mgr.mu.Unlock()
}

func mgrStarts(h *harness) int {
	h.mgr.mu.Lock()
	defer h.mgr.mu.Unlock()
	return h.mgr.starts
}

func setMgrRunning(h *harness, running bool) {
	h.mgr.mu.Lock()
	h.mgr.running = running
	h.mgr.mu.Unlock()
}

// stopMainProbe cancels the automatic exit probe a start schedules, so a
// test decides itself when one runs.
func stopMainProbe(h *harness) {
	h.s.probeMu.Lock()
	if h.s.probeTimer != nil {
		h.s.probeTimer.Stop()
	}
	h.s.probeMu.Unlock()
}

// freePACPort is a TCP port free on 127.0.0.1 right now, for the PAC server.
func freePACPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// pacHarness is a harness in PAC mode, the PAC server on a free port.
func pacHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, nil)
	set := h.s.Settings()
	set.Advanced.PACMode, set.Advanced.PACPort = true, freePACPort(t)
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	return h
}

// A restart whose start fails leaves no core: the system proxy and the
// system DNS must not keep pointing at it (review item 1), and when the
// proxy cannot be released the error says to do it by hand (item 8).
func TestRestartFailureLetsGoOfProxyAndDNS(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stuck     bool
		wantProxy bool   // Status.SystemProxy afterwards
		wantText  string // in the returned error
	}{
		{"released", false, false, "系统代理已关闭"},
		{"release fails", true, true, "手动关闭代理"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sys *stuckSys
			h := newHarness(t, func(h *harness) { h.helper.installed = true; h.dns.servers = []string{"192.168.1.1"} })
			sys = &stuckSys{sysProxy: h.s.d.sys}
			h.s.d.sys = sys
			if err := h.s.SetSystemProxy(true); err != nil {
				t.Fatal(err)
			}
			if err := h.s.SetTUN(true); err != nil {
				t.Fatal(err)
			}
			if st := h.s.Status(); !st.SystemProxy || !st.TUN {
				t.Fatalf("setup: %+v", st)
			}
			setStartErr(h, errors.New("address already in use"))
			sys.stuck.Store(tc.stuck)

			err := h.s.RestartCore()
			if err == nil || !strings.Contains(err.Error(), tc.wantText) || !strings.Contains(err.Error(), "address already in use") {
				t.Fatalf("RestartCore error = %v, want it to hold %q and the cause", err, tc.wantText)
			}
			st := h.s.Status()
			if st.Core != model.CoreError || st.TUN {
				t.Fatalf("status after the failed restart: core %s, TUN %v", st.Core, st.TUN)
			}
			if st.SystemProxy != tc.wantProxy {
				t.Fatalf("Status.SystemProxy = %v, want %v", st.SystemProxy, tc.wantProxy)
			}
			cur, _ := h.sys.Get()
			if cur.Enabled != tc.wantProxy {
				t.Fatalf("system proxy enabled = %v, want %v (%+v)", cur.Enabled, tc.wantProxy, cur)
			}
			if h.s.markerExists() != tc.wantProxy {
				t.Fatalf("marker exists = %v, want %v", h.s.markerExists(), tc.wantProxy)
			}
			if got := h.dns.get(); !slices.Equal(got, []string{"192.168.1.1"}) {
				t.Fatalf("system DNS after the failed restart = %v, want it put back", got)
			}
			sys.stuck.Store(false) // let Shutdown clean up
		})
	}
}

// A crash report that is not about the running core changes nothing
// (review item 2): neither one that arrives after an operation started
// the core again, nor the second report of an exit already handled.
func TestStaleCrashReportIsIgnored(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.s.TakeNotices()
	healthy := func(what string, starts int) {
		t.Helper()
		st := h.s.Status()
		if st.Core != model.CoreRunning || !st.SystemProxy || st.CoreError != "" {
			t.Fatalf("%s: status = core %s, proxy %v, error %q", what, st.Core, st.SystemProxy, st.CoreError)
		}
		if got := mgrStarts(h); got != starts {
			t.Fatalf("%s: starts = %d, want %d", what, got, starts)
		}
		if cur, _ := h.sys.Get(); !cur.Enabled {
			t.Fatalf("%s: the system proxy was released: %+v", what, cur)
		}
		h.s.mu.Lock()
		streams := h.s.streamCancel != nil
		h.s.mu.Unlock()
		if !streams {
			t.Fatalf("%s: the streams of the running core were stopped", what)
		}
	}

	// Stale: the manager runs (the core was started again since the exit).
	h.s.handleCrash(errors.New("exit status 2"))
	healthy("stale report", 1)
	if _, clears := h.sys.counts(); clears != 0 {
		t.Fatalf("stale report released the proxy %d time(s)", clears)
	}
	if ns := h.s.TakeNotices(); len(ns) != 0 {
		t.Fatalf("stale report notified: %+v", ns)
	}

	// A real exit, reported twice (the crash watcher and the wake recheck):
	// one restart, and the second report leaves the new core alone.
	setMgrRunning(h, false)
	h.s.handleCrash(errors.New("exit status 2"))
	healthy("first report", 2)
	h.s.TakeNotices()
	h.s.handleCrash(errors.New("exit status 2"))
	healthy("duplicate report", 2)
	for _, n := range h.s.TakeNotices() {
		if n.Kind == "core-crash" {
			t.Fatalf("duplicate report was treated as a second crash: %+v", n)
		}
	}
}

// A crash whose proxy release fails must not claim the proxy is off
// (review item 8).
func TestCrashWithStuckProxySaysSo(t *testing.T) {
	h := newHarness(t, nil)
	sys := &stuckSys{sysProxy: h.s.d.sys}
	h.s.d.sys = sys
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	h.s.TakeNotices()
	setStartErr(h, errors.New("no such file")) // the automatic restart fails too
	setMgrRunning(h, false)
	sys.stuck.Store(true)
	h.s.handleCrash(errors.New("exit status 2"))
	sys.stuck.Store(false)

	if st := h.s.Status(); st.Core != model.CoreError || !st.SystemProxy {
		t.Fatalf("status = core %s, proxy %v; the proxy is still set and must show so", st.Core, st.SystemProxy)
	}
	var crash *Notice
	for _, n := range h.s.TakeNotices() {
		if n.Kind == "core-crash" {
			crash = &n
		}
	}
	if crash == nil {
		t.Fatal("no core-crash notice")
	}
	if !strings.Contains(crash.Detail, "手动关闭代理") || strings.Contains(crash.Detail, "系统代理已关闭") {
		t.Fatalf("notice detail = %q", crash.Detail)
	}
}

// A stop the manager could not carry out is not shown as stopped (item 8).
func TestStopFailureKeepsErrorState(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.d.mgr = stopFailMgr{h.s.d.mgr}
	if err := h.s.StopCore(); err == nil {
		t.Fatal("StopCore succeeded although the manager's Stop failed")
	}
	if st := h.s.Status(); st.Core != model.CoreError || !strings.Contains(st.CoreError, "helper did not answer") {
		t.Fatalf("status = core %s, error %q", st.Core, st.CoreError)
	}
}

// aTrust coming or going while the system proxy is a PAC URL keeps it a
// PAC URL that matches the marker (review item 3).
func TestCoexistKeepsPACMode(t *testing.T) {
	h := pacHarness(t)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	before, _ := h.sys.Get()
	if before.PACURL == "" || before.Enabled {
		t.Fatalf("setup: %+v", before)
	}
	for _, step := range []struct {
		name string
		info func()
	}{
		{"aTrust connects", func() { h.vpn.set(sampleATrust) }},
		{"aTrust quits", func() { h.vpn.set(atrust.Info{}) }},
	} {
		step.info()
		h.s.recheckCoexist()
		cur, _ := h.sys.Get()
		if cur.Enabled || cur.PACURL != before.PACURL {
			t.Fatalf("%s: system proxy = %+v, want the PAC URL %s and no manual proxy", step.name, cur, before.PACURL)
		}
		if sets, _ := h.sys.counts(); sets != 0 {
			t.Fatalf("%s: the manual proxy was set %d time(s) in PAC mode", step.name, sets)
		}
		m, ok := h.s.readMarker()
		if !ok || !proxyIsOurs(cur, m, m.Port) {
			t.Fatalf("%s: the marker (%+v, %v) no longer matches the system proxy %+v", step.name, m, ok, cur)
		}
		if !h.s.Status().SystemProxy {
			t.Fatalf("%s: ownership of the system proxy was dropped", step.name)
		}
	}
}

// The automatic exit probe never switches the probe group under another
// probe; it is put off, and a caller that may wait gives up with its
// context (review item 4).
func TestMainProbeYieldsToRunningProbe(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stopMainProbe(h)
	probed := func() int {
		h.loc.mu.Lock()
		defer h.loc.mu.Unlock()
		return len(h.loc.probed)
	}
	probeSelects := func() int {
		h.ctrl.mu.Lock()
		defer h.ctrl.mu.Unlock()
		n := 0
		for _, sel := range h.ctrl.selected {
			if sel[0] == probeGroup {
				n++
			}
		}
		return n
	}

	h.s.probeExitMu.Lock() // an unlock check or a region scan is running
	h.s.probeMu.Lock()
	h.s.probeTimer = nil
	h.s.probeMu.Unlock()
	h.s.runMainProbe()
	if probed() != 0 || probeSelects() != 0 {
		t.Fatalf("the automatic probe ran under another probe: probed %d, selects in the probe group %d", probed(), probeSelects())
	}
	h.s.probeMu.Lock()
	rescheduled := h.s.probeTimer != nil
	h.s.probeMu.Unlock()
	if !rescheduled {
		t.Fatal("the automatic probe was dropped instead of put off")
	}
	stopMainProbe(h)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	err := h.s.probeNode(ctx, "东京 03")
	cancel()
	if !errors.Is(err, errProbeGroupBusy) || probed() != 0 {
		t.Fatalf("probeNode under another probe: err %v, probed %d", err, probed())
	}
	h.s.probeExitMu.Unlock()

	h.s.runMainProbe()
	if probed() != 1 || probeSelects() != 1 {
		t.Fatalf("free probe group: probed %d, selects %d, want 1 and 1", probed(), probeSelects())
	}
	if !h.s.probeExitMu.TryLock() {
		t.Fatal("the automatic probe kept probeExitMu")
	}
	h.s.probeExitMu.Unlock()
}

// The aTrust poll detects without opMu: an unchanged result never waits
// for a running operation (review item 5).
func TestCoexistPollDoesNotWaitForOperations(t *testing.T) {
	h := newHarness(t, nil)
	h.s.opMu.Lock()
	done := make(chan struct{})
	go func() { h.s.recheckCoexist(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		h.s.opMu.Unlock()
		t.Fatal("recheckCoexist waited for opMu although nothing changed")
	}
	h.s.opMu.Unlock()
	h.vpn.mu.Lock()
	calls := h.vpn.calls
	h.vpn.mu.Unlock()
	if calls != 1 {
		t.Fatalf("detector calls = %d, want 1", calls)
	}
}

// SetMode is an operation like a reload: it waits for opMu, stores the
// mode before patching the core, and takes it back when the patch fails
// (review item 6).
func TestSetModeSerializesWithOperations(t *testing.T) {
	var ctrl *patchFailCtrl
	h := newHarness(t, nil)
	ctrl = &patchFailCtrl{ctrlClient: h.s.d.ctrl}
	h.s.d.ctrl = ctrl
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stopMainProbe(h)
	modePatches := func() []string {
		h.ctrl.mu.Lock()
		defer h.ctrl.mu.Unlock()
		var out []string
		for _, p := range h.ctrl.patches {
			if p.Mode != nil {
				out = append(out, *p.Mode)
			}
		}
		return out
	}

	h.s.opMu.Lock() // a reload is in flight
	done := make(chan error, 1)
	go func() { done <- h.s.SetMode(model.ModeGlobal) }()
	time.Sleep(50 * time.Millisecond)
	if len(modePatches()) != 0 || h.s.Settings().Mode != model.ModeRule {
		h.s.opMu.Unlock()
		t.Fatalf("SetMode ran beside an operation: patches %v, stored mode %s", modePatches(), h.s.Settings().Mode)
	}
	h.s.opMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := modePatches(); !slices.Equal(got, []string{"global"}) {
		t.Fatalf("mode patches = %v", got)
	}
	if h.s.Settings().Mode != model.ModeGlobal || h.s.Status().Mode != model.ModeGlobal {
		t.Fatalf("mode after SetMode: settings %s, status %s", h.s.Settings().Mode, h.s.Status().Mode)
	}
	// What a reload composes now is the mode the core was patched to.
	h.s.opMu.Lock()
	err := h.s.reloadLocked()
	h.s.opMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.prof.mu.Lock()
	composed := h.prof.inputs[len(h.prof.inputs)-1].Managed.Mode
	h.prof.mu.Unlock()
	if composed != model.ModeGlobal {
		t.Fatalf("reload composed mode %s", composed)
	}

	// The patch fails: settings and status keep the core's mode.
	ctrl.fail.Store(true)
	if err := h.s.SetMode(model.ModeDirect); err == nil {
		t.Fatal("SetMode succeeded although the patch failed")
	}
	if h.s.Settings().Mode != model.ModeGlobal || h.s.Status().Mode != model.ModeGlobal {
		t.Fatalf("mode after a failed patch: settings %s, status %s", h.s.Settings().Mode, h.s.Status().Mode)
	}
	saved, err := loadSettings(h.dir)
	if err != nil || saved.Mode != model.ModeGlobal {
		t.Fatalf("stored mode after a failed patch = %s (%v)", saved.Mode, err)
	}
}

// A failing flush is logged, once per distinct error, and so is the
// recovery (review item 7).
func TestStatsFlushFailureIsLoggedOnce(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "stats") // a file where the directory should be
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 4, 10, 0, 0, 0, time.Local)
	st := newStatsStore(blocker, func() time.Time { return now }, nil)
	var mu sync.Mutex
	var lines []string
	st.logf = func(format string, _ ...any) {
		mu.Lock()
		lines = append(lines, format)
		mu.Unlock()
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(lines) }
	st.record([]model.Connection{{ID: "a", Host: "example.com:443", Upload: 10, Download: 20}}, now)

	for i := 0; i < 3; i++ {
		if err := st.flushLogged(); err == nil {
			t.Fatal("flush into a file's place succeeded")
		}
	}
	if count() != 1 {
		t.Fatalf("a repeating flush error was logged %d times, want once: %v", count(), lines)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := st.flushLogged(); err != nil {
		t.Fatalf("flush after the directory became writable: %v", err)
	}
	if count() != 2 || !strings.Contains(lines[1], "works again") {
		t.Fatalf("recovery not logged: %v", lines)
	}
	if err := st.flushLogged(); err != nil || count() != 2 {
		t.Fatalf("a clean flush logged again: %v %v", err, lines)
	}
	if files, _ := filepath.Glob(filepath.Join(blocker, "*")); len(files) == 0 {
		t.Fatal("the retried flush wrote nothing: the failed day was lost")
	}
}

// Shutdown writes what the connection stream recorded after the
// statistics loop's last flush (review item 7).
func TestShutdownFlushesStats(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.onConnections(controller.Connections{Connections: []controller.Connection{
		{ID: "c1", Upload: 100, Download: 200, Chains: []string{"东京 03", "节点选择"}},
	}})
	dir := filepath.Join(h.dir, "stats")
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Fatalf("statistics already on disk before Shutdown: %v", files)
	}
	h.s.Shutdown()
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) == 0 {
		t.Fatal("Shutdown did not write the recorded statistics")
	}
}

// Restoring the selections stops at the first call the context ended
// (Shutdown, or a controller that does not answer) instead of waiting out
// every group (review item 9).
func TestRestoreSelectionsGivesUpOnceCancelled(t *testing.T) {
	var ctrl *cancelCtrl
	h := newHarness(t, nil)
	ctrl = &cancelCtrl{ctrlClient: h.s.d.ctrl}
	h.s.d.ctrl = ctrl
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stopMainProbe(h)
	h.s.rememberSelection("节点选择", "香港 01")
	h.s.rememberSelection("流媒体", "香港 01")

	ctrl.selects.Store(0)
	h.s.rootCancel() // Shutdown has begun
	h.s.restoreSelections()
	if got := ctrl.selects.Load(); got != 1 {
		t.Fatalf("SelectProxy calls after the cancel = %d, want 1 (give up at the first)", got)
	}
}

// The PAC server lives only while something points at it (review item 10).
func TestPACServerStopsWithItsUsers(t *testing.T) {
	for _, tc := range []struct {
		name string
		off  func(h *harness) error
	}{
		{"system proxy off", func(h *harness) error { return h.s.SetSystemProxy(false) }},
		{"core stopped", func(h *harness) error { return h.s.StopCore() }},
		{"PAC mode off", func(h *harness) error {
			set := h.s.Settings()
			set.Advanced.PACMode = false
			return h.s.SaveSettings(set)
		}},
		{"restart failed", func(h *harness) error {
			setStartErr(h, errors.New("boom"))
			if err := h.s.RestartCore(); err == nil {
				return errors.New("restart succeeded")
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := pacHarness(t)
			if err := h.s.SetSystemProxy(true); err != nil {
				t.Fatal(err)
			}
			url := h.s.pacURL()
			if url == "" {
				t.Fatal("setup: no PAC server")
			}
			if err := tc.off(h); err != nil {
				t.Fatal(err)
			}
			if got := h.s.pacURL(); got != "" {
				t.Fatalf("the PAC server still listens at %s", got)
			}
			addr := strings.TrimSuffix(strings.TrimPrefix(url, "http://"), pacPath)
			if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
				c.Close()
				t.Fatalf("%s still accepts connections", addr)
			}
		})
	}
}

// The waiting notices are bounded and hold no exact duplicates (item 11).
func TestQueueNotice(t *testing.T) {
	dns := Notice{Kind: "error", Title: "未能恢复系统 DNS", Detail: "d"}
	recovery := Notice{Kind: "recovery", Title: "上次澜流未正常退出"}
	numbered := func(i int) Notice { return Notice{Kind: "error", Title: "t", Detail: strings.Repeat("x", i+1)} }

	t.Run("duplicate", func(t *testing.T) {
		var q []Notice
		for i := 0; i < 5; i++ {
			q = queueNotice(q, dns)
		}
		q = queueNotice(q, numbered(0))
		q = queueNotice(q, dns)
		if len(q) != 2 || q[0] != numbered(0) || q[1] != dns {
			t.Fatalf("queue = %+v, want the other notice then the repeated one once", q)
		}
	})
	t.Run("same title, other detail", func(t *testing.T) {
		q := queueNotice(queueNotice(nil, numbered(0)), numbered(1))
		if len(q) != 2 {
			t.Fatalf("notices that differ were merged: %+v", q)
		}
	})
	t.Run("cap", func(t *testing.T) {
		q := []Notice{recovery}
		for i := 0; i < maxNotices+20; i++ {
			q = queueNotice(q, numbered(i))
		}
		if len(q) != maxNotices {
			t.Fatalf("len = %d, want %d", len(q), maxNotices)
		}
		if q[0] != recovery {
			t.Fatalf("the recovery notice was dropped: first = %+v", q[0])
		}
		if q[len(q)-1] != numbered(maxNotices+19) || q[1] != numbered(21) {
			t.Fatalf("kept the wrong end: second = %d chars, last = %d chars", len(q[1].Detail), len(q[len(q)-1].Detail))
		}
	})
}

// A reload drops the 检测 page's tables of nodes that are gone (item 11).
func TestPruneExitsDropsProbeTablesOfGoneNodes(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stopMainProbe(h)
	h.s.mu.Lock()
	h.s.state.probe.ip = map[string]model.IPReport{
		"香港 01": {}, "旧节点": {}, "检测中的旧节点": {Running: true}, model.ProbeDirect: {},
	}
	h.s.state.probe.unlock = map[string]model.UnlockReport{
		"香港 01": {}, "旧节点": {}, "检测中的旧节点": {Running: true}, model.ProbeDirect: {},
	}
	h.s.state.probe.regionCache = map[string]string{"香港 01": "HK", "旧节点": "JP"}
	h.s.mu.Unlock()

	h.s.pruneExits()

	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	keys := func(n int, has func(string) bool) {
		t.Helper()
		for _, k := range []string{"香港 01", "检测中的旧节点", model.ProbeDirect} {
			if !has(k) {
				t.Fatalf("%q was dropped", k)
			}
		}
		if has("旧节点") || n != 3 {
			t.Fatalf("the gone node was kept (%d entries)", n)
		}
	}
	keys(len(h.s.state.probe.ip), func(k string) bool { _, ok := h.s.state.probe.ip[k]; return ok })
	keys(len(h.s.state.probe.unlock), func(k string) bool { _, ok := h.s.state.probe.unlock[k]; return ok })
	if _, ok := h.s.state.probe.regionCache["旧节点"]; ok || len(h.s.state.probe.regionCache) != 1 {
		t.Fatalf("regionCache = %v", h.s.state.probe.regionCache)
	}
}

// The table of subjects without an icon is bounded like the icons (item 11).
func TestProcIconFailuresAreBounded(t *testing.T) {
	h := newHarness(t, nil)
	h.s.d.fileIcon = func(string, int) ([]byte, error) { return nil, errors.New("no icon") }
	h.s.mu.Lock()
	c := h.s.procIconsLocked()
	h.s.mu.Unlock()
	missing := filepath.Join(h.dir, "missing")
	for i := 0; i < maxProcIconsInMem+10; i++ {
		h.s.loadProcIcon(c, missing+strings.Repeat("x", i))
	}
	c.mu.Lock()
	n := len(c.failed)
	c.mu.Unlock()
	if n > maxProcIconsInMem {
		t.Fatalf("failed holds %d subjects, cap %d", n, maxProcIconsInMem)
	}
}

// Out of sight the routes are not rebuilt with every connections frame;
// coming back rebuilds them once (review item 12).
func TestBackgroundSkipsRouteRebuilds(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stopMainProbe(h)
	mainConns := func() int {
		for _, r := range h.s.ExitRoutes() {
			if r.Main {
				return r.Connections
			}
		}
		return -1
	}
	if mainConns() != 0 {
		t.Fatalf("setup: main route has %d connections", mainConns())
	}
	frame := controller.Connections{Connections: []controller.Connection{
		{ID: "c1", Upload: 1, Download: 2, Chains: []string{"东京 03", "节点选择"}},
	}}
	h.s.SetBackground(true)
	h.s.onConnections(frame)
	if got := mainConns(); got != 0 {
		t.Fatalf("routes rebuilt in the background: main route has %d connections", got)
	}
	if n := len(h.s.Connections()); n != 1 {
		t.Fatalf("connections in the background = %d, want 1 (only the routes wait)", n)
	}
	h.s.SetBackground(false)
	if !waitFor(func() bool { return mainConns() == 1 }) {
		t.Fatalf("routes not rebuilt on the way back: main route has %d connections", mainConns())
	}
}
