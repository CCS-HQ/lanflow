package app

import (
	"slices"
	"strings"
	"testing"

	"lanflow/internal/controller"
	"lanflow/internal/model"
	"lanflow/internal/platform/tailscale"
	"lanflow/internal/profile"
)

func builtinSettings(t *testing.T, h *harness, edit func(*model.TailscaleBuiltin)) {
	t.Helper()
	set := h.s.Settings()
	set.TailscaleBuiltin.Enabled = true
	if edit != nil {
		edit(&set.TailscaleBuiltin)
	}
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
}

// TestTailscaleBuiltinLayer: enabling the built-in node composes it with
// the auth key from the keychain, keeps every tailnet range in the TUN,
// leaves the system proxy's bypass alone and probes the node so the core
// starts it.
func TestTailscaleBuiltinLayer(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, []string{"192.168.1.1"})
	ks := newFakeSecrets()
	h.s.cs.secrets = ks
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetTailscaleAuthKey("tskey-auth-abc"); err != nil {
		t.Fatal(err)
	}
	if ks.get(tsAuthKeyKey) != "tskey-auth-abc" || !h.s.TailscaleAuthKeySet() {
		t.Fatal("auth key not in the keychain")
	}
	if ts := lastTailscale(h); ts.Node != nil {
		t.Fatal("node composed while off")
	}
	builtinSettings(t, h, func(b *model.TailscaleBuiltin) {
		b.Hostname, b.ControlURL, b.AcceptRoutes = "My Mac", "https://hs.example.com/", true
	})
	ts := lastTailscale(h)
	if ts.Node == nil || ts.Iface != "" {
		t.Fatalf("layer = %+v", ts)
	}
	n := *ts.Node
	if n.Hostname != "my-mac" || n.AuthKey != "tskey-auth-abc" || n.ControlURL != "https://hs.example.com" || n.StateDir != "tailscale" || !n.AcceptRoutes {
		t.Fatalf("node = %+v", n)
	}
	if !slices.Equal(ts.DirectCIDRs, []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}) || len(ts.ExcludeCIDRs) != 0 {
		t.Fatalf("ranges = %v / %v", ts.DirectCIDRs, ts.ExcludeCIDRs)
	}
	if len(ts.DNS) != 1 || ts.DNS[0].Domain != "ts.net" || ts.DNS[0].Servers[0] != profile.TailscaleDNS {
		t.Fatalf("dns = %+v", ts.DNS)
	}
	h.s.mu.Lock()
	bp := h.s.bypassLocked()
	h.s.mu.Unlock()
	if slices.Contains(bp, "100.64.0.0/10") || slices.Contains(bp, "*.ts.net") {
		t.Fatalf("bypass lets the tailnet past the core: %v", bp)
	}
	if !waitFor(func() bool {
		h.ctrl.mu.Lock()
		defer h.ctrl.mu.Unlock()
		for _, d := range h.ctrl.delays {
			if d[0] == tsKickURL {
				return true
			}
		}
		return false
	}) {
		t.Fatal("node not probed after the reload")
	}
	st := h.s.Status().Tailscale
	if !st.Applied || st.Outbound != profile.TailscaleOutbound || st.Builtin.State != model.TSStateStarting || st.Builtin.Hostname != "my-mac" {
		t.Fatalf("status = %+v", st)
	}
	r := h.s.TailscaleReport()
	if !r.Builtin.Enabled || !r.AuthKey || len(r.Notes) == 0 {
		t.Fatalf("report = %+v", r)
	}
	if err := h.s.SetTailscaleAuthKey(""); err != nil {
		t.Fatal(err)
	}
	if ts := lastTailscale(h); ts.Node == nil || ts.Node.AuthKey != "" {
		t.Fatalf("removed key still composed: %+v", ts.Node)
	}
}

