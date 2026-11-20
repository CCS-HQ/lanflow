package rpc_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/app/fake"
	"lanflow/internal/app/remote"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
)

// recorder answers consent requests and keeps them.
type recorder struct {
	mu     sync.Mutex
	answer rpc.Answer
	seen   []rpc.Consent
}

func (r *recorder) prompt(_ context.Context, c rpc.Consent) (rpc.Answer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, c)
	return r.answer, nil
}

func (r *recorder) set(a rpc.Answer) {
	r.mu.Lock()
	r.answer = a
	r.mu.Unlock()
}

func (r *recorder) last(t *testing.T) rpc.Consent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		t.Fatal("nobody was asked")
	}
	return r.seen[len(r.seen)-1]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// TestUIIdentityNeedsOwnExecutable: a program other than Lanflow's own
// executable cannot call itself the window and skip the permission
// levels; as ctl it is not "a person at a terminal" either, and a
// remembered "always" does not count for it.
func TestUIIdentityNeedsOwnExecutable(t *testing.T) {
	rec := &recorder{answer: rpc.AnswerAlways}
	h := start(t, rec.prompt)

	// The test binary is the server's own executable: the window connects.
	ui := h.dial(t, "ui", false)
	if err := ui.InstallTUNHelper(); err != nil {
		t.Fatalf("the window was refused a call: %v", err)
	}

	defer rpc.SetSelfExe(filepath.Join(h.dir, "Lanflow"))() // now the test binary is some other program
	_, err := remote.Dial(context.Background(), remote.Options{DataDir: h.dir, Client: "ui", Version: "test"})
	if code(err) != rpc.CodeHandshake {
		t.Fatalf("another program connected as ui: err = %v", err)
	}
	// The window of another copy of Lanflow is told which service to
	// replace, as after an update (split.Connect).
	var re *rpc.Error
	if !errors.As(err, &re) || re.Data == nil || re.Data.Kind != "protocol" || re.Data.PID != os.Getpid() {
		t.Fatalf("refusal data = %+v", re)
	}
	// As ctl it may connect, but claiming a terminal does not help.
	ctl := h.dial(t, "ctl", false)
	if err := ctl.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	if c := rec.last(t); c.Client != "ctl" || c.Kind != rpc.LevelWrite || c.PID != os.Getpid() || c.Path == "" {
		t.Fatalf("consent = %+v, want a write request of ctl with this process", c)
	}
	if !strings.Contains(rec.last(t).Summary, rpc.PeerLine(os.Getpid(), rec.last(t).Path)) {
		t.Fatalf("the summary does not name the process: %q", rec.last(t).Summary)
	}
	// "Always" from an unidentified program is not remembered.
	if _, ok := grantOf(h.fake.Settings(), rpc.CtlGrant); ok {
		t.Fatal("a grant was stored for a program that only says it is lanflow ctl")
	}
	// Nor does a grant the user gave the real client count for it.
	s := h.fake.Settings()
	s.AgentGrants = []model.AgentGrant{{Agent: "claude", Allow: true}}
	if err := h.fake.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	n := rec.count()
	mcp := h.dial(t, "mcp:claude", false)
	if err := mcp.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	if rec.count() != n+1 {
		t.Fatal("a program using a remembered name was not asked")
	}
	// A remembered refusal holds for everyone.
	s = h.fake.Settings()
	s.AgentGrants = []model.AgentGrant{{Agent: "claude", Allow: false}}
	if err := h.fake.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := h.dial(t, "mcp:claude", false).SetMode(model.ModeRule); code(err) != rpc.CodeConsent {
		t.Fatalf("refused agent: %v", err)
	}
	// No MCP client may take the name ctl's answer is kept under.
	if _, err := remote.Dial(context.Background(), remote.Options{DataDir: h.dir, Client: "mcp:" + rpc.CtlGrant}); code(err) != rpc.CodeHandshake {
		t.Fatalf("mcp:%s: %v", rpc.CtlGrant, err)
	}
}

