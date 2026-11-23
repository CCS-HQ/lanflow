package rpc_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/app/remote"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
)

// Settings saved as a patch (app.API.UpdateSettings) and the agents'
// PatchSettings on top of it.

// TestUpdateSettingsOverRPC: two windows' worth of writers read the same
// settings, each changes an option of its own, and both arrive: the
// service merges each change into what is stored then. base and next
// travel as JSON, and what a writer did not touch must not count as
// changed after the trip.
func TestUpdateSettingsOverRPC(t *testing.T) {
	h := start(t, nil)
	seed := h.fake.Settings()
	seed.AgentGrants = []model.AgentGrant{{Agent: "x", Allow: true, At: seed.Geo.UpdatedAt}}
	if err := h.fake.SaveSettings(seed); err != nil {
		t.Fatal(err)
	}
	a, b := h.dial(t, "ui", false), h.dial(t, "ui", false)
	baseA, baseB := a.Settings(), b.Settings()

	nextA := baseA
	nextA.LogLevel = "debug"
	nextB := baseB
	nextB.TrayNode = true
	nextB.DNS.TakeOver = !baseB.DNS.TakeOver
	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() { defer wg.Done(); errA = a.UpdateSettings(baseA, nextA) }()
	go func() { defer wg.Done(); errB = b.UpdateSettings(baseB, nextB) }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("UpdateSettings: %v, %v", errA, errB)
	}
	got := h.fake.Settings()
	if got.LogLevel != "debug" || !got.TrayNode || got.DNS.TakeOver == seed.DNS.TakeOver {
		t.Fatalf("a change was lost: log level %q, tray node %v, DNS take over %v", got.LogLevel, got.TrayNode, got.DNS.TakeOver)
	}

	// A whole save from a stale snapshot in between, then a patch from an
	// older one still: the patch changes its option alone.
	whole := h.fake.Settings()
	whole.Mode = model.ModeGlobal
	if err := a.SaveSettings(whole); err != nil {
		t.Fatal(err)
	}
	nextB = baseB
	nextB.DelayTimeoutMs = 4321
	if err := b.UpdateSettings(baseB, nextB); err != nil {
		t.Fatal(err)
	}
	// And the convenience form the pages use.
	if err := app.EditSettings(b, func(s *model.Settings) { s.TrayMode = true }); err != nil {
		t.Fatal(err)
	}
	got = h.fake.Settings()
	if got.Mode != model.ModeGlobal || got.DelayTimeoutMs != 4321 || !got.TrayMode || got.LogLevel != "debug" || !got.TrayNode || len(got.AgentGrants) != 1 {
		t.Fatalf("after a whole save and two patches: mode %s, delay timeout %d, tray mode %v, log level %q, tray node %v, %d grants",
			got.Mode, got.DelayTimeoutMs, got.TrayMode, got.LogLevel, got.TrayNode, len(got.AgentGrants))
	}
}

// TestUpdateSettingsRefusedToAgents: like SaveSettings, UpdateSettings
// takes whole settings structs, which agents only ever read redacted; and
// it would be a way around PatchSettings' locked fields and its
// confirmation of high-impact changes. ctl and MCP are refused before
// anything is asked, at a terminal too.
func TestUpdateSettingsRefusedToAgents(t *testing.T) {
	rec := &recorder{answer: rpc.AnswerAlways}
	h := start(t, rec.prompt)
	before := h.fake.Settings()
	next := before
	next.TUN, next.CorePath, next.Mode = true, "/tmp/x", model.ModeGlobal
	for name, c := range map[string]*remote.Client{
		"mcp":               h.dial(t, "mcp:claude", false),
		"ctl in a script":   h.dialScript(t),
		"ctl at a terminal": h.dial(t, "ctl", false),
	} {
		if err := c.UpdateSettings(before, next); code(err) != rpc.CodeDenied {
			t.Errorf("%s: UpdateSettings: %v, want denied", name, err)
		}
		if err := c.SaveSettings(next); code(err) != rpc.CodeDenied {
			t.Errorf("%s: SaveSettings: %v, want denied", name, err)
		}
	}
	if rec.count() != 0 {
		t.Errorf("the user was asked %d times about calls that are never allowed", rec.count())
	}
	if got := h.fake.Settings(); got.TUN || got.CorePath != before.CorePath || got.Mode != before.Mode {
		t.Fatalf("a refused call changed the settings: TUN %v, core %q, mode %s", got.TUN, got.CorePath, got.Mode)
	}
	if rpc.LevelOf("UpdateSettings") != rpc.LevelOf("SaveSettings") {
		t.Errorf("UpdateSettings is %s, SaveSettings %s", rpc.LevelOf("UpdateSettings"), rpc.LevelOf("SaveSettings"))
	}
	// The refusals are in the audit log.
	var entries []rpc.AuditEntry
	if err := h.dial(t, "ctl", false).Conn().CallAPI(context.Background(), "AuditLog", []any{20}, &entries); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Method == "UpdateSettings" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d audit entries for the refused UpdateSettings, want 3", n)
	}
}

