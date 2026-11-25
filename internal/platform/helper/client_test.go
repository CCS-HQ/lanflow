package helper

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/platform/helper/protocol"
)

// fakeHelper is an in-process protocol server standing in for the daemon.
type fakeHelper struct {
	mu        sync.Mutex
	running   bool
	exitError string
	starts    int
	stops     int
	lastSpec  protocol.Spec
	refuse    string // when set, start is refused with this message
	stopped   chan struct{}
	listener  net.Listener
	dialErrOn bool
	stalled   chan struct{}                         // while non-nil and open, status answers wait for it
	version   string                                // ping version; "fake" when empty
	protocol  int                                   // ping protocol; protocol.Version when zero
	stage     func(protocol.Spec) protocol.Response // the stage op; nil = a helper from before it existed
	workDir   string                                // status work dir; empty = a helper from before 0.1.8
}

func startFakeHelper(t *testing.T) *fakeHelper {
	t.Helper()
	f := &fakeHelper{stopped: make(chan struct{})}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.listener = l
	ctx, cancel := context.WithCancel(context.Background())
	srv := &protocol.Server{Handler: f.handle}
	go func() { _ = srv.Serve(ctx, l); close(f.stopped) }()
	addr := l.Addr().String()
	old := dialHelper
	dialHelper = func(ctx context.Context) (net.Conn, error) {
		f.mu.Lock()
		dead := f.dialErrOn
		f.mu.Unlock()
		if dead {
			return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
	t.Cleanup(func() { cancel(); dialHelper = old })
	return f
}

func (f *fakeHelper) handle(_ context.Context, req protocol.Request) protocol.Response {
	f.mu.Lock()
	st := f.stalled
	f.mu.Unlock()
	if st != nil && req.Op == protocol.OpStatus {
		<-st
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Op {
	case protocol.OpPing:
		v, p := f.version, f.protocol
		if v == "" {
			v = "fake"
		}
		if p == 0 {
			p = protocol.Version
		}
		return protocol.Response{OK: true, Version: v, Protocol: p}
	case protocol.OpStart:
		if f.refuse != "" {
			return protocol.Response{Error: f.refuse}
		}
		f.starts++
		f.running = true
		f.exitError = ""
		f.lastSpec = *req.Spec
		return protocol.Response{OK: true, Running: true, PID: 7}
	case protocol.OpStop:
		f.stops++
		f.running = false
		return protocol.Response{OK: true}
	case protocol.OpStatus:
		return protocol.Response{OK: true, Running: f.running, ExitError: f.exitError, PID: 7, WorkDir: f.workDir}
	case protocol.OpStage:
		if f.stage != nil {
			return f.stage(*req.Spec)
		}
		// What helper 0.1.3 and older answer (service.NewHandler).
		return protocol.Response{Error: `未知操作: "stage"`}
	}
	return protocol.Response{Error: "unknown"}
}

func (f *fakeHelper) crash(msg string) {
	f.mu.Lock()
	f.running = false
	f.exitError = msg
	f.mu.Unlock()
}

func newTestRunner() *runner {
	r := newRunner().(*runner)
	r.pollInterval = 20 * time.Millisecond
	return r
}

func TestInstalledPing(t *testing.T) {
	ctx := context.Background()
	if installed(ctx) {
		t.Skip("a real helper answers on this machine")
	}
	f := startFakeHelper(t)
	if !installed(ctx) {
		t.Fatal("installed() false with a fake helper running")
	}
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	start := time.Now()
	if installed(ctx) {
		t.Fatal("installed() true when dial fails")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("installed() did not respect the 1 s timeout")
	}
}

func TestRunnerStartStopNoExit(t *testing.T) {
	f := startFakeHelper(t)
	r := newTestRunner()
	ctx := context.Background()
	spec := core.Spec{Bin: "/b", WorkDir: "/w", Config: "/c", Controller: "/s", LogFile: "/l", Env: []string{"X=1"}}
	if err := r.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if !r.Running() {
		t.Fatal("not running after start")
	}
	exited := r.Exited()
	if f.lastSpec.Env[0] != "X=1" || f.lastSpec.Controller != "/s" {
		t.Fatalf("spec not forwarded: %+v", f.lastSpec)
	}
	time.Sleep(5 * r.pollInterval) // several status polls while running
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if r.Running() {
		t.Fatal("running after stop")
	}
	select {
	case err := <-exited:
		t.Fatalf("Stop must not report an exit, got %v", err)
	case <-time.After(5 * r.pollInterval):
	}
	if f.stops != 1 || f.starts != 1 {
		t.Fatalf("starts=%d stops=%d", f.starts, f.stops)
	}
}

func TestRunnerExitedOnCrash(t *testing.T) {
	f := startFakeHelper(t)
	r := newTestRunner()
	ctx := context.Background()
	if err := r.Start(ctx, core.Spec{Bin: "/b"}); err != nil {
		t.Fatal(err)
	}
	exited := r.Exited()
	f.crash("Mihomo 意外退出: exit status 1")
	select {
	case err := <-exited:
		if err == nil || !strings.Contains(err.Error(), "exit status 1") {
			t.Fatalf("exit error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Exited not delivered")
	}
	if r.Running() {
		t.Fatal("still running after crash")
	}
	// Exactly one delivery; the channel is then quiet.
	select {
	case err := <-exited:
		t.Fatalf("second delivery: %v", err)
	case <-time.After(5 * r.pollInterval):
	}

	// A new Start replaces the channel; the old one stays quiet.
	if err := r.Start(ctx, core.Spec{Bin: "/b"}); err != nil {
		t.Fatal(err)
	}
	exited2 := r.Exited()
	if exited2 == exited {
		t.Fatal("Exited channel not replaced on Start")
	}
	f.crash("second")
	select {
	case err := <-exited2:
		if err == nil || err.Error() != "second" {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second exit not delivered")
	}
}

func TestRunnerExitedWhenHelperVanishes(t *testing.T) {
	f := startFakeHelper(t)
	r := newTestRunner()
	if err := r.Start(context.Background(), core.Spec{Bin: "/b"}); err != nil {
		t.Fatal(err)
	}
	exited := r.Exited()
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	select {
	case err := <-exited:
		if err == nil || !strings.Contains(err.Error(), "权限组件无响应") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unreachable helper not reported")
	}
}

// A helper that stalls (accepts, answers late) is waited out for the
// stall limit: the core is not reported gone while it runs on.
func TestRunnerWaitsOutAStalledHelper(t *testing.T) {
	f := startFakeHelper(t)
	r := newTestRunner()
	r.statusWait = 30 * time.Millisecond
	r.stall = 2 * time.Second
	if err := r.Start(context.Background(), core.Spec{Bin: "/b"}); err != nil {
		t.Fatal(err)
	}
	exited := r.Exited()
	stall := make(chan struct{})
	f.mu.Lock()
	f.stalled = stall
	f.mu.Unlock()
	select {
	case err := <-exited:
		t.Fatalf("stall of 0.5 s reported as exit: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	f.mu.Lock()
	f.stalled = nil
	f.mu.Unlock()
	close(stall)
	time.Sleep(100 * time.Millisecond)
	if !r.Running() {
		t.Fatal("not running after the helper answered again")
	}
	// a stall past the limit is reported
	stall = make(chan struct{})
	defer close(stall)
	f.mu.Lock()
	f.stalled = stall
	f.mu.Unlock()
	select {
	case err := <-exited:
		if err == nil || !strings.Contains(err.Error(), "权限组件无响应") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("a stall past the limit was not reported")
	}
}

func TestRunnerStartRefused(t *testing.T) {
	f := startFakeHelper(t)
	f.refuse = "权限组件拒绝运行未登记的内核: /b"
	r := newTestRunner()
	err := r.Start(context.Background(), core.Spec{Bin: "/b"})
	if err == nil || !strings.Contains(err.Error(), "未登记") {
		t.Fatalf("err = %v", err)
	}
	if r.Running() {
		t.Fatal("running after refused start")
	}
}

func TestRunnerStartWithoutHelper(t *testing.T) {
	f := startFakeHelper(t)
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	r := newTestRunner()
	err := r.Start(context.Background(), core.Spec{Bin: "/b"})
	if err == nil || !strings.Contains(err.Error(), "无法连接权限组件") {
		t.Fatalf("err = %v", err)
	}
}

func TestExitedBeforeStartIsQuiet(t *testing.T) {
	r := newTestRunner()
	ch := r.Exited()
	if ch == nil {
		t.Fatal("Exited returned nil before Start")
	}
	select {
	case v := <-ch:
		t.Fatalf("unexpected value %v", v)
	default:
	}
}

func TestStopWithoutHelperIsNoop(t *testing.T) {
	f := startFakeHelper(t)
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	r := newTestRunner()
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on an idle runner without a helper: %v", err)
	}
}

func setExpected(t *testing.T, v string) {
	t.Helper()
	learnedMu.Lock()
	old := learnedVersion
	learnedVersion = v
	learnedMu.Unlock()
	t.Cleanup(func() { learnedMu.Lock(); learnedVersion = old; learnedMu.Unlock() })
}

func TestCheckVersions(t *testing.T) {
	ctx := context.Background()
	if reachable(ctx) {
		t.Skip("a real helper answers on this machine")
	}
	if err := check(ctx); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("no helper: %v", err)
	}
	f := startFakeHelper(t)
	f.mu.Lock()
	f.version = "1.2.0"
	f.mu.Unlock()

	setExpected(t, "1.2.0")
	if err := check(ctx); err != nil || !installed(ctx) {
		t.Fatalf("same version: %v", err)
	}
	setExpected(t, "1.3.0")
	err := check(ctx)
	var ve *VersionError
	if !errors.Is(err, ErrVersionMismatch) || !errors.As(err, &ve) || ve.Installed != "1.2.0" || ve.Bundled != "1.3.0" {
		t.Fatalf("mismatch: %v", err)
	}
	if installed(ctx) {
		t.Fatal("installed() true for a mismatched version")
	}
	if !reachable(ctx) {
		t.Fatal("reachable() false for a running helper")
	}
	// dev builds and an unknown expectation compare only the protocol.
	setExpected(t, "")
	if err := check(ctx); err != nil {
		t.Fatalf("unknown expectation: %v", err)
	}
	setExpected(t, "1.3.0")
	f.mu.Lock()
	f.version = "dev"
	f.mu.Unlock()
	if err := check(ctx); err != nil {
		t.Fatalf("dev helper: %v", err)
	}
	f.mu.Lock()
	f.protocol = protocol.Version + 1
	f.mu.Unlock()
	if err := check(ctx); !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("protocol: %v", err)
	}
}

func TestCheckDowngrade(t *testing.T) {
	ctx := context.Background()
	if reachable(ctx) {
		t.Skip("a real helper answers on this machine")
	}
	setExpected(t, "")
	old := helperVersionOf
	t.Cleanup(func() { helperVersionOf = old })
	bundled := "1.0.0"
	helperVersionOf = func(context.Context, string) (string, error) { return bundled, nil }

	// No helper running: nothing to downgrade.
	if err := checkDowngrade(ctx, "/x"); err != nil {
		t.Fatal(err)
	}
	if expectedVersion() != "1.0.0" {
		t.Fatalf("bundled version not learned: %q", expectedVersion())
	}
	f := startFakeHelper(t)
	f.mu.Lock()
	f.version = "1.1.0"
	f.mu.Unlock()
	if err := checkDowngrade(ctx, "/x"); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgrade allowed: %v", err)
	}
	bundled = "1.1.0"
	if err := checkDowngrade(ctx, "/x"); err != nil {
		t.Fatalf("same version refused: %v", err)
	}
	bundled = "1.2.0"
	if err := checkDowngrade(ctx, "/x"); err != nil {
		t.Fatalf("upgrade refused: %v", err)
	}
	// Unreadable bundled version does not block the install.
	helperVersionOf = func(context.Context, string) (string, error) { return "", errors.New("boom") }
	if err := checkDowngrade(ctx, "/x"); err != nil {
		t.Fatal(err)
	}
}

// The helper's core runs on the helper's copy of the config, so a reload
// asks for a new copy and gets its path (core.ConfigStager).
func TestRunnerStageConfig(t *testing.T) {
	f := startFakeHelper(t)
	var r core.Runner = newTestRunner()
	st, ok := r.(core.ConfigStager)
	if !ok {
		t.Fatal("the helper's runner is not a core.ConfigStager")
	}
	ctx := context.Background()
	spec := core.Spec{WorkDir: "/data/core", Config: "/data/core/runtime.yaml"}

	// A helper from before the op existed (still running a core the app
	// took over after an upgrade) says so, and the app reloads the way
	// that core expects.
	if path, err := st.StageConfig(ctx, spec); !errors.Is(err, core.ErrStageUnsupported) {
		t.Fatalf("an old helper: %q, %v", path, err)
	}

	var got protocol.Spec
	f.mu.Lock()
	f.stage = func(s protocol.Spec) protocol.Response {
		got = s
		return protocol.Response{OK: true, Running: true, ConfigPath: "/helper/work/runtime.yaml"}
	}
	f.mu.Unlock()
	path, err := st.StageConfig(ctx, spec)
	if err != nil || path != "/helper/work/runtime.yaml" {
		t.Fatalf("StageConfig = %q, %v", path, err)
	}
	if got.Config != spec.Config || got.WorkDir != spec.WorkDir {
		t.Fatalf("spec sent: %+v", got)
	}

	// A refusal is the helper's own message.
	f.mu.Lock()
	f.stage = func(protocol.Spec) protocol.Response {
		return protocol.Response{Error: "权限组件拒绝了这份配置"}
	}
	f.mu.Unlock()
	if _, err := st.StageConfig(ctx, spec); err == nil || !strings.Contains(err.Error(), "拒绝了这份配置") || errors.Is(err, core.ErrStageUnsupported) {
		t.Fatalf("refusal: %v", err)
	}
	// An answer without a path is not a path.
	f.mu.Lock()
	f.stage = func(protocol.Spec) protocol.Response { return protocol.Response{OK: true} }
	f.mu.Unlock()
	if path, err := st.StageConfig(ctx, spec); err == nil {
		t.Fatalf("empty path accepted: %q", path)
	}
	// An unreachable helper reads as such.
	f.mu.Lock()
	f.dialErrOn = true
	f.mu.Unlock()
	if _, err := st.StageConfig(ctx, spec); err == nil || !strings.Contains(err.Error(), "无法连接权限组件") || errors.Is(err, core.ErrStageUnsupported) {
		t.Fatalf("unreachable helper: %v", err)
	}
}

// A core the helper runs for another data directory is not ours: it is
// neither reported as running for us nor adopted (a test or a second
// install must not take the user's TUN core away).
func TestCoreOfAnotherWorkDirIsNotOurs(t *testing.T) {
	f := startFakeHelper(t)
	ours, theirs := t.TempDir(), t.TempDir()
	resolved := func(dir string) string {
		r, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f.mu.Lock()
	f.running, f.workDir = true, resolved(theirs)
	f.mu.Unlock()
	ctx := context.Background()
	if running, err := CoreRunning(ctx, ours); err != nil || running {
		t.Fatalf("CoreRunning for another work dir = %v, %v; want false", running, err)
	}
	if err := newRunner().(core.Adopter).Adopt(ctx, core.Spec{WorkDir: ours}); !errors.Is(err, core.ErrNothingToAdopt) {
		t.Fatalf("Adopt of another work dir's core: %v, want ErrNothingToAdopt", err)
	}
	// The helper reports the directory with its symlinks resolved.
	f.mu.Lock()
	f.workDir = resolved(ours)
	f.mu.Unlock()
	if running, err := CoreRunning(ctx, ours); err != nil || !running {
		t.Fatalf("CoreRunning for our work dir = %v, %v; want true", running, err)
	}
	// A helper from before 0.1.8 does not say: as before, the core is ours.
	f.mu.Lock()
	f.workDir = ""
	f.mu.Unlock()
	if running, err := CoreRunning(ctx, ours); err != nil || !running {
		t.Fatalf("CoreRunning with an old helper = %v, %v; want true", running, err)
	}
}

func TestDisableMakesTheHelperUnreachable(t *testing.T) {
	f := startFakeHelper(t)
	f.mu.Lock()
	f.running = true
	f.mu.Unlock()
	restore := Disable()
	if running, err := CoreRunning(context.Background(), ""); err == nil || running {
		t.Fatalf("CoreRunning while disabled = %v, %v; want an error", running, err)
	}
	if Installed(context.Background()) {
		t.Fatal("Installed while disabled")
	}
	restore()
	if running, err := CoreRunning(context.Background(), ""); err != nil || !running {
		t.Fatalf("CoreRunning after restore = %v, %v; want true", running, err)
	}
}