// TestScriptedCtlAsks: `lanflow ctl` away from a terminal (a script, an
// agent with a shell) asks before its first write, like MCP; at a
// terminal it does not.
func TestScriptedCtlAsks(t *testing.T) {
	rec := &recorder{answer: rpc.AnswerDeny}
	h := start(t, rec.prompt)

	script := h.dialScript(t)
	if script.Status().Core == "" {
		t.Fatalf("read: %v", script.LastError())
	}
	if rec.count() != 0 {
		t.Fatal("a read asked for consent")
	}
	if err := script.SetMode(model.ModeGlobal); code(err) != rpc.CodeConsent {
		t.Fatalf("refused write: %v", err)
	}
	if c := rec.last(t); c.Client != "ctl" || c.Agent != "" || c.Kind != rpc.LevelWrite || c.PID != os.Getpid() {
		t.Fatalf("consent = %+v", c)
	}
	if allow, ok := grantOf(h.fake.Settings(), rpc.CtlGrant); !ok || allow {
		t.Fatal("the refusal was not remembered")
	}
	// Revoked in the settings, then allowed for good.
	s := h.fake.Settings()
	s.AgentGrants = nil
	if err := h.fake.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	rec.set(rpc.AnswerAlways)
	if err := script.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	n := rec.count()
	if err := h.dialScript(t).SetMode(model.ModeRule); err != nil || rec.count() != n {
		t.Fatalf("a later script: err %v, asked %d more times", err, rec.count()-n)
	}
	// The audit names the process.
	var entries []rpc.AuditEntry
	if err := script.Conn().CallAPI(context.Background(), "AuditLog", []any{10}, &entries); err != nil {
		t.Fatal(err)
	}
	if last := entries[len(entries)-1]; last.Method != "SetMode" || last.PID != os.Getpid() || last.Path == "" {
		t.Fatalf("audit entry = %+v", last)
	}
	// At a terminal nothing is asked, high-impact writes included.
	term := h.dial(t, "ctl", false)
	if err := term.SetSystemProxy(false); err != nil || rec.count() != n {
		t.Fatalf("ctl at a terminal: err %v, asked %d more times", err, rec.count()-n)
	}
}

// TestHighImpactWrites: "always allow" covers the small writes only. A
// high-impact write is confirmed once per connection, without an
// "always".
func TestHighImpactWrites(t *testing.T) {
	rec := &recorder{answer: rpc.AnswerAlways}
	h := start(t, rec.prompt)
	mcp := h.dial(t, "mcp:claude", false)
	if err := mcp.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	if allow, ok := grantOf(h.fake.Settings(), "claude"); !ok || !allow || rec.count() != 1 {
		t.Fatalf("always was not remembered (asked %d)", rec.count())
	}

	rec.set(rpc.AnswerOnce)
	if err := mcp.SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	if c := rec.last(t); rec.count() != 2 || c.Kind != rpc.LevelHigh || c.Method != "SetSystemProxy" || c.Agent != "claude" {
		t.Fatalf("asked %d times, last %+v", rec.count(), c)
	}
	// Once per connection.
	if err := mcp.RestartCore(); err != nil || rec.count() != 2 {
		t.Fatalf("second high-impact write: err %v, asked %d times", err, rec.count())
	}
	// A small write still needs no question.
	if err := mcp.SetMode(model.ModeRule); err != nil || rec.count() != 2 {
		t.Fatalf("small write: err %v, asked %d times", err, rec.count())
	}
	// Even an "always" answer is for this connection only.
	rec.set(rpc.AnswerAlways)
	other := h.dial(t, "mcp:claude", false)
	if err := other.SetSystemProxy(false); err != nil || rec.count() != 3 {
		t.Fatalf("new connection: err %v, asked %d times", err, rec.count())
	}
	again := h.dial(t, "mcp:claude", false)
	if err := again.SetSystemProxy(false); err != nil || rec.count() != 4 {
		t.Fatalf("after an always answer: err %v, asked %d times", err, rec.count())
	}
	// A refusal holds for the connection and stores nothing.
	rec.set(rpc.AnswerDeny)
	deny := h.dial(t, "mcp:claude", false)
	if err := deny.StopCore(); code(err) != rpc.CodeConsent {
		t.Fatalf("refused: %v", err)
	}
	if err := deny.StopCore(); code(err) != rpc.CodeConsent || rec.count() != 5 {
		t.Fatalf("after a refusal: err %v, asked %d times", err, rec.count())
	}
	if allow, ok := grantOf(h.fake.Settings(), "claude"); !ok || !allow {
		t.Fatal("refusing a high-impact write changed the remembered grant")
	}
	// A dry run of ApplyConfigFile changes nothing: not high-impact.
	n := rec.count()
	if _, err := deny.ApplyConfigFile("", true); code(err) == rpc.CodeConsent || rec.count() != n {
		t.Fatalf("dry run: err %v, asked %d more times", err, rec.count()-n)
	}
	for _, m := range []string{"ImportSubscriptionURL", "UseSubscription", "SaveOverride", "SaveOverrideEntry", "DeleteOverrideEntry",
		"StartCore", "StopCore", "RestartCore", "SetSystemProxy", "SetTUN", "ApplyConfigFile", "ImportSubscriptionFile"} {
		if !rpc.HighImpact(m) || rpc.LevelOf(m) != rpc.LevelWrite {
			t.Errorf("%s: high impact %v, level %s", m, rpc.HighImpact(m), rpc.LevelOf(m))
		}
	}
	if rpc.HighImpact("SelectProxy") || rpc.HighImpact("SetMode") {
		t.Error("switching nodes or the mode is high-impact")
	}
}