// TestPatchSettingsHighImpactField: turning TUN on is a high-impact write
// through SetTUN, confirmed once per connection whatever the agent is
// allowed "always". Patching the setting must ask the same.
func TestPatchSettingsHighImpactField(t *testing.T) {
	rec := &recorder{answer: rpc.AnswerAlways}
	h := start(t, rec.prompt)
	patch := func(c *remote.Client, p map[string]any, dry bool) error {
		var res rpc.PatchResult
		return c.Conn().CallAPI(context.Background(), "PatchSettings", []any{p, dry}, &res)
	}
	mcp := h.dial(t, "mcp:claude", false)
	if err := patch(mcp, map[string]any{"Mode": "global"}, false); err != nil {
		t.Fatal(err)
	}
	if allow, ok := grantOf(h.fake.Settings(), "claude"); !ok || !allow || rec.count() != 1 {
		t.Fatalf("always was not remembered (asked %d)", rec.count())
	}
	// Small patches need no further question; nor does a dry run of TUN.
	if err := patch(mcp, map[string]any{"LogLevel": "debug"}, false); err != nil || rec.count() != 1 {
		t.Fatalf("small patch: err %v, asked %d times", err, rec.count())
	}
	if err := patch(mcp, map[string]any{"TUN": true}, true); err != nil || rec.count() != 1 {
		t.Fatalf("dry run: err %v, asked %d times", err, rec.count())
	}

	// TUN, in any spelling and beside other fields: refused when the user
	// says no, and nothing of the patch is stored.
	rec.set(rpc.AnswerDeny)
	if err := patch(mcp, map[string]any{"tun": true, "TrayNode": true}, false); code(err) != rpc.CodeConsent {
		t.Fatalf("TUN patch after a refusal: %v", err)
	}
	if c := rec.last(t); rec.count() != 2 || c.Kind != rpc.LevelHigh || c.Method != "PatchSettings" || c.Agent != "claude" {
		t.Fatalf("asked %d times, last %+v", rec.count(), c)
	}
	if s := h.fake.Settings(); s.TUN || s.TrayNode {
		t.Fatalf("a refused patch was stored: TUN %v, tray node %v", s.TUN, s.TrayNode)
	}
	if allow, ok := grantOf(h.fake.Settings(), "claude"); !ok || !allow {
		t.Fatal("refusing a high-impact patch changed the remembered grant")
	}

	// Confirmed on another connection: applied, and once per connection.
	rec.set(rpc.AnswerOnce)
	other := h.dial(t, "mcp:claude", false)
	if err := patch(other, map[string]any{"TUN": true}, false); err != nil || rec.count() != 3 {
		t.Fatalf("confirmed TUN patch: err %v, asked %d times", err, rec.count())
	}
	if !h.fake.Settings().TUN {
		t.Fatal("the confirmed patch was not stored")
	}
	if err := patch(other, map[string]any{"TUN": false}, false); err != nil || rec.count() != 3 {
		t.Fatalf("second TUN patch on the connection: err %v, asked %d times", err, rec.count())
	}

	// A script running `lanflow ctl config set TUN true` is asked as well;
	// a person at a terminal is not.
	rec.set(rpc.AnswerDeny)
	if err := patch(h.dialScript(t), map[string]any{"TUN": true}, false); code(err) != rpc.CodeConsent || rec.last(t).Kind != rpc.LevelHigh {
		t.Fatalf("script: err %v, last %+v", err, rec.last(t))
	}
	n := rec.count()
	if err := patch(h.dial(t, "ctl", false), map[string]any{"TUN": true}, false); err != nil || rec.count() != n {
		t.Fatalf("ctl at a terminal: err %v, asked %d more times", err, rec.count()-n)
	}
}

