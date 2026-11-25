//go:build darwin

package service

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/platform/helper/protocol"
)

// fakeProcess is a Process controlled by the test.
type fakeProcess struct {
	pid             int
	ignoreInterrupt bool
	exit            chan error
	once            sync.Once
	mu              sync.Mutex
	signals         []string
}

func (p *fakeProcess) Pid() int    { return p.pid }
func (p *fakeProcess) Wait() error { return <-p.exit }
func (p *fakeProcess) Interrupt() error {
	p.record("interrupt")
	if !p.ignoreInterrupt {
		p.finish(nil)
	}
	return nil
}
func (p *fakeProcess) Kill() error {
	p.record("kill")
	p.finish(errors.New("killed"))
	return nil
}
func (p *fakeProcess) record(sig string) {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	p.mu.Unlock()
}
func (p *fakeProcess) finish(err error) {
	p.once.Do(func() { p.exit <- err; close(p.exit) })
}
func (p *fakeProcess) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.signals...)
}

// fakeStarter records commands and hands out fake processes.
type fakeStarter struct {
	mu              sync.Mutex
	last            Command
	procs           []*fakeProcess
	failed          error
	ignoreInterrupt bool
}

func (f *fakeStarter) start(_ context.Context, c Command) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed != nil {
		return nil, f.failed
	}
	f.last = c
	p := &fakeProcess{pid: 1000 + len(f.procs), exit: make(chan error, 1), ignoreInterrupt: f.ignoreInterrupt}
	f.procs = append(f.procs, p)
	return p, nil
}

func newTestSupervisor(t *testing.T) (*Supervisor, *fixture, *fakeStarter) {
	t.Helper()
	f := newFixture(t)
	fs := &fakeStarter{}
	s := NewSupervisor(f.policy)
	s.Start = fs.start
	s.Hooks = Hooks{} // no chown/socket polling in tests
	s.Logf = t.Logf
	// The helper's own work directory, outside the user's data directory.
	s.WorkDir = filepath.Join(resolvedTempDir(t), "helper-work")
	return s, f, fs
}

func TestSupervisorLifecycle(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	ctx := context.Background()
	if s.Running() {
		t.Fatal("running before start")
	}
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Running || st.PID != 1000 || st.ExitError != "" {
		t.Fatalf("status after start: %+v", st)
	}
	// Command line matches internal/core's flags, but the core runs in the
	// helper's own directory on the copy of the config there: neither the
	// user's work dir nor the user's config is named.
	args := strings.Join(fs.last.Args, " ")
	staged := filepath.Join(s.WorkDir, stagedConfigName)
	resolvedCtl, _ := filepath.EvalSymlinks(filepath.Dir(f.spec.Controller))
	want := "-d " + s.WorkDir + " -f " + staged + " -ext-ctl-unix " + filepath.Join(resolvedCtl, filepath.Base(f.spec.Controller))
	if args != want || fs.last.Path != f.core || fs.last.Dir != s.WorkDir {
		t.Fatalf("command = %s %s (dir %s), want %s", fs.last.Path, args, fs.last.Dir, want)
	}
	if got, err := os.ReadFile(staged); err != nil || string(got) != "mixed-port: 7890\n" {
		t.Fatalf("staged config = %q, %v", got, err)
	}
	if fi, err := os.Stat(s.WorkDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("work dir mode: %v %v", fi, err)
	}
	if fi, err := os.Stat(staged); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("staged config mode: %v %v", fi, err)
	}
	for _, kv := range fs.last.Env {
		if strings.HasPrefix(kv, "PATH=") && strings.Contains(kv, os.Getenv("HOME")) {
			t.Fatalf("helper leaked its own environment: %s", kv)
		}
		if strings.HasPrefix(kv, "HOME=") && kv != "HOME="+s.WorkDir {
			t.Fatalf("core HOME = %s, want the helper's work dir", kv)
		}
	}
	// Only one at a time.
	if err := s.StartCore(ctx, f.spec); err == nil {
		t.Fatal("second start accepted")
	}
	// Log file was created inside the data dir.
	if _, err := os.Stat(f.spec.LogFile); err != nil {
		t.Fatalf("log file: %v", err)
	}
	if _, err := io.WriteString(fs.last.Output, "hello\n"); err != nil {
		t.Fatal(err)
	}

	if err := s.StopCore(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Running() {
		t.Fatal("running after stop")
	}
	st = s.Status()
	if st.Running || st.ExitError != "" {
		t.Fatalf("status after stop: %+v (stop must not count as unexpected exit)", st)
	}
	if got := fs.procs[0].sent(); len(got) != 1 || got[0] != "interrupt" {
		t.Fatalf("signals = %v, want graceful interrupt only", got)
	}
	// Stop when nothing runs is a no-op.
	if err := s.StopCore(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.spec.LogFile)
	if !strings.Contains(string(b), "hello") {
		t.Fatalf("log content: %q", b)
	}
}

