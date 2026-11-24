package profile

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"lanflow/internal/model"
)

const lanSub = testSub + `listeners:
  - {name: open, type: mixed, listen: 0.0.0.0, port: 7899}
  - {name: bare, type: socks, port: 7898}
  - {name: v6, type: http, listen: "::", port: 7897}
  - {name: local, type: http, listen: 127.0.0.1, port: 7896}
  - {name: lanflow-probe, type: http, listen: 0.0.0.0, port: 1}
tunnels:
  - tcp/udp,0.0.0.0:6553,114.114.114.114:53,DIRECT
  - tcp,127.0.0.1:6554,1.1.1.1:53,DIRECT
  - {network: [tcp], address: "[::]:6555", target: "1.1.1.1:53", proxy: DIRECT}
  - {network: [tcp], address: nonsense, target: "1.1.1.1:53", proxy: DIRECT}
dns:
  enable: true
  listen: 0.0.0.0:1053
  nameserver: [223.5.5.5]
`

func listenersOf(t *testing.T, m map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	ls, _ := m["listeners"].([]any)
	for _, l := range ls {
		it := l.(map[string]any)
		out[fmt.Sprint(it["name"])] = fmt.Sprint(it["listen"])
	}
	return out
}

// TestComposeConfinesSubscriptionListeners: with 允许局域网 off, nothing a
// subscription brings listens beyond this machine.
func TestComposeConfinesSubscriptionListeners(t *testing.T) {
	t.Parallel()
	out, err := Compose(Input{Subscription: []byte(lanSub), Managed: Managed{MixedPort: 17890, ProbePort: 41234}})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	got := listenersOf(t, m)
	want := map[string]string{"open": "127.0.0.1", "bare": "127.0.0.1", "v6": "127.0.0.1", "local": "127.0.0.1", "lanflow-probe": "127.0.0.1"}
	if len(got) != len(want) {
		t.Fatalf("listeners = %v", got)
	}
	for name, listen := range want {
		if got[name] != listen {
			t.Errorf("listener %s listens on %q, want %q", name, got[name], listen)
		}
	}
	// the app's own probe listener is the one in the result, not the
	// subscription's entry of that name
	for _, l := range m["listeners"].([]any) {
		if it := l.(map[string]any); it["name"] == "lanflow-probe" && (it["port"] != 41234 || it["proxy"] != "LANFLOW-PROBE") {
			t.Errorf("probe listener = %v", it)
		}
	}

	ts, _ := m["tunnels"].([]any)
	if len(ts) != 3 {
		t.Fatalf("tunnels = %v", ts)
	}
	if ts[0] != "tcp/udp,127.0.0.1:6553,114.114.114.114:53,DIRECT" || ts[1] != "tcp,127.0.0.1:6554,1.1.1.1:53,DIRECT" {
		t.Errorf("string tunnels = %v", ts[:2])
	}
	if a := ts[2].(map[string]any)["address"]; a != "127.0.0.1:6555" {
		t.Errorf("mapping tunnel address = %v", a)
	}
	if l := m["dns"].(map[string]any)["listen"]; l != "127.0.0.1:1053" {
		t.Errorf("dns.listen = %v", l)
	}
	if m["allow-lan"] != false {
		t.Errorf("allow-lan = %v", m["allow-lan"])
	}
	// every change is reported: open, bare, v6, the stale probe entry, two
	// tunnels rewritten, one removed, dns.listen
	if len(out.Warnings) != 8 {
		t.Errorf("warnings (%d): %s", len(out.Warnings), strings.Join(out.Warnings, "\n"))
	}
}

// TestComposeAllowLANKeepsSubscriptionListeners: with the switch on, the
// subscription's listeners are left as they are.
func TestComposeAllowLANKeepsSubscriptionListeners(t *testing.T) {
	t.Parallel()
	out, err := Compose(Input{Subscription: []byte(lanSub), Managed: Managed{MixedPort: 17890, AllowLAN: true}})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	got := listenersOf(t, m)
	if got["open"] != "0.0.0.0" || got["v6"] != "::" || got["bare"] != "<nil>" {
		t.Errorf("listeners = %v", got)
	}
	if ts := m["tunnels"].([]any); len(ts) != 4 || ts[0] != "tcp/udp,0.0.0.0:6553,114.114.114.114:53,DIRECT" {
		t.Errorf("tunnels = %v", ts)
	}
	if l := m["dns"].(map[string]any)["listen"]; l != "0.0.0.0:1053" {
		t.Errorf("dns.listen = %v", l)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("warnings: %v", out.Warnings)
	}
}

