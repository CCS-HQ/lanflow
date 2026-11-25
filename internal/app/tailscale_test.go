package app

import (
	"slices"
	"strings"
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/platform/tailscale"
	"lanflow/internal/profile"
)

var sampleTS = tailscale.Info{
	Iface:  "utun5",
	Addrs:  []string{"100.101.2.3", "fd7a:115c:a1e0::1234"},
	Routes: []string{"192.168.50.0/24"},
	Resolvers: []tailscale.Resolver{
		{Domain: "corp.example.com", Servers: []string{"10.20.0.53"}},
		{Domain: "tail1234.ts.net", Servers: []string{"100.100.100.100"}},
	},
	Suffix:   "tail1234.ts.net",
	MagicDNS: true,
}

func lastTailscale(h *harness) profile.Tailscale {
	h.prof.mu.Lock()
	defer h.prof.mu.Unlock()
	return h.prof.inputs[len(h.prof.inputs)-1].Tailscale
}

func TestTailscaleLayer(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, []string{"192.168.1.1"})
	if !h.s.Settings().TailscaleCompat {
		t.Fatal("Tailscale compatibility must be on by default")
	}
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	h.s.TakeNotices()

	n := lastInput(h)
	h.ts.set(sampleTS)
	h.s.recheckTailscale()
	if lastInput(h) == n {
		t.Fatal("config not recomposed after Tailscale appeared")
	}
	ts := lastTailscale(h)
	if ts.Iface != "utun5" {
		t.Fatalf("layer = %+v", ts)
	}
	if want := []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48", "192.168.50.0/24"}; !slices.Equal(ts.ExcludeCIDRs, want) || len(ts.DirectCIDRs) != 0 {
		t.Fatalf("ranges = %v / %v", ts.ExcludeCIDRs, ts.DirectCIDRs)
	}
	if !slices.Equal(ts.Domains, []string{"tail1234.ts.net"}) {
		t.Fatalf("domains = %v", ts.Domains)
	}
	var doms []string
	for _, e := range ts.DNS {
		doms = append(doms, e.Domain+"="+strings.Join(e.Servers, "+"))
	}
	if want := []string{"tail1234.ts.net=100.100.100.100", "corp.example.com=10.20.0.53", "ts.net=100.100.100.100"}; !slices.Equal(doms, want) {
		t.Fatalf("DNS = %v, want %v", doms, want)
	}
	st, _ := h.sys.Get()
	for _, want := range []string{"100.64.0.0/10", "*.ts.net", "*.tail1234.ts.net", "*.corp.example.com", "192.168.50.0/24"} {
		if !slices.Contains(st.Bypass, want) {
			t.Fatalf("bypass %v lacks %s", st.Bypass, want)
		}
	}
	s := h.s.Status().Tailscale
	if !s.Running || !s.Applied || s.Interface != "utun5" || s.Outbound != profile.TailscaleOutbound || !slices.Contains(s.Domains, "ts.net") {
		t.Fatalf("status = %+v", s)
	}
	if ns := h.s.TakeNotices(); len(ns) != 1 || !strings.Contains(ns[0].Title, "Tailscale") {
		t.Fatalf("notices = %+v", ns)
	}

	// an address change alone does not recompose
	n = lastInput(h)
	moved := sampleTS
	moved.Addrs = []string{"100.101.9.9"}
	h.ts.set(moved)
	h.s.recheckTailscale()
	if lastInput(h) != n {
		t.Fatal("recomposed for a new address")
	}
	if a := h.s.Status().Tailscale.Addresses; !slices.Equal(a, moved.Addrs) {
		t.Fatalf("addresses = %v", a)
	}

	// a short gap is ridden out; only tailscaleGrace misses in a row count
	h.ts.set(tailscale.Info{})
	for i := 1; i < tailscaleGrace; i++ {
		h.s.recheckTailscale()
		if lastInput(h) != n || !h.s.Status().Tailscale.Running {
			t.Fatalf("layer taken out after %d misses", i)
		}
	}
	h.ts.set(moved) // back: the count starts over
	h.s.recheckTailscale()
	h.ts.set(tailscale.Info{})
	for i := 1; i < tailscaleGrace; i++ {
		h.s.recheckTailscale()
	}
	if lastInput(h) != n {
		t.Fatal("misses not counted from the start again")
	}
	h.s.recheckTailscale()
	if lastInput(h) == n || lastTailscale(h).Iface != "" {
		t.Fatal("layer kept after Tailscale quit")
	}
	st, _ = h.sys.Get()
	if slices.Contains(st.Bypass, "100.64.0.0/10") {
		t.Fatalf("bypass still has the tailnet: %v", st.Bypass)
	}
	if s := h.s.Status().Tailscale; s.Running || s.Applied {
		t.Fatalf("status after quit = %+v", s)
	}

	// back again: no second toast in this run
	h.ts.set(sampleTS)
	h.s.recheckTailscale()
	if lastTailscale(h).Iface != "utun5" {
		t.Fatal("layer not back")
	}
	if ns := h.s.TakeNotices(); len(ns) != 0 {
		t.Fatalf("second toast: %+v", ns)
	}
}