func TestSupervisorUnexpectedExit(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	ctx := context.Background()
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	fs.procs[0].finish(errors.New("exit status 1"))
	deadline := time.Now().Add(2 * time.Second)
	for s.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := s.Status()
	if st.Running || !strings.Contains(st.ExitError, "意外退出") || !strings.Contains(st.ExitError, "exit status 1") {
		t.Fatalf("status: %+v", st)
	}
	// A new start clears the exit error.
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); !st.Running || st.ExitError != "" || st.PID != 1001 {
		t.Fatalf("status after restart: %+v", st)
	}
	// Status names the user's work directory of the run, so a Lanflow of
	// another data directory can tell the core is not its own.
	if st := s.Status(); st.WorkDir == "" || filepath.Base(st.WorkDir) != filepath.Base(f.spec.WorkDir) {
		t.Fatalf("status work dir = %q, spec %q", st.WorkDir, f.spec.WorkDir)
	}
	_ = s.StopCore(ctx)
	if st := s.Status(); st.Running || st.WorkDir != "" {
		t.Fatalf("status after stop: %+v", st)
	}
}

func TestSupervisorRefusesInvalidSpec(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	bad := f.spec
	bad.Bin = "/usr/bin/true"
	if err := s.StartCore(context.Background(), bad); err == nil {
		t.Fatal("invalid spec accepted")
	}
	if len(fs.procs) != 0 {
		t.Fatal("process spawned despite refusal")
	}
	if s.Running() {
		t.Fatal("running after refusal")
	}
}

func TestSupervisorStartFailure(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	fs.failed = errors.New("exec format error")
	err := s.StartCore(context.Background(), f.spec)
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("err = %v", err)
	}
	if s.Running() {
		t.Fatal("running after failed start")
	}
}

func TestSupervisorKillsAfterGrace(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	fs.ignoreInterrupt = true
	ctx := context.Background()
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	// Use a short context so the test does not wait the full grace period.
	sctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := s.StopCore(sctx); err != nil {
		t.Fatal(err)
	}
	if got := fs.procs[0].sent(); len(got) != 2 || got[0] != "interrupt" || got[1] != "kill" {
		t.Fatalf("signals = %v", got)
	}
	if s.Running() {
		t.Fatal("still running after kill")
	}
}

