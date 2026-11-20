package settings

import (
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env/envtest"
)

// The tests here run the page's jobs on goroutines of their own
// (envtest.Async), as the shell does in the app; every other test of the
// package runs them on the calling thread. Run them with -race.

// gated is the fake with calls that wait for the test: a call of a gated
// method blocks until release lets one through.
type gated struct {
	*fake.Fake

	mu       sync.Mutex
	cond     *sync.Cond
	started  map[string]int
	allowed  map[string]int          // calls up to this number may go
	only     map[string]map[int]bool // and these, whatever their number
	inFlight int
	maxSaves int
	saves    []model.Settings // as SaveSettings got them, in order
}

func newGated() *gated {
	g := &gated{Fake: fake.NewInstant(), started: map[string]int{}, allowed: map[string]int{}, only: map[string]map[int]bool{}}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// enter blocks the call of name until release allows it; it returns the
// call's number, from 1.
func (g *gated) enter(name string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.started[name]++
	n := g.started[name]
	g.cond.Broadcast()
	for g.allowed[name] < n && !g.only[name][n] {
		g.cond.Wait()
	}
	return n
}

// releaseOnly lets the n-th call of name through, not those before it.
func (g *gated) releaseOnly(name string, n int) {
	g.mu.Lock()
	if g.only[name] == nil {
		g.only[name] = map[int]bool{}
	}
	g.only[name][n] = true
	g.cond.Broadcast()
	g.mu.Unlock()
}

// release lets the n-th call of name through (and those before it).
func (g *gated) release(name string, n int) {
	g.mu.Lock()
	g.allowed[name] = max(g.allowed[name], n)
	g.cond.Broadcast()
	g.mu.Unlock()
}

// waitStarted blocks until the n-th call of name has begun.
func (g *gated) waitStarted(t *testing.T, name string, n int) {
	t.Helper()
	timer := time.AfterFunc(10*time.Second, func() {
		g.mu.Lock()
		g.started[name] = -1
		g.cond.Broadcast()
		g.mu.Unlock()
	})
	defer timer.Stop()
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.started[name] >= 0 && g.started[name] < n {
		g.cond.Wait()
	}
	if g.started[name] < 0 {
		t.Fatalf("call %d of %s never started", n, name)
	}
}

func (g *gated) SaveSettings(s model.Settings) error {
	g.mu.Lock()
	g.inFlight++
	g.maxSaves = max(g.maxSaves, g.inFlight)
	g.saves = append(g.saves, s)
	g.mu.Unlock()
	g.enter("save")
	err := g.Fake.SaveSettings(s)
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
	return err
}

func (g *gated) PreviewOverride(key, text string) ([]model.DiffLine, error) {
	g.enter("preview")
	return g.Fake.PreviewOverride(key, text)
}

func (g *gated) RuntimeConfig() ([]model.RuntimeLine, error) {
	n := g.enter("runtime")
	return []model.RuntimeLine{{Text: "call: " + string(rune('0'+n))}}, nil
}

func asyncPage(t *testing.T, g *gated) (*Page, *envtest.Async, *ui.Tester, *rec) {
	t.Helper()
	e, r := testEnv(g.Fake)
	e.API = g
	a := envtest.NewAsync(e)
	p := New(e)
	tt := ui.NewTester(a.View(p.View), 1000, 800)
	return p, a, tt, r
}

// TestSavesRunOneAtATimeAndKeepLaterChanges: settings are saved as whole
// snapshots, so a change made while another is being saved must contain
// it, be saved after it, and the draft must stay until the last save is
// done. Before, each save ran on its own goroutine and the first to
// finish cleared the draft: the controls fell back to the service's
// settings, and the next change was built on a snapshot without the one
// still in flight, which it then overwrote.
func TestSavesRunOneAtATimeAndKeepLaterChanges(t *testing.T) {
	g := newGated()
	p, a, tt, r := asyncPage(t, g)

	// A: saving starts and waits.
	s := p.current()
	s.OpenAtLogin = true
	p.save(s, "", nil)
	g.waitStarted(t, "save", 1)

	// B, while A is in flight: built on the draft, queued behind A.
	s = p.current()
	if !s.OpenAtLogin {
		t.Fatal("the draft does not show A while it is being saved")
	}
	s.LogLevel = "debug"
	p.save(s, "", nil)
	tt.Frame()

	// A finishes. The draft stays (B is not saved yet) and B starts.
	g.release("save", 1)
	a.WaitFinished(1)
	tt.Frame()
	if p.draft == nil {
		t.Fatal("the draft was dropped while a save was still queued")
	}
	if cur := p.current(); !cur.OpenAtLogin || cur.LogLevel != "debug" {
		t.Fatalf("the page shows %+v, want A and B", cur)
	}
	if !p.saving {
		t.Fatal("the switches should wait while a save is queued")
	}
	g.waitStarted(t, "save", 2)

	// C, while B is in flight: it must contain A and B.
	s = p.current()
	s.SilentStart = true
	p.save(s, "已保存 C", nil)

	g.release("save", 3)
	a.Settle(tt.Frame)

	got := g.Fake.Settings()
	if !got.OpenAtLogin || got.LogLevel != "debug" || !got.SilentStart {
		t.Fatalf("saved settings lost a change: OpenAtLogin %v, LogLevel %q, SilentStart %v", got.OpenAtLogin, got.LogLevel, got.SilentStart)
	}
	g.mu.Lock()
	maxSaves, n := g.maxSaves, len(g.saves)
	g.mu.Unlock()
	if maxSaves != 1 || n != 3 {
		t.Fatalf("%d saves, at most %d at once; want 3, one at a time", n, maxSaves)
	}
	if p.draft != nil || len(p.saveQ) != 0 || p.saveBusy {
		t.Fatalf("the queue is not empty after the last save: draft %v, %d queued", p.draft != nil, len(p.saveQ))
	}
	if len(r.toasts) != 1 || r.toasts[0] != "已保存 C" {
		t.Fatalf("toasts: %q", r.toasts)
	}
}

// TestSaveErrorReachesOnErrOnTheUIThread: a refused port shows under its
// field, written by the UI thread (the job used to write it under a lock
// the view did not take).
func TestSaveErrorReachesOnErrOnTheUIThread(t *testing.T) {
	g := newGated()
	p, a, tt, r := asyncPage(t, g)
	g.release("save", 100)
	s := p.current()
	p.port.text = "7891" // the fake refuses 7891 as taken
	p.commitPort(s)
	for !a.Idle() {
		tt.Frame() // the view reads portErr while the job runs
	}
	tt.Frame()
	if p.portErr == "" || g.Fake.Settings().MixedPort != 7890 {
		t.Fatalf("port error %q, port %d", p.portErr, g.Fake.Settings().MixedPort)
	}
	if len(r.toasts) != 0 {
		t.Fatalf("the error belongs under the field, not in a toast: %q", r.toasts)
	}
}

// TestLatePreviewDoesNotReachAnotherOverride: 预览差异 of one override,
// 取消 while it is computed, another override opened: the late result
// must not show its diff in the editor of the other one.
func TestLatePreviewDoesNotReachAnotherOverride(t *testing.T) {
	g := newGated()
	p, a, tt, _ := asyncPage(t, g)
	subID := g.Fake.Subscriptions()[0].ID

	p.requestTab(tabOverride)
	p.openOverride("global")
	tt.Frame()
	p.SetOverrideText("log-level: debug\n")
	p.previewOverride()
	g.waitStarted(t, "preview", 1)
	tt.Frame()
	if !p.ov.previewing {
		t.Fatal("not previewing")
	}

	p.ov.open = false // 取消: nothing was edited against the saved text here
	tt.Frame()
	p.openOverride(subID)
	tt.Frame()

	g.release("preview", 1)
	a.Settle(tt.Frame)
	if p.ov.key != subID || p.ov.showDiff || p.ov.diff != nil || p.ov.previewing || p.ov.err != "" {
		t.Fatalf("the preview of the global override reached %q: showDiff %v, diff %v, previewing %v", p.ov.key, p.ov.showDiff, p.ov.diff != nil, p.ov.previewing)
	}

	// The preview of the editor that is open still arrives.
	p.SetOverrideText("mode: direct\n")
	p.previewOverride()
	g.release("preview", 2)
	a.Settle(tt.Frame)
	if !p.ov.showDiff || p.ov.diff == nil {
		t.Fatal("the open editor's own preview did not arrive")
	}
}

// TestLateRuntimeConfigIsDropped: the runtime config asked for by an
// earlier opening of the dialog does not replace the later one's.
func TestLateRuntimeConfigIsDropped(t *testing.T) {
	g := newGated()
	p, a, tt, _ := asyncPage(t, g)
	p.requestTab(tabOverride)
	p.openRuntime()
	g.waitStarted(t, "runtime", 1)
	tt.Frame()
	p.rt.open = false
	tt.Frame()
	p.openRuntime()
	g.waitStarted(t, "runtime", 2)

	// The second call answers first, then the first, late.
	g.releaseOnly("runtime", 2)
	a.WaitFinished(1)
	tt.Frame()
	g.release("runtime", 1)
	a.Settle(tt.Frame)
	if p.rt.loading || len(p.rt.lines) != 1 || p.rt.lines[0].Text != "call: 2" {
		t.Fatalf("runtime dialog shows %+v (loading %v), want the second call's lines", p.rt.lines, p.rt.loading)
	}
	if !tt.HasText("call: 2") {
		t.Fatalf("lines not drawn: %q", tt.Texts())
	}
}

// TestJobsWriteStateOnTheUIThread starts the page's background jobs and
// keeps building frames while they finish. With -race it fails when a
// job writes a field the view reads; it also checks each result lands.
func TestJobsWriteStateOnTheUIThread(t *testing.T) {
	g := newGated()
	p, a, tt, _ := asyncPage(t, g)
	for _, name := range []string{"save", "preview", "runtime"} {
		g.release(name, 1000)
	}
	frames := func() {
		for !a.Idle() {
			tt.Frame()
		}
		tt.Frame()
	}

	// 常规: cache sizes, the WebDAV list.
	tt.Frame()
	frames()
	if !p.caches.loaded || p.caches.items == nil {
		t.Fatal("cache sizes not read")
	}
	p.f2.davListOpen = true
	p.davListJob("list", nil, "")
	frames()
	if p.f2.davBusy != "" {
		t.Fatalf("dav still busy: %q", p.f2.davBusy)
	}

	// 内核: update check, release list, mirror test.
	p.requestTab(tabKernel)
	p.checkUpdate("")
	p.cores.installOpen = true
	p.loadReleases(model.CoreChannelStable, false)
	p.testMirrors()
	frames()
	if p.checking || p.cores.loading || p.f2.mirrorTesting {
		t.Fatalf("kernel jobs not finished: checking %v, loading %v, testing %v", p.checking, p.cores.loading, p.f2.mirrorTesting)
	}
	if len(p.cores.releases[model.CoreChannelStable]) == 0 || len(p.f2.mirrorResults) == 0 {
		t.Fatal("release list or mirror results missing")
	}
	p.cores.installOpen = false

	// 覆写: runtime config, preview, save.
	p.requestTab(tabOverride)
	p.openRuntime()
	frames()
	if len(p.rt.lines) == 0 {
		t.Fatal("runtime config missing")
	}
	p.rt.open = false
	p.openOverride("global")
	p.SetOverrideText("log-level: debug\n")
	p.previewOverride()
	frames()
	if !p.ov.showDiff {
		t.Fatalf("no diff: %q", p.ov.err)
	}
	p.ov.showDiff = false
	p.saveOverride()
	frames()
	if p.ov.open || p.ov.saving {
		t.Fatalf("override not saved: %q", p.ov.err)
	}

	// DNS / 嗅探 / 高级 apply.
	p.requestTab(tabSniffer)
	tt.Frame()
	p.snf.httpPorts = "8080"
	if !p.snfDirty() {
		t.Fatal("the sniffer form should be dirty")
	}
	p.applySniffer()
	frames()
	if p.applying || p.snfDirty() {
		t.Fatal("sniffer settings not applied")
	}
}

// TestRevealLabelPerSystem: Linux is not Windows.
func TestRevealLabelPerSystem(t *testing.T) {
	for _, tc := range []struct {
		mac  bool
		goos string
		want string
	}{
		{true, "darwin", "在访达中显示"},
		{false, "windows", "在资源管理器中显示"},
		{false, "linux", "在文件管理器中显示"},
		{false, "freebsd", "在文件管理器中显示"},
	} {
		if got := revealLabelFor(tc.mac, tc.goos); got != tc.want {
			t.Errorf("mac %v, %s: %q, want %q", tc.mac, tc.goos, got, tc.want)
		}
	}
}

// TestNotMacSmoke: the page builds with the labels of the other systems.
func TestNotMacSmoke(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	e.Mac = false
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	if !tt.HasText("隐藏到托盘") && !tt.HasText("通知区域") {
		// 关闭窗口时's value and 启动时不显示窗口's description
		t.Fatalf("no non-Mac wording: %q", tt.Texts())
	}
	if tt.HasText("菜单栏显示") {
		t.Fatalf("the macOS-only section shows: %q", tt.Texts())
	}
	for _, tab := range []int{tabProxy, tabDNS, tabSniffer, tabOverride, tabKernel, tabAdvanced} {
		p.requestTab(tab)
		tt.Frame()
	}
}