// TestComposeOverrideListenersKept: the override layers and the 高级
// options are the user's own and keep the address they name.
func TestComposeOverrideListenersKept(t *testing.T) {
	t.Parallel()
	out, err := Compose(Input{
		Subscription:   []byte(lanSub),
		SubOverride:    "listeners+:\n  - {name: mine, type: mixed, listen: 0.0.0.0, port: 7001}\n",
		GlobalOverride: "tunnels+:\n  - tcp,0.0.0.0:7002,1.1.1.1:53,DIRECT\n",
		Overrides:      []Layer{{Name: "列表", Text: "dns:\n  listen: 0.0.0.0:5353\n"}},
		Managed:        Managed{MixedPort: 17890},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	got := listenersOf(t, m)
	if got["mine"] != "0.0.0.0" || got["open"] != "127.0.0.1" {
		t.Errorf("listeners = %v", got)
	}
	ts := m["tunnels"].([]any)
	if ts[len(ts)-1] != "tcp,0.0.0.0:7002,1.1.1.1:53,DIRECT" || ts[0] != "tcp/udp,127.0.0.1:6553,114.114.114.114:53,DIRECT" {
		t.Errorf("tunnels = %v", ts)
	}
	if l := m["dns"].(map[string]any)["listen"]; l != "0.0.0.0:5353" {
		t.Errorf("dns.listen = %v", l)
	}

	// 高级 › DNS 监听 is written by Lanflow after the subscription's
	out, err = Compose(Input{Subscription: []byte(lanSub), Managed: Managed{MixedPort: 17890}, Advanced: model.AdvancedSettings{DNSListen: "0.0.0.0:53"}})
	if err != nil {
		t.Fatal(err)
	}
	if l := decode(t, out.YAML)["dns"].(map[string]any)["listen"]; l != "0.0.0.0:53" {
		t.Errorf("advanced dns.listen = %v", l)
	}
}

// TestComposeDNSTakeOverDropsSubscriptionListen: Lanflow's own dns
// section replaces the subscription's, listen included.
func TestComposeDNSTakeOverDropsSubscriptionListen(t *testing.T) {
	t.Parallel()
	out, err := Compose(Input{Subscription: []byte(lanSub), Managed: Managed{MixedPort: 17890}, DNS: model.DNSSettings{TakeOver: true}})
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := decode(t, out.YAML)["dns"].(map[string]any)["listen"]; ok {
		t.Errorf("dns.listen = %v", l)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, "dns.listen") {
			t.Errorf("warning about a section Lanflow replaces: %s", w)
		}
	}
}

func TestLoopbackAddr(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in, out     string
		changed, ok bool
	}{
		{"0.0.0.0:53", "127.0.0.1:53", true, true},
		{":53", "127.0.0.1:53", true, true},
		{"[::]:53", "127.0.0.1:53", true, true},
		{"192.168.1.2:53", "127.0.0.1:53", true, true},
		{"127.0.0.1:53", "127.0.0.1:53", false, true},
		{"[::1]:53", "[::1]:53", false, true},
		{"localhost:53", "localhost:53", false, true},
		{"53", "", false, false},
		{"", "", false, false},
	} {
		out, changed, ok := loopbackAddr(c.in)
		if out != c.out || changed != c.changed || ok != c.ok {
			t.Errorf("loopbackAddr(%q) = %q, %v, %v; want %q, %v, %v", c.in, out, changed, ok, c.out, c.changed, c.ok)
		}
	}
}

// TestComposeRefusesAliasBomb: anchors under a key Lanflow strips, nested
// so that every level multiplies the one below, are refused instead of
// being expanded into the proxies that reference them.
func TestComposeRefusesAliasBomb(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("external-ui:\n  a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "  a%d: &a%d [", i, i)
		for j := 0; j < 10; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "*a%d", i-1)
		}
		b.WriteString("]\n")
	}
	b.WriteString("proxies:\n  - {name: n1, type: socks5, server: 127.0.0.1, port: 1, x: *a9}\n")
	_, err := Compose(Input{Subscription: []byte(b.String()), Managed: Managed{MixedPort: 17890}})
	if !errors.Is(err, errAliasBomb) {
		t.Fatalf("err = %v", err)
	}

	// an ordinary dangling alias is still resolved
	sub := "external-ui:\n  base: &base {type: socks5, server: 127.0.0.1, port: 1}\nproxies:\n  - {name: n1, <<: *base}\n"
	out, err := Compose(Input{Subscription: []byte(sub), Managed: Managed{MixedPort: 17890}})
	if err != nil {
		t.Fatal(err)
	}
	p := decode(t, out.YAML)["proxies"].([]any)[0].(map[string]any)
	if p["server"] != "127.0.0.1" || p["type"] != "socks5" {
		t.Errorf("proxy = %v", p)
	}
}