func TestLogRotation(t *testing.T) {
	dir := resolvedTempDir(t)
	path := filepath.Join(dir, "core.log")
	if err := os.WriteFile(path, make([]byte, LogRotateSize), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := openLogFile(path, "-1")
	if err == nil {
		_ = w.Close()
	}
	// uid "-1" is invalid, so chown fails; use the current uid instead.
	w, err = openLogFile(path, strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "new\n")
	_ = w.Close()
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != LogRotateSize {
		t.Fatalf("rotated file: %v", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new\n" {
		t.Fatalf("new log = %q", b)
	}
}

// TestProtocolOverUnixSocket runs the real handler behind a protocol
// server on a temp unix socket and drives it with the protocol client,
// including the peer-uid authorizer.
func TestProtocolOverUnixSocket(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	sock := filepath.Join(shortTempDir(t), "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &protocol.Server{Handler: NewHandler(s, "test"), Authorize: Authorizer(strconv.Itoa(os.Getuid())), Logf: t.Logf}
	go func() { _ = srv.Serve(ctx, l) }()
	c := &protocol.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}

	resp, err := c.Call(ctx, protocol.Request{Op: protocol.OpPing})
	if err != nil || resp.Version != "test" || resp.Protocol != protocol.Version || resp.CorePath != f.core {
		t.Fatalf("ping: %+v %v", resp, err)
	}
	resp, err = c.Call(ctx, protocol.Request{Op: protocol.OpStart, Spec: &f.spec})
	if err != nil || !resp.Running || resp.PID == 0 {
		t.Fatalf("start: %+v %v", resp, err)
	}
	bad := f.spec
	bad.Bin = "/bin/sh"
	if _, err := c.Call(ctx, protocol.Request{Op: protocol.OpStart, Spec: &bad}); err == nil {
		t.Fatal("start with foreign binary accepted over the wire")
	}
	fs.procs[0].finish(errors.New("exit status 2"))
	var st protocol.Response
	for i := 0; i < 100; i++ {
		st, err = c.Call(ctx, protocol.Request{Op: protocol.OpStatus})
		if err != nil {
			t.Fatal(err)
		}
		if !st.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Running || !strings.Contains(st.ExitError, "exit status 2") {
		t.Fatalf("status: %+v", st)
	}
	if resp, err = c.Call(ctx, protocol.Request{Op: protocol.OpStop}); err != nil || resp.Running {
		t.Fatalf("stop: %+v %v", resp, err)
	}

	// A wrong peer uid is rejected by the authorizer.
	srv2 := &protocol.Server{Handler: NewHandler(s, "test"), Authorize: Authorizer("4294967")}
	sock2 := filepath.Join(filepath.Dir(sock), "h2.sock")
	l2, err := net.Listen("unix", sock2)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv2.Serve(ctx, l2) }()
	c2 := &protocol.Client{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock2)
	}}
	cctx, ccancel := context.WithTimeout(ctx, time.Second)
	defer ccancel()
	if _, err := c2.Call(cctx, protocol.Request{Op: protocol.OpPing}); err == nil {
		t.Fatal("connection from a different uid accepted")
	}
}

func TestPeerUID(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "p.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := net.Dial("unix", sock)
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			c.Close()
		}
	}()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	uid, err := PeerUID(conn)
	if err != nil || int(uid) != os.Getuid() {
		t.Fatalf("uid=%d err=%v want %d", uid, err, os.Getuid())
	}
}

// shortTempDir returns a temp dir short enough for a unix socket path.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "lfh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// A start with a config the helper refuses spawns nothing; one whose
// controller or UI keys had to go starts on the cleaned copy.
func TestSupervisorStartStagesConfig(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	ctx := context.Background()
	writeFile(t, f.spec.Config, "proxy-providers:\n  p: {type: http, url: 'https://e/x', path: /etc/cron.d/x}\n", 0o600)
	if err := s.StartCore(ctx, f.spec); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("start with an escaping provider path: %v", err)
	}
	if s.Running() || len(fs.procs) != 0 {
		t.Fatal("a core was spawned for a refused config")
	}
	if _, err := os.Stat(f.spec.LogFile); err == nil {
		t.Fatal("the log was opened for a refused config")
	}

	writeFile(t, f.spec.Config, "mixed-port: 7890\nexternal-ui: /etc\nexternal-ui-url: https://e/ui.zip\nexternal-controller: 0.0.0.0:9090\n", 0o600)
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	defer s.StopCore(ctx)
	staged, err := os.ReadFile(fs.last.Args[3])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(staged), "external") || !strings.Contains(string(staged), "mixed-port: 7890") {
		t.Fatalf("the core's config:\n%s", staged)
	}
}