func TestTailscaleSettingAndLists(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, nil)
	h.ts.set(sampleTS)
	h.s.recheckTailscale()
	h.s.TakeNotices()

	set := h.s.Settings()
	set.TailscaleCompat = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if ts := lastTailscale(h); ts.Iface != "" || h.s.Status().Tailscale.Running {
		t.Fatalf("setting off still applies: %+v", ts)
	}
	h.s.recheckTailscale()
	if h.s.Status().Tailscale.Running {
		t.Fatal("detected although the setting is off")
	}

	set.TailscaleCompat = true
	set.Tailscale = model.TailscaleConfig{
		Domains: []string{"+.Lab.Example.NET", "bad domain", "lab.example.net"},
		Routes:  []string{"10.9.0.0/16", "0.0.0.0/0", "nonsense"},
		DNS:     []string{"10.9.0.53", "fd7a:115c:a1e0::53"},
	}
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	got := h.s.Settings().Tailscale
	want := model.TailscaleConfig{Domains: []string{"lab.example.net"}, Routes: []string{"10.9.0.0/16"}, DNS: []string{"10.9.0.53"}}
	if !slices.Equal(got.Domains, want.Domains) || !slices.Equal(got.Routes, want.Routes) || !slices.Equal(got.DNS, want.DNS) {
		t.Fatalf("stored lists = %+v, want %+v", got, want)
	}
	ts := lastTailscale(h)
	if ts.Iface != "utun5" || !slices.Contains(ts.ExcludeCIDRs, "10.9.0.0/16") || !slices.Contains(ts.Domains, "lab.example.net") {
		t.Fatalf("lists not merged: %+v", ts)
	}
	for _, e := range ts.DNS {
		if e.Domain == "lab.example.net" && !slices.Equal(e.Servers, []string{"10.9.0.53"}) {
			t.Fatalf("lab DNS = %v", e.Servers)
		}
	}
}

func TestTailscaleExitNode(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, nil)
	exit := sampleTS
	exit.ExitNode = true
	h.ts.set(exit)
	h.s.recheckTailscale()
	h.s.TakeNotices()
	if !h.s.Status().Tailscale.ExitNode {
		t.Fatal("exit node not in the status")
	}
	// Tailscale's interface owns the default route: TUN is refused, saying why
	h.tunOf = "utun5"
	err := h.s.SetTUN(true)
	if err == nil || !strings.Contains(err.Error(), "出口节点") {
		t.Fatalf("SetTUN = %v", err)
	}
	r := h.s.TailscaleReport()
	if !r.Compat || !r.State.ExitNode || len(r.Notes) == 0 || !strings.Contains(r.Notes[0], "出口节点") {
		t.Fatalf("report = %+v", r)
	}

	// turned on while the TUN runs: a warning
	h.tunOf = ""
	h.ts.set(sampleTS)
	h.s.recheckTailscale()
	if err := h.s.SetTUN(true); err != nil {
		t.Fatal(err)
	}
	h.s.TakeNotices()
	h.ts.set(exit)
	h.s.recheckTailscale()
	ns := h.s.TakeNotices()
	if len(ns) != 1 || ns[0].Kind != "error" || !strings.Contains(ns[0].Title, "出口节点") {
		t.Fatalf("notices = %+v", ns)
	}
}

func TestTailscaleInputDNSRange(t *testing.T) {
	t.Parallel()
	set := defaultSettings()
	info := sampleTS
	info.Routes = []string{"10.20.0.0/16"}
	// the system DNS inside a subnet route keeps that range in the TUN;
	// Tailscale's own resolver never does
	ts := tailscaleInput(info, set, []string{"10.20.0.53", "100.100.100.100"})
	if !slices.Equal(ts.DirectCIDRs, []string{"10.20.0.0/16"}) || slices.Contains(ts.ExcludeCIDRs, "10.20.0.0/16") || !slices.Contains(ts.ExcludeCIDRs, "100.64.0.0/10") {
		t.Fatalf("ranges = %v / %v", ts.ExcludeCIDRs, ts.DirectCIDRs)
	}
	// nothing known about MagicDNS: no ts.net policy
	bare := tailscale.Info{Iface: "Tailscale"}
	ts = tailscaleInput(bare, set, nil)
	if len(ts.DNS) != 0 || len(ts.Domains) != 0 || len(ts.ExcludeCIDRs) != 2 {
		t.Fatalf("bare layer = %+v", ts)
	}
	if r := tailscaleReport(set, tailscaleStatus(bare, ts), nil, "windows"); len(r.Notes) != 2 {
		t.Fatalf("notes = %v", r.Notes)
	}
}

func TestCheckTailscaleConfig(t *testing.T) {
	t.Parallel()
	out, bad := CheckTailscaleConfig(model.TailscaleConfig{
		Domains: []string{"a.example", " ", "a.example", "x y"},
		Routes:  []string{"10.0.0.1", "::/0"},
		DNS:     []string{"1.1.1.1", "::1"},
	})
	if !slices.Equal(out.Domains, []string{"a.example"}) || !slices.Equal(out.Routes, []string{"10.0.0.1/32"}) || !slices.Equal(out.DNS, []string{"1.1.1.1"}) {
		t.Fatalf("out = %+v", out)
	}
	if len(bad) != 3 {
		t.Fatalf("bad = %v", bad)
	}
}

// The aTrust detection never takes Tailscale's interface for aTrust's.
func TestTailscaleOwnForATrust(t *testing.T) {
	t.Parallel()
	h := tunHarness(t, nil)
	h.ts.set(sampleTS)
	h.s.recheckTailscale()
	own := h.s.tailscaleOwn()
	if !own("utun5") || own("utun6") {
		t.Fatal("own does not cover Tailscale's interface alone")
	}
}