// TestTailscaleBuiltinGivesWayToApp: while the Tailscale app runs the
// built-in node is left out (二选一) and the user is told; once the app
// quits, the node comes back.
func TestTailscaleBuiltinGivesWayToApp(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, nil)
	set := h.s.Settings()
	set.TailscaleCompat = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	builtinSettings(t, h, nil)
	h.s.TakeNotices()
	if lastTailscale(h).Node == nil {
		t.Fatal("node not composed")
	}

	h.ts.set(sampleTS)
	h.s.recheckTailscale()
	if ts := lastTailscale(h); ts.Node != nil || ts.Iface != "" {
		t.Fatalf("app running, compat off: layer = %+v", ts)
	}
	st := h.s.Status().Tailscale
	if !st.Running || st.Builtin.State != model.TSStatePausedForApp {
		t.Fatalf("status = %+v", st)
	}
	if !noticeTitled(h.s.TakeNotices(), "检测到 Tailscale App，内置 Tailscale 已暂停") {
		t.Fatal("no notice for the pause")
	}
	r := h.s.TailscaleReport()
	if !slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, "已暂停") }) {
		t.Fatalf("notes = %v", r.Notes)
	}

	// with 兼容 on, the app's interface is used instead
	set = h.s.Settings()
	set.TailscaleCompat = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if ts := lastTailscale(h); ts.Node != nil || ts.Iface != "utun5" {
		t.Fatalf("app running, compat on: layer = %+v", ts)
	}

	h.ts.set(tailscale.Info{})
	for range tailscaleGrace {
		h.s.recheckTailscale()
	}
	if lastTailscale(h).Node == nil {
		t.Fatal("node not back after the app quit")
	}
	if st := h.s.Status().Tailscale; st.Running || st.Builtin.State == model.TSStatePausedForApp {
		t.Fatalf("status = %+v", st)
	}
}

func noticeTitled(ns []Notice, title string) bool {
	return slices.ContainsFunc(ns, func(n Notice) bool { return n.Title == title })
}

// TestTailscaleBuiltinLog: the node's lines in the core's log drive its
// status: the login URL (notified once), the state, the addresses and
// the account.
func TestTailscaleBuiltinLog(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, nil)
	builtinSettings(t, h, nil)
	h.s.TakeNotices()
	p := "[Tailscale](LANFLOW-TAILSCALE) "
	h.s.onTailscaleLog("info", p+"LocalBackend state is NeedsLogin; running StartLoginInteractive...")
	h.s.onTailscaleLog("info", p+"To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/1a2b3c")
	h.s.onTailscaleLog("info", p+"To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/1a2b3c")
	b := h.s.Status().Tailscale.Builtin
	if b.State != model.TSStateNeedsLogin || b.LoginURL != "https://login.tailscale.com/a/1a2b3c" {
		t.Fatalf("builtin = %+v", b)
	}
	ns := h.s.TakeNotices()
	if c := len(slices.DeleteFunc(ns, func(n Notice) bool { return n.Title != "内置 Tailscale 需要登录" })); c != 1 {
		t.Fatalf("%d login notices", c)
	}
	if !slices.ContainsFunc(h.s.TailscaleReport().Notes, func(n string) bool { return strings.Contains(n, "login.tailscale.com/a/1a2b3c") }) {
		t.Fatal("report does not give the URL")
	}

	h.s.onTailscaleLog("debug", p+"Switching ipn state NeedsLogin -> Starting (WantRunning=true, nm=true)")
	h.s.onTailscaleLog("debug", p+`active login: "user@example.com"`)
	h.s.onTailscaleLog("debug", p+"peerapi: serving on http://[fd7a:115c:a1e0::5:6]:41641")
	if known := h.s.onTailscaleLog("debug", p+"peerapi: serving on http://100.101.102.103:41641"); !known {
		t.Fatal("addresses not reported known")
	}
	h.s.onTailscaleLog("info", p+"AuthLoop: state is Running; done")
	h.s.onTailscaleLog("info", "[TCP] 127.0.0.1:1 --> x.ts.net:22 using LANFLOW-TAILSCALE")
	b = h.s.Status().Tailscale.Builtin
	if b.State != model.TSStateRunning || b.LoginURL != "" || b.LoginName != "user@example.com" ||
		!slices.Equal(b.Addresses, []string{"100.101.102.103", "fd7a:115c:a1e0::5:6"}) {
		t.Fatalf("builtin = %+v", b)
	}
	if !noticeTitled(h.s.TakeNotices(), "内置 Tailscale 已连上 tailnet") {
		t.Fatal("no notice for the login")
	}
	h.s.onTailscaleLog("warning", p+"set exit node failed: no such node")
	if b := h.s.Status().Tailscale.Builtin; b.Error != "set exit node failed: no such node" {
		t.Fatalf("error = %q", b.Error)
	}

	// logging out starts over in a fresh state directory
	if err := h.s.LogoutTailscale(); err != nil {
		t.Fatal(err)
	}
	if ts := lastTailscale(h); ts.Node == nil || ts.Node.StateDir != "tailscale-1" {
		t.Fatalf("after logout: %+v", ts.Node)
	}
	if b := h.s.Status().Tailscale.Builtin; b.LoginName != "" || len(b.Addresses) != 0 {
		t.Fatalf("after logout: %+v", b)
	}
}