// TestMCPFilePaths: MCP does not choose which local file Lanflow reads.
func TestMCPFilePaths(t *testing.T) {
	h := start(t, func(context.Context, rpc.Consent) (rpc.Answer, error) { return rpc.AnswerOnce, nil })
	mcp := h.dial(t, "mcp:claude", false)
	if _, err := mcp.ImportSubscriptionFile("/etc/hosts", false); code(err) != rpc.CodeDenied {
		t.Fatalf("ImportSubscriptionFile: %v", err)
	}
	if _, err := mcp.ApplyConfigFile("/etc/hosts", true); code(err) != rpc.CodeDenied {
		t.Fatalf("ApplyConfigFile outside the sync folder: %v", err)
	}
	// Beside the sync file it may.
	sync := filepath.Join(h.dir, "sync")
	if err := os.MkdirAll(sync, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.fake.SetConfigSync("folder", sync, ""); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(sync, "draft.yaml")
	if err := os.WriteFile(draft, []byte("format: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mcp.ApplyConfigFile(draft, true); code(err) == rpc.CodeDenied {
		t.Fatalf("a file beside lanflow.yaml: %v", err)
	}
	if _, err := mcp.ApplyConfigFile("", true); code(err) == rpc.CodeDenied {
		t.Fatalf("the sync file itself: %v", err)
	}
	// A link out of the folder is followed before the check.
	link := filepath.Join(sync, "link.yaml")
	if err := os.Symlink("/etc/hosts", link); err == nil {
		if _, err := mcp.ApplyConfigFile(link, true); code(err) != rpc.CodeDenied {
			t.Fatalf("a link out of the sync folder: %v", err)
		}
	}
	// ctl keeps both (a person names the file).
	ctl := h.dial(t, "ctl", false)
	if _, err := ctl.ApplyConfigFile("/etc/hosts", true); code(err) == rpc.CodeDenied {
		t.Fatalf("ctl ApplyConfigFile: %v", err)
	}
}

// panicky panics in SetMode.
type panicky struct{ *fake.Fake }

func (panicky) SetMode(model.Mode) error { panic("boom") }

// TestPanicBecomesError: a method that panics fails its call; the server
// goes on serving.
func TestPanicBecomesError(t *testing.T) {
	dir := shortDir(t)
	setPipe(t, dir)
	f := fake.NewInstant()
	srv, err := rpc.Start(dir, rpc.Options{API: panicky{f}, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := remote.Dial(context.Background(), remote.Options{DataDir: dir, Client: "ui", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Timeout = 10 * time.Second
	err = c.SetMode(model.ModeGlobal)
	if code(err) != rpc.CodeApp || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the panic as an error", err)
	}
	if c.Status().Core == "" {
		t.Fatalf("the server stopped answering: %v", c.LastError())
	}
}

// TestHandshakeDeadline: a connection that never says hello is closed.
func TestHandshakeDeadline(t *testing.T) {
	defer rpc.SetLimits(200*time.Millisecond, 0)()
	h := start(t, nil)
	nc, err := rpc.RawDial(context.Background(), h.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := nc.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !isClosed(err) {
		t.Fatalf("read = %v, want the server to close the silent connection", err)
	}
	// A hello larger than any real one is not buffered.
	nc2, err := rpc.RawDial(context.Background(), h.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer nc2.Close()
	go func() { _, _ = nc2.Write([]byte(strings.Repeat("x", 1<<20))) }()
	_ = nc2.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := nc2.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !isClosed(err) {
		t.Fatalf("read = %v, want the server to close an oversized hello", err)
	}
}

// isClosed: the peer closed (a reset or a broken pipe, not our deadline).
func isClosed(err error) bool {
	var ne interface{ Timeout() bool }
	return err != nil && !(errors.As(err, &ne) && ne.Timeout())
}

// TestConnectionLimit: connections beyond the limit are refused, and a
// closed one makes room again.
func TestConnectionLimit(t *testing.T) {
	defer rpc.SetLimits(0, 2)()
	h := start(t, nil)
	a, b := h.dial(t, "ctl", false), h.dial(t, "ctl", false)
	_ = b
	if _, err := remote.Dial(context.Background(), remote.Options{DataDir: h.dir, Client: "ctl"}); err == nil {
		t.Fatal("a third connection was accepted")
	}
	a.Close()
	var ok atomic.Bool
	for i := 0; i < 50 && !ok.Load(); i++ {
		if c, err := remote.Dial(context.Background(), remote.Options{DataDir: h.dir, Client: "ctl"}); err == nil {
			c.Close()
			ok.Store(true)
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !ok.Load() {
		t.Fatal("no room after a connection closed")
	}
}

// TestScriptCtlHighImpactCanBeRemembered: every run of `lanflow ctl` is a
// connection of its own, so "once per connection" would ask at each run of
// a script and refuse it when nobody is there to answer. A script's
// high-impact writes may be allowed for good; an MCP client's may not.
func TestScriptCtlHighImpactCanBeRemembered(t *testing.T) {
	rec := &recorder{answer: rpc.AnswerAlways}
	h := start(t, rec.prompt)

	if err := h.dialScript(t).SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	if c := rec.last(t); c.Client != "ctl" || c.Kind != rpc.LevelHigh {
		t.Fatalf("consent = %+v", c)
	}
	if allow, ok := grantOf(h.fake.Settings(), rpc.CtlHighGrant); !ok || !allow {
		t.Fatal("\"always\" for a script's high-impact write was not remembered")
	}
	n := rec.count()
	if err := h.dialScript(t).SetSystemProxy(false); err != nil || rec.count() != n {
		t.Fatalf("a later run of the script: err %v, asked %d more times", err, rec.count()-n)
	}

	// An MCP client answering "always" is still asked on its next connection.
	if err := h.dial(t, "mcp:claude", false).SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	n = rec.count()
	if err := h.dial(t, "mcp:claude", false).SetSystemProxy(false); err != nil || rec.count() != n+1 {
		t.Fatalf("a later MCP connection: err %v, asked %d more times, want 1", err, rec.count()-n)
	}
	// And no client may take the name the grant is kept under.
	if _, err := rpc.ParseIdentity("mcp:" + rpc.CtlHighGrant); err == nil {
		t.Fatal("an MCP client may call itself " + rpc.CtlHighGrant)
	}
}
