package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var builtinTailscale = Tailscale{
	Node: &TailscaleNode{
		Hostname: "mac-lanflow", AuthKey: "tskey-auth-test", ControlURL: "https://hs.example.com",
		StateDir: "tailscale", AcceptRoutes: true,
	},
	DirectCIDRs: []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48", "10.30.0.0/16"},
	Domains:     []string{"ts.net", "lab.example.net"},
	DNS: []DomainDNS{
		{Domain: "ts.net", Servers: []string{TailscaleDNS}},
		{Domain: "lab.example.net", Servers: []string{"10.30.0.53"}},
	},
}

// TestComposeTailscaleBuiltin: the core's own node is a tailscale
// outbound under the shared name, the tailnet goes to it ahead of every
// rule, MagicDNS resolves through tailscale://, and nothing is kept out of
// the TUN (the tailnet is only reachable through the core).
func TestComposeTailscaleBuiltin(t *testing.T) {
	t.Parallel()
	in := Input{Subscription: []byte(autoGroupSub), Managed: Managed{MixedPort: 7890, TUN: true}, Tailscale: builtinTailscale}
	in.DNS.TakeOver = true
	in.DNS.Nameserver = []string{"https://doh.pub/dns-query"}
	out, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name          string `yaml:"name"`
			ExcludeFilter string `yaml:"exclude-filter"`
		} `yaml:"proxy-groups"`
		Rules []string `yaml:"rules"`
		DNS   struct {
			Filter []string  `yaml:"fake-ip-filter"`
			Policy yaml.Node `yaml:"nameserver-policy"`
		} `yaml:"dns"`
		TUN struct {
			Exclude []string `yaml:"route-exclude-address"`
		} `yaml:"tun"`
	}
	if err := yaml.Unmarshal(out.YAML, &cfg); err != nil {
		t.Fatal(err)
	}
	var node map[string]any
	n := 0
	for _, p := range cfg.Proxies {
		if p["name"] == TailscaleOutbound {
			node = p
			n++
		}
	}
	if n != 1 || node["type"] != "tailscale" || node["hostname"] != "mac-lanflow" || node["auth-key"] != "tskey-auth-test" ||
		node["control-url"] != "https://hs.example.com" || node["state-dir"] != "tailscale" || node["accept-routes"] != true || node["udp"] != true {
		t.Fatalf("outbound %d: %v", n, node)
	}
	if _, ok := node["interface-name"]; ok {
		t.Error("built-in node bound to an interface")
	}
	if _, ok := node["exit-node"]; ok {
		t.Error("exit-node written without one")
	}
	want := []string{
		"DOMAIN-SUFFIX,ts.net,LANFLOW-TAILSCALE",
		"DOMAIN-SUFFIX,lab.example.net,LANFLOW-TAILSCALE",
		"IP-CIDR,100.64.0.0/10,LANFLOW-TAILSCALE,no-resolve",
		"IP-CIDR6,fd7a:115c:a1e0::/48,LANFLOW-TAILSCALE,no-resolve",
		"IP-CIDR,10.30.0.0/16,LANFLOW-TAILSCALE,no-resolve",
	}
	if len(cfg.Rules) < len(want) || strings.Join(cfg.Rules[:len(want)], "\n") != strings.Join(want, "\n") {
		t.Fatalf("rules = %v", cfg.Rules)
	}
	if len(cfg.TUN.Exclude) != 0 {
		t.Errorf("route-exclude-address = %v", cfg.TUN.Exclude)
	}
	pol := cfg.DNS.Policy.Content
	if len(pol) < 4 || pol[0].Value != "+.ts.net" || pol[2].Value != "+.lab.example.net" {
		t.Fatalf("policy keys %v", keysOf(pol))
	}
	if s := pol[1].Content; len(s) != 1 || s[0].Value != "tailscale://LANFLOW-TAILSCALE" {
		t.Errorf("ts.net servers %v", s)
	}
	if s := pol[3].Content; len(s) != 1 || s[0].Value != "10.30.0.53#LANFLOW-TAILSCALE" {
		t.Errorf("lab servers %v", s)
	}
	if !hasItem(cfg.DNS.Filter, "+.ts.net") {
		t.Errorf("fake-ip-filter = %v", cfg.DNS.Filter)
	}
	for _, g := range cfg.Groups {
		if g.Name == "节点选择" && !strings.Contains(g.ExcludeFilter, "^LANFLOW-TAILSCALE$") {
			t.Errorf("include-all group not excluding it: %q", g.ExcludeFilter)
		}
	}
	found := false
	for _, l := range out.Lines {
		if l.Source == layerTailscaleBuiltin {
			found = true
		}
	}
	if !found {
		t.Error("no line attributed to the built-in layer")
	}
}

// TestComposeTailscaleBuiltinMinimal: without a key, control URL or exit
// node only what is set is written; accept-routes is always written so a
// node that had it on gets it turned off.
func TestComposeTailscaleBuiltinMinimal(t *testing.T) {
	t.Parallel()
	ts := Tailscale{Node: &TailscaleNode{Hostname: "h", StateDir: "tailscale-2", ExitNode: "auto:any"}, DirectCIDRs: []string{"100.64.0.0/10"}}
	out, err := Compose(Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890}, Tailscale: ts})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(out.YAML, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Proxies {
		if p["name"] != TailscaleOutbound {
			continue
		}
		if _, ok := p["auth-key"]; ok {
			t.Error("empty auth-key written")
		}
		if _, ok := p["control-url"]; ok {
			t.Error("empty control-url written")
		}
		if p["accept-routes"] != false || p["exit-node"] != "auto:any" || p["state-dir"] != "tailscale-2" {
			t.Errorf("outbound %v", p)
		}
		return
	}
	t.Fatal("no outbound")
}

// TestComposeTailscaleBuiltinMihomoCheck: the built-in node and the
// tailscale:// nameserver pass `mihomo -t` (the outbound does not start
// its tailnet client while the config is only checked).
func TestComposeTailscaleBuiltinMihomoCheck(t *testing.T) {
	t.Parallel()
	bin := mihomoTestPath()
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("%s not present", bin)
	}
	ts := builtinTailscale
	node := *ts.Node
	node.AuthKey, node.ExitNode = "", "100.64.0.9"
	ts.Node = &node
	in := Input{Subscription: []byte(autoGroupSub), Managed: Managed{MixedPort: 17892, TUN: true, TUNStack: "mixed"}, Tailscale: ts}
	in.DNS.TakeOver = true
	in.DNS.Nameserver = []string{"https://doh.pub/dns-query"}
	in.Sniffer.TakeOver, in.Sniffer.Enable, in.Sniffer.TLS = true, true, true
	out, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, src := range []string{filepath.Join("..", "..", "bin", "geo"), filepath.Join("..", "..", "resources", "geo")} {
		ents, _ := os.ReadDir(src)
		for _, e := range ents {
			if b, err := os.ReadFile(filepath.Join(src, e.Name())); err == nil {
				_ = os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600)
			}
		}
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, out.YAML, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := exec.Command(bin, "-d", dir, "-f", cfg, "-t").CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t failed: %v\n%s\n--- config ---\n%s", err, res, out.YAML)
	}
	if _, err := os.Stat(filepath.Join(dir, "tailscale", "tailscaled.state")); err == nil {
		t.Error("checking the config started the tailnet client")
	}
}