// The stage op: the app's hot reload gets the path of a fresh, checked
// copy inside the helper's work dir.
func TestSupervisorStageConfig(t *testing.T) {
	s, f, _ := newTestSupervisor(t)
	ctx := context.Background()
	h := NewHandler(s, "test")
	stage := func(spec protocol.Spec) protocol.Response {
		return h(ctx, protocol.Request{V: protocol.Version, Op: protocol.OpStage, Spec: &spec})
	}
	if resp := stage(f.spec); resp.OK || resp.ConfigPath != "" {
		t.Fatalf("stage without a running core: %+v", resp)
	}
	if resp := h(ctx, protocol.Request{V: protocol.Version, Op: protocol.OpStage}); resp.OK {
		t.Fatalf("stage without a spec: %+v", resp)
	}
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	defer s.StopCore(ctx)
	want := filepath.Join(s.WorkDir, stagedConfigName)

	// The app wrote a new runtime config and a provider file it names.
	writeFile(t, f.spec.Config, "mixed-port: 7891\nsecret: x\nrule-providers:\n  r: {type: http, behavior: domain, url: 'https://e/r', path: rs/r.yaml}\n", 0o600)
	if err := os.MkdirAll(filepath.Join(f.spec.WorkDir, "rs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.spec.WorkDir, "rs", "r.yaml"), "payload: []", 0o600)
	// Only the config of the request is used; the rest of the spec is the
	// run's.
	resp := stage(protocol.Spec{Config: f.spec.Config, WorkDir: "/etc", Bin: "/bin/sh"})
	if !resp.OK || resp.ConfigPath != want || !resp.Running {
		t.Fatalf("stage: %+v", resp)
	}
	got, _ := os.ReadFile(want)
	if !strings.Contains(string(got), "mixed-port: 7891") || strings.Contains(string(got), "secret") {
		t.Fatalf("staged config:\n%s", got)
	}
	if b, err := os.ReadFile(filepath.Join(s.WorkDir, "rs", "r.yaml")); err != nil || string(b) != "payload: []" {
		t.Fatalf("provider file: %q %v", b, err)
	}

	// A refused config leaves the copy the core last loaded.
	writeFile(t, f.spec.Config, "rule-providers:\n  r: {type: http, behavior: domain, url: 'https://e/r', path: ../r.yaml}\n", 0o600)
	if resp := stage(f.spec); resp.OK {
		t.Fatalf("stage of an escaping provider path: %+v", resp)
	}
	if now, _ := os.ReadFile(want); string(now) != string(got) {
		t.Fatalf("staged config changed to:\n%s", now)
	}

	// A config outside the data directory, or behind a link, is refused.
	outside := filepath.Join(resolvedTempDir(t), "runtime.yaml")
	writeFile(t, outside, "mixed-port: 1\n", 0o600)
	if resp := stage(protocol.Spec{Config: outside}); resp.OK {
		t.Fatalf("stage of a config outside the data dir: %+v", resp)
	}
	link := filepath.Join(f.dataDir, "link.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if resp := stage(protocol.Spec{Config: link}); resp.OK {
		t.Fatalf("stage of a linked config: %+v", resp)
	}
}

// Review 2 and 5: reading, checking and copying files happens outside the
// lock status, stop and ping need, so a slow start does not freeze the
// helper; a stop that arrives meanwhile cancels the start.
func TestSupervisorStaysResponsiveWhileStaging(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	s.PrepareWorkDir = func(dir string) error {
		close(entered)
		<-release
		return prepareWorkDir(dir)
	}
	started := make(chan error, 1)
	go func() { started <- s.StartCore(ctx, f.spec) }()
	<-entered

	h := NewHandler(s, "test")
	answered := make(chan string, 3)
	go func() {
		for _, op := range []string{protocol.OpStatus, protocol.OpPing, protocol.OpStop} {
			if resp := h(ctx, protocol.Request{V: protocol.Version, Op: op}); !resp.OK || resp.Running {
				answered <- op + ": " + resp.Error
				return
			}
			answered <- ""
		}
	}()
	for i := 0; i < 3; i++ {
		select {
		case msg := <-answered:
			if msg != "" {
				t.Fatal(msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("status, ping or stop waited for a start that is still copying files")
		}
	}
	// A second start does wait its turn, and a stage request is refused
	// (nothing runs).
	close(release)
	if err := <-started; err == nil || !strings.Contains(err.Error(), "取消") {
		t.Fatalf("a start that was stopped while preparing: %v", err)
	}
	if s.Running() || len(fs.procs) != 0 {
		t.Fatal("a core runs after stop")
	}

	// The next start is not affected by the earlier stop.
	s.PrepareWorkDir = nil
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	if err := s.StopCore(ctx); err != nil {
		t.Fatal(err)
	}
}