// TestTailscaleBuiltinLogStream: the node's own stream reads the core's
// log while the node is configured.
func TestTailscaleBuiltinLogStream(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, nil)
	builtinSettings(t, h, nil)
	l := controller.Log{Type: "info", Payload: "[Tailscale](LANFLOW-TAILSCALE) To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/zz"}
	// the window's log stream reads the same fake channel: offer the line
	// until the node's stream has taken it
	if !waitFor(func() bool {
		select {
		case h.ctrl.logsC <- l:
		default:
		}
		return h.s.Status().Tailscale.Builtin.LoginURL == "https://login.tailscale.com/a/zz"
	}) {
		t.Fatalf("builtin = %+v", h.s.Status().Tailscale.Builtin)
	}
}

func TestParseTailscaleLog(t *testing.T) {
	for line, want := range map[string]tsParsed{
		"AuthLoop: state is NeedsMachineAuth; done":          {state: model.TSStateMachineAuth},
		"AuthLoop: state is Starting; done":                  {state: model.TSStateStarting},
		"Switching ipn state Running -> NeedsLogin (x)":      {state: model.TSStateNeedsLogin},
		"peerapi: serving on http://8.8.8.8:1":               {},
		"tsnet starting with hostname \"mac-lanflow\", x":    {},
		"go to: https://hs.example.com/register/nodekey:abc": {state: model.TSStateNeedsLogin, url: "https://hs.example.com/register/nodekey:abc"},
	} {
		got, ok := parseTailscaleLog("info", tsLinePrefix+line)
		if !ok || got != want {
			t.Errorf("%q: %+v %v", line, got, ok)
		}
	}
	if _, ok := parseTailscaleLog("info", "[Tailscale](other) AuthLoop: state is Running; done"); ok {
		t.Error("another outbound's line taken")
	}
}

func TestCheckTailscaleBuiltin(t *testing.T) {
	got, bad := CheckTailscaleBuiltin(model.TailscaleBuiltin{
		Hostname: " Work_Mac ", ControlURL: "https://hs.example.com/", ExitNode: "100.64.0.9", StateGen: -1,
	})
	if len(bad) != 0 || got.Hostname != "work-mac" || got.ControlURL != "https://hs.example.com" || got.ExitNode != "100.64.0.9" || got.StateGen != 0 {
		t.Fatalf("%+v %v", got, bad)
	}
	for _, b := range []model.TailscaleBuiltin{
		{Hostname: "!!!"}, {ControlURL: "ftp://x"}, {ControlURL: "hs.example.com"}, {ExitNode: "not a node!"},
	} {
		if _, bad := CheckTailscaleBuiltin(b); len(bad) != 1 {
			t.Errorf("%+v: %v", b, bad)
		}
	}
	for _, e := range []string{"auto:any", "exit-1", "exit.tail1234.ts.net", "fd7a:115c:a1e0::9"} {
		if _, bad := CheckTailscaleBuiltin(model.TailscaleBuiltin{ExitNode: e}); len(bad) != 0 {
			t.Errorf("%s refused: %v", e, bad)
		}
	}
}

func TestTSHostname(t *testing.T) {
	old := osHostname
	t.Cleanup(func() { osHostname = old })
	osHostname = func() (string, error) { return "Yovin's MacBook-Pro.local", nil }
	if got := tsHostname(""); got != "yovin-s-macbook-pro-lanflow" {
		t.Errorf("default = %q", got)
	}
	if got := tsHostname("Lab Box"); got != "lab-box" {
		t.Errorf("set = %q", got)
	}
	osHostname = func() (string, error) { return "", nil }
	if got := tsHostname(""); got != "mac-lanflow" {
		t.Errorf("no name = %q", got)
	}
}