// meddling stores another writer's change right before each
// UpdateSettings reaches the fake: after the server read the settings it
// builds the change on, before it saves.
type meddling struct {
	*fake.Fake
	mu     sync.Mutex
	before []func(*model.Settings) // one per UpdateSettings call, in order
}

func (m *meddling) UpdateSettings(base, next model.Settings) error {
	m.mu.Lock()
	var edit func(*model.Settings)
	if len(m.before) > 0 {
		edit, m.before = m.before[0], m.before[1:]
	}
	m.mu.Unlock()
	if edit != nil {
		if err := app.EditSettings(m.Fake, edit); err != nil {
			return err
		}
	}
	return m.Fake.UpdateSettings(base, next)
}

// TestAgentWritesMergeIntoStoredSettings: what the server saves for an
// agent (the remembered answer, a patch, an undo) is a patch as well. The
// user changing a setting in the window at the same moment keeps it.
func TestAgentWritesMergeIntoStoredSettings(t *testing.T) {
	dir := shortDir(t)
	setPipe(t, dir)
	f := fake.NewInstant()
	m := &meddling{Fake: f, before: []func(*model.Settings){
		func(s *model.Settings) { s.TrayNode = true },       // while the grant is saved
		func(s *model.Settings) { s.LogLevel = "debug" },    // while the patch is saved
		func(s *model.Settings) { s.DelayTimeoutMs = 4321 }, // while the undo is saved
	}}
	rec := &recorder{answer: rpc.AnswerAlways}
	srv, err := rpc.Start(dir, rpc.Options{API: m, Version: "test", AuditDir: filepath.Join(dir, "logs"), Prompt: rec.prompt})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := remote.Dial(context.Background(), remote.Options{DataDir: dir, Client: "mcp:claude", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	before := f.Settings()

	var res rpc.PatchResult
	if err := c.Conn().CallAPI(context.Background(), "PatchSettings", []any{map[string]any{"Mode": "global", "TrayMode": true}, false}, &res); err != nil {
		t.Fatal(err)
	}
	got := f.Settings()
	if allow, ok := grantOf(got, "claude"); !ok || !allow {
		t.Fatal("the answer was not remembered")
	}
	if got.Mode != model.ModeGlobal || !got.TrayMode {
		t.Fatalf("the patch was not stored: mode %s, tray mode %v", got.Mode, got.TrayMode)
	}
	if !got.TrayNode || got.LogLevel != "debug" {
		t.Fatalf("the agent's write undid the user's: tray node %v, log level %q", got.TrayNode, got.LogLevel)
	}

	// Undo takes back what the patch changed, and only that: not what the
	// user set since (here: while the undo is saved, and one of the two
	// patched options again).
	if err := app.EditSettings(f, func(s *model.Settings) { s.MainGroup = "after" }); err != nil {
		t.Fatal(err)
	}
	var u rpc.UndoResult
	if err := c.Conn().CallAPI(context.Background(), "Undo", nil, &u); err != nil {
		t.Fatal(err)
	}
	got = f.Settings()
	if u.Method != "PatchSettings" || got.Mode != before.Mode || got.TrayMode != before.TrayMode {
		t.Fatalf("undo = %+v, mode %s, tray mode %v", u, got.Mode, got.TrayMode)
	}
	if !got.TrayNode || got.LogLevel != "debug" || got.DelayTimeoutMs != 4321 || got.MainGroup != "after" {
		t.Fatalf("undo took back more than the patch: tray node %v, log level %q, delay timeout %d, main group %q",
			got.TrayNode, got.LogLevel, got.DelayTimeoutMs, got.MainGroup)
	}
	if allow, ok := grantOf(got, "claude"); !ok || !allow {
		t.Fatal("undo dropped the remembered answer")
	}
	m.mu.Lock()
	left := len(m.before)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d of the three saves did not go through UpdateSettings", left)
	}
}

func patchCall(c *remote.Client, p map[string]any, dry bool) (rpc.PatchResult, error) {
	var res rpc.PatchResult
	err := c.Conn().CallAPI(context.Background(), "PatchSettings", []any{p, dry}, &res)
	return res, err
}

func undoCall(c *remote.Client) (rpc.UndoResult, error) {
	var u rpc.UndoResult
	err := c.Conn().CallAPI(context.Background(), "Undo", nil, &u)
	return u, err
}

// TestPatchAndUndoKeepStoredLists: applying a patch used to decode into
// the lists of the settings it had read, which then served as what undo
// goes back to. A masked password sent back with a new entry lost the
// real one on undo; a shorter list came back with the tail of the old.
func TestPatchAndUndoKeepStoredLists(t *testing.T) {
	h := start(t, nil)
	seed := h.fake.Settings()
	seed.Advanced.Authentication = []string{"u:secret"}
	seed.Advanced.HTTPPort = 8080
	seed.Bypass = []string{"a", "b"}
	seed.DNS.Nameserver = []string{"1.1.1.1"}
	if err := h.fake.SaveSettings(seed); err != nil {
		t.Fatal(err)
	}
	c := h.dial(t, "ctl", false) // at a terminal: nothing is asked

	// A: the agent read ["u:***"], adds a user and sends both.
	if _, err := patchCall(c, map[string]any{"Advanced": map[string]any{"authentication": []string{"u:***", "n:pw"}}}, false); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Settings().Advanced.Authentication; !slices.Equal(got, []string{"u:secret", "n:pw"}) {
		t.Fatalf("after the patch: %v, want the stored password kept", got)
	}
	if _, err := undoCall(c); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Settings().Advanced.Authentication; !slices.Equal(got, []string{"u:secret"}) {
		t.Fatalf("after undo: %v, want [u:secret]", got)
	}

	// B: a shorter list, and back.
	if _, err := patchCall(c, map[string]any{"Bypass": []string{"x"}}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := undoCall(c); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Settings().Bypass; !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Bypass after patch and undo: %v, want [a b]", got)
	}

	// A list of the same length: previewed, changed, and taken back.
	res, err := patchCall(c, map[string]any{"Bypass": []string{"p", "q"}, "DNS": map[string]any{"nameserver": []string{"8.8.8.8"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var plus, minus string
	for _, l := range res.Diff {
		switch l.Op {
		case '+':
			plus += l.Text
		case '-':
			minus += l.Text
		}
	}
	if !slices.Equal(res.Changed, []string{"Bypass", "DNS"}) || !strings.Contains(plus, `"p"`) || !strings.Contains(plus, "8.8.8.8") || !strings.Contains(minus, `"a"`) || !strings.Contains(minus, "1.1.1.1") {
		t.Fatalf("preview: changed %v, + %s, - %s", res.Changed, plus, minus)
	}
	if got := h.fake.Settings(); !slices.Equal(got.Bypass, []string{"a", "b"}) || got.DNS.Nameserver[0] != "1.1.1.1" {
		t.Fatalf("the preview changed the settings: %v %v", got.Bypass, got.DNS.Nameserver)
	}
	if _, err := patchCall(c, map[string]any{"Bypass": []string{"p", "q"}, "DNS": map[string]any{"nameserver": []string{"8.8.8.8"}}}, false); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Settings(); !slices.Equal(got.Bypass, []string{"p", "q"}) || got.DNS.Nameserver[0] != "8.8.8.8" {
		t.Fatalf("lists of the same length were not replaced: %v %v", got.Bypass, got.DNS.Nameserver)
	}

	// A port that is set, by the name agents read it under.
	if _, err := patchCall(c, map[string]any{"Advanced": map[string]any{"httpPort": 9090}}, false); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Settings().Advanced.HTTPPort; got != 9090 {
		t.Fatalf("Advanced.httpPort: %d, want 9090", got)
	}
	if _, err := patchCall(c, map[string]any{"Advanced": map[string]any{"HTTPPort": 9191}}, false); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Settings().Advanced.HTTPPort; got != 9191 {
		t.Fatalf("Advanced.HTTPPort: %d, want 9191", got)
	}
	for range 3 {
		if _, err := undoCall(c); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.fake.Settings(); got.Advanced.HTTPPort != 8080 || !slices.Equal(got.Bypass, []string{"a", "b"}) || got.DNS.Nameserver[0] != "1.1.1.1" ||
		!slices.Equal(got.Advanced.Authentication, []string{"u:secret"}) {
		t.Fatalf("after undoing everything: port %d, bypass %v, nameserver %v, authentication %v",
			got.Advanced.HTTPPort, got.Bypass, got.DNS.Nameserver, got.Advanced.Authentication)
	}
}

// TestUndoOfHighImpactWriteAsks: taking a high-impact write back is one.
// A connection that confirmed turning TUN off must not have it turned on
// again by a client that is merely allowed to write.
func TestUndoOfHighImpactWriteAsks(t *testing.T) {
	rec := &recorder{answer: rpc.AnswerAlways}
	h := start(t, rec.prompt)
	on := h.fake.Settings()
	on.TUN = true
	if err := h.fake.SaveSettings(on); err != nil {
		t.Fatal(err)
	}
	a := h.dial(t, "mcp:claude", false)
	if err := a.SetMode(model.ModeGlobal); err != nil { // "always" for plain writes
		t.Fatal(err)
	}
	rec.set(rpc.AnswerOnce)
	if _, err := patchCall(a, map[string]any{"TUN": false}, false); err != nil || h.fake.Settings().TUN {
		t.Fatalf("turning TUN off: %v", err)
	}
	asked := rec.count()

	// Another client, allowed "always": asked, and refused.
	rec.set(rpc.AnswerDeny)
	b := h.dial(t, "mcp:claude", false)
	if _, err := undoCall(b); code(err) != rpc.CodeConsent {
		t.Fatalf("undo without confirmation: %v", err)
	}
	if c := rec.last(t); rec.count() != asked+1 || c.Kind != rpc.LevelHigh || c.Method != "Undo" || !strings.Contains(c.Summary, "PatchSettings") {
		t.Fatalf("asked %d more times, last %+v", rec.count()-asked, c)
	}
	if h.fake.Settings().TUN {
		t.Fatal("TUN was turned on again without the user's confirmation")
	}

	// Confirmed: the entry is still there and is taken back.
	rec.set(rpc.AnswerOnce)
	c := h.dial(t, "mcp:claude", false)
	u, err := undoCall(c)
	if err != nil || u.Method != "PatchSettings" || !h.fake.Settings().TUN {
		t.Fatalf("confirmed undo: %+v, %v, TUN %v", u, err, h.fake.Settings().TUN)
	}
	if rec.count() != asked+2 {
		t.Fatalf("asked %d more times, want 2", rec.count()-asked)
	}

	// The same for a high-impact method of the API; a plain write is
	// taken back without a question.
	if err := c.SetSystemProxy(true); err != nil { // confirmed on this connection already
		t.Fatal(err)
	}
	rec.set(rpc.AnswerDeny)
	d := h.dial(t, "mcp:claude", false)
	n := rec.count()
	if _, err := undoCall(d); code(err) != rpc.CodeConsent || rec.count() != n+1 || rec.last(t).Kind != rpc.LevelHigh || !h.fake.Status().SystemProxy {
		t.Fatalf("undo of SetSystemProxy: %v, asked %d more times, system proxy %v", err, rec.count()-n, h.fake.Status().SystemProxy)
	}
	e := h.dial(t, "mcp:claude", false)
	if err := e.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	n = rec.count()
	if u, err := undoCall(e); err != nil || u.Method != "SetMode" || rec.count() != n || h.fake.Settings().Mode != model.ModeGlobal {
		t.Fatalf("undo of a plain write: %+v, %v, asked %d more times, mode %s", u, err, rec.count()-n, h.fake.Settings().Mode)
	}
}

// service stands in for what the fake leaves out of the real service:
// the settings it stores are normalized (a port equal to the mixed port is
// cleared). before and fail act on its next UpdateSettings.
type service struct {
	*fake.Fake
	mu      sync.Mutex
	fail    error
	setMode func() // runs inside SetMode: another writer at that moment
}

func (s *service) UpdateSettings(base, next model.Settings) error {
	s.mu.Lock()
	err := s.fail
	s.fail = nil
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Fake.SaveSettings(app.NormalizeSettings(model.MergeSettings(s.Fake.Settings(), base, next)))
}

func (s *service) SetMode(m model.Mode) error {
	if s.setMode != nil {
		s.setMode()
	}
	return s.Fake.SetMode(m)
}

// TestUndoTakesBackOnlyWhatIsStillThere: undo restores a setting only
// while it still has the value the write gave it, and says what it left;
// what the service changed along with the write goes back with it; what
// another writer stored during the write is not counted as the write's;
// and an undo that fails can be tried again.
func TestUndoTakesBackOnlyWhatIsStillThere(t *testing.T) {
	dir := shortDir(t)
	setPipe(t, dir)
	f := fake.NewInstant()
	seed := app.NormalizeSettings(f.Settings())
	seed.Advanced.HTTPPort = 8080
	if err := f.SaveSettings(seed); err != nil {
		t.Fatal(err)
	}
	api := &service{Fake: f}
	srv, err := rpc.Start(dir, rpc.Options{API: api, Version: "test", AuditDir: filepath.Join(dir, "logs")})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := remote.Dial(rpc.WithInteractive(context.Background()), remote.Options{DataDir: dir, Client: "ctl", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// The agent: rule → global, and two more settings. The user then
	// picks direct. Undo leaves the mode and says so.
	if _, err := patchCall(c, map[string]any{"Mode": "global", "LogLevel": "debug", "TrayNode": true}, false); err != nil {
		t.Fatal(err)
	}
	if err := f.SetMode(model.ModeDirect); err != nil {
		t.Fatal(err)
	}
	u, err := undoCall(c)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Settings(); got.Mode != model.ModeDirect || got.LogLevel != seed.LogLevel || got.TrayNode {
		t.Fatalf("after undo: mode %s (the user's direct), log level %q, tray node %v", got.Mode, got.LogLevel, got.TrayNode)
	}
	if !strings.Contains(u.Desc, "未恢复：Mode") {
		t.Fatalf("the result does not say the mode was left: %q", u.Desc)
	}

	// The same for a method of the API, with another writer storing
	// something while it runs: that is not the method's write.
	if err := f.SetMode(model.ModeRule); err != nil {
		t.Fatal(err)
	}
	api.setMode = func() { _ = app.EditSettings(f, func(s *model.Settings) { s.TrayGraph = true }) }
	if err := c.SetMode(model.ModeGlobal); err != nil {
		t.Fatal(err)
	}
	api.setMode = nil
	if u, err = undoCall(c); err != nil || u.Method != "SetMode" {
		t.Fatalf("undo: %+v, %v", u, err)
	}
	if got := f.Settings(); got.Mode != model.ModeRule || !got.TrayGraph {
		t.Fatalf("after undoing SetMode: mode %s, tray graph %v (stored by another writer meanwhile)", got.Mode, got.TrayGraph)
	}

	// The mixed port set to the HTTP port: the service clears the HTTP
	// port. Undo brings both back.
	if _, err := patchCall(c, map[string]any{"MixedPort": 8080}, false); err != nil {
		t.Fatal(err)
	}
	if got := f.Settings(); got.MixedPort != 8080 || got.Advanced.HTTPPort != 0 {
		t.Fatalf("after the patch: mixed port %d, HTTP port %d", got.MixedPort, got.Advanced.HTTPPort)
	}
	// An undo that fails stays on the stack.
	api.mu.Lock()
	api.fail = errors.New("disk full")
	api.mu.Unlock()
	if _, err := undoCall(c); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("failing undo: %v", err)
	}
	if u, err = undoCall(c); err != nil || u.Method != "PatchSettings" {
		t.Fatalf("undo after a failed one: %+v, %v", u, err)
	}
	if got := f.Settings(); got.MixedPort != seed.MixedPort || got.Advanced.HTTPPort != 8080 {
		t.Fatalf("after undo: mixed port %d, HTTP port %d, want %d and 8080", got.MixedPort, got.Advanced.HTTPPort, seed.MixedPort)
	}
	if strings.Contains(u.Desc, "未恢复") {
		t.Fatalf("undo left something: %q", u.Desc)
	}
	if _, err := undoCall(c); err == nil {
		t.Fatal("the stack should be empty")
	}
}

// TestProtocolVersionCoversUpdateSettings: the window saves settings with
// UpdateSettings alone. A service from before the method must fail the
// handshake, which makes the window replace it (internal/split), rather
// than answer every save with "no such method".
func TestProtocolVersionCoversUpdateSettings(t *testing.T) {
	if _, ok := rpc.Lookup("UpdateSettings"); !ok || rpc.ProtocolVersion < 2 {
		t.Fatalf("UpdateSettings in the manifest: %v, protocol version %d (1 had no UpdateSettings)", ok, rpc.ProtocolVersion)
	}
}
