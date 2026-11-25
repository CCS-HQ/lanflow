package profile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

var fullTailscale = Tailscale{
	Iface:        "utun5",
	ExcludeCIDRs: []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48", "192.168.50.0/24"},
	DirectCIDRs:  []string{"10.20.0.0/16"},
	Domains:      []string{"tail1234.ts.net"},
	DNS: []DomainDNS{
		{Domain: "tail1234.ts.net", Servers: []string{"100.100.100.100"}},
		{Domain: "ts.net", Servers: []string{"100.100.100.100"}},
		{Domain: "corp.example.com", Servers: []string{"10.20.0.53"}},
	},
}

type composedTailscale struct {
	Proxies []struct {
		Name  string `yaml:"name"`
		Type  string `yaml:"type"`
		Iface string `yaml:"interface-name"`
		UDP   bool   `yaml:"udp"`
	} `yaml:"proxies"`
	Groups []struct {
		Name          string   `yaml:"name"`
		Proxies       []string `yaml:"proxies"`
		ExcludeFilter string   `yaml:"exclude-filter"`
	} `yaml:"proxy-groups"`
	Rules []string `yaml:"rules"`
	DNS   struct {
		Filter []string  `yaml:"fake-ip-filter"`
		Policy yaml.Node `yaml:"nameserver-policy"`
	} `yaml:"dns"`
	Sniffer struct {
		Skip []string `yaml:"skip-domain"`
	} `yaml:"sniffer"`
	TUN struct {
		Exclude []string `yaml:"route-exclude-address"`
	} `yaml:"tun"`
}

func composeTailscale(t *testing.T, in Input) (composedTailscale, Output) {
	t.Helper()
	out, err := Compose(in)
	if err != nil {
		t.Fatal(err)
	}
	var cfg composedTailscale
	if err := yaml.Unmarshal(out.YAML, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, out
}

const autoGroupSub = `proxies:
  - {name: 东京 01, type: ss, server: 1.1.1.1, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: LANFLOW-TAILSCALE, type: ss, server: 1.1.1.3, port: 8388, cipher: aes-128-gcm, password: pw}
proxy-groups:
  - {name: 节点选择, type: select, include-all: true}
  - {name: 自动, type: url-test, include-all-proxies: true, exclude-filter: 香港, url: "https://www.gstatic.com/generate_204", interval: 300}
  - {name: 手选, type: select, proxies: [东京 01, DIRECT]}
rules:
  - DOMAIN-SUFFIX,example.org,节点选择
  - MATCH,节点选择
`

func TestComposeTailscale(t *testing.T) {
	t.Parallel()
	in := Input{Subscription: []byte(autoGroupSub), Managed: Managed{MixedPort: 7890, TUN: true}, Tailscale: fullTailscale}
	in.DNS.TakeOver = true
	in.DNS.Nameserver = []string{"https://doh.pub/dns-query"}
	in.DNS.Policy = nil
	in.Sniffer.TakeOver, in.Sniffer.Enable = true, true
	cfg, out := composeTailscale(t, in)

	// exactly one outbound of that name: ours, bound to Tailscale
	n := 0
	for _, p := range cfg.Proxies {
		if p.Name == TailscaleOutbound {
			n++
			if p.Type != "direct" || p.Iface != "utun5" || !p.UDP {
				t.Errorf("outbound = %+v", p)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d outbounds named %s", n, TailscaleOutbound)
	}
	// kept out of the groups that collect proxies by themselves
	for _, g := range cfg.Groups {
		switch g.Name {
		case "节点选择":
			if !strings.HasPrefix(g.ExcludeFilter, "^LANFLOW-TAILSCALE$") {
				t.Errorf("%s exclude-filter = %q", g.Name, g.ExcludeFilter)
			}
		case "自动":
			if !strings.HasPrefix(g.ExcludeFilter, "香港`^LANFLOW-TAILSCALE$") {
				t.Errorf("%s exclude-filter = %q", g.Name, g.ExcludeFilter)
			}
		case "手选":
			if g.ExcludeFilter != "" {
				t.Errorf("%s exclude-filter = %q", g.Name, g.ExcludeFilter)
			}
		}
	}
	wantHead := []string{
		"DOMAIN-SUFFIX,tail1234.ts.net,LANFLOW-TAILSCALE",
		"IP-CIDR,100.64.0.0/10,LANFLOW-TAILSCALE,no-resolve",
		"IP-CIDR6,fd7a:115c:a1e0::/48,LANFLOW-TAILSCALE,no-resolve",
		"IP-CIDR,192.168.50.0/24,LANFLOW-TAILSCALE,no-resolve",
		"IP-CIDR,10.20.0.0/16,LANFLOW-TAILSCALE,no-resolve",
	}
	if len(cfg.Rules) < len(wantHead) || strings.Join(cfg.Rules[:len(wantHead)], "|") != strings.Join(wantHead, "|") {
		t.Fatalf("rules head = %v", cfg.Rules)
	}
	// split DNS domains get DNS treatment, not a rule
	for _, r := range cfg.Rules {
		if strings.Contains(r, "corp.example.com") || strings.HasPrefix(r, "DOMAIN-SUFFIX,ts.net,") {
			t.Errorf("unexpected rule %q", r)
		}
	}
	for _, d := range []string{"+.tail1234.ts.net", "+.ts.net", "+.corp.example.com"} {
		if !hasItem(cfg.DNS.Filter, d) || !hasItem(cfg.Sniffer.Skip, d) {
			t.Errorf("%s: fake-ip-filter %v skip-domain %v", d, cfg.DNS.Filter, cfg.Sniffer.Skip)
		}
	}
	// the policies come first, in order, through the outbound
	pol := cfg.DNS.Policy.Content
	if len(pol) < 6 || pol[0].Value != "+.tail1234.ts.net" || pol[2].Value != "+.ts.net" || pol[4].Value != "+.corp.example.com" {
		t.Fatalf("nameserver-policy order: %s", out.YAML)
	}
	if pol[5].Kind != yaml.SequenceNode || pol[5].Content[0].Value != "10.20.0.53#LANFLOW-TAILSCALE" {
		t.Errorf("corp policy = %+v", pol[5])
	}
	// only the excluded ranges leave the TUN; the one holding a DNS server stays
	for _, c := range []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48", "192.168.50.0/24"} {
		if !hasItem(cfg.TUN.Exclude, c) {
			t.Errorf("route-exclude-address %v lacks %s", cfg.TUN.Exclude, c)
		}
	}
	if hasItem(cfg.TUN.Exclude, "10.20.0.0/16") {
		t.Errorf("DNS range excluded: %v", cfg.TUN.Exclude)
	}
	// attributed to the layer
	found := false
	for _, l := range out.Lines {
		if l.Source == layerTailscale {
			found = true
		}
	}
	if !found {
		t.Error("no line attributed to the Tailscale layer")
	}
}

func TestComposeTailscalePolicyFirst(t *testing.T) {
	t.Parallel()
	in := Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890, TUN: true}, Tailscale: fullTailscale}
	in.DNS.TakeOver = true
	in.DNS.Nameserver = []string{"https://doh.pub/dns-query"}
	in.DNS.Policy = append(in.DNS.Policy, model.KV{Key: "geosite:geolocation-!cn", Value: "https://1.1.1.1/dns-query"})
	cfg, _ := composeTailscale(t, in)
	pol := cfg.DNS.Policy.Content
	if len(pol) == 0 || pol[0].Value != "+.tail1234.ts.net" || pol[len(pol)-2].Value != "geosite:geolocation-!cn" {
		t.Fatalf("policy keys %v", keysOf(pol))
	}
}

func keysOf(m []*yaml.Node) []string {
	var ks []string
	for i := 0; i < len(m); i += 2 {
		ks = append(ks, m[i].Value)
	}
	return ks
}

func TestComposeTailscaleTUNOff(t *testing.T) {
	t.Parallel()
	in := Input{Subscription: []byte(testSub), Managed: Managed{MixedPort: 7890}, Tailscale: fullTailscale}
	cfg, _ := composeTailscale(t, in)
	if len(cfg.TUN.Exclude) != 0 {
		t.Errorf("route-exclude-address without TUN: %v", cfg.TUN.Exclude)
	}
	if len(cfg.Rules) == 0 || cfg.Rules[0] != "DOMAIN-SUFFIX,tail1234.ts.net,LANFLOW-TAILSCALE" {
		t.Errorf("rules = %v", cfg.Rules)
	}
}

// Without Tailscale, a rule or group written for its outbound must not
// make the core refuse the config.
func TestComposeTailscaleAbsentScrubs(t *testing.T) {
	t.Parallel()
	sub := `proxies:
  - {name: 东京 01, type: ss, server: 1.1.1.1, port: 8388, cipher: aes-128-gcm, password: pw}
proxy-groups:
  - {name: 节点选择, type: select, proxies: [东京 01, LANFLOW-TAILSCALE, DIRECT]}
rules:
  - DOMAIN-SUFFIX,tail1234.ts.net,LANFLOW-TAILSCALE
  - IP-CIDR,100.64.0.0/10,LANFLOW-TAILSCALE,no-resolve
  - MATCH,节点选择
`
	cfg, _ := composeTailscale(t, Input{Subscription: []byte(sub), Managed: Managed{MixedPort: 7890}})
	for _, p := range cfg.Proxies {
		if p.Name == TailscaleOutbound {
			t.Fatal("outbound left without Tailscale")
		}
	}
	if !hasItem(cfg.Rules, "DOMAIN-SUFFIX,tail1234.ts.net,DIRECT") || !hasItem(cfg.Rules, "IP-CIDR,100.64.0.0/10,DIRECT,no-resolve") {
		t.Errorf("rules = %v", cfg.Rules)
	}
	for _, g := range cfg.Groups {
		if hasItem(g.Proxies, TailscaleOutbound) {
			t.Errorf("group %s still lists it", g.Name)
		}
	}
}

func TestReplaceTarget(t *testing.T) {
	for in, want := range map[string]string{
		"DOMAIN,a.b,LANFLOW-TAILSCALE":                    "DOMAIN,a.b,DIRECT",
		"IP-CIDR,1.0.0.0/8, LANFLOW-TAILSCALE,no-resolve": "IP-CIDR,1.0.0.0/8,DIRECT,no-resolve",
		"DOMAIN,LANFLOW-TAILSCALE-x.com,节点选择":             "DOMAIN,LANFLOW-TAILSCALE-x.com,节点选择",
	} {
		if got := replaceTarget(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

// TestComposeTailscaleMihomoCheck runs the layer through `mihomo -t` when
// the binary is available: the bound direct outbound and the
// "server#outbound" nameservers must be accepted.
func TestComposeTailscaleMihomoCheck(t *testing.T) {
	t.Parallel()
	bin := mihomoTestPath()
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("%s not present", bin)
	}
	in := Input{Subscription: []byte(autoGroupSub), Managed: Managed{MixedPort: 17891, TUN: true, TUNStack: "mixed"}, Tailscale: fullTailscale, Coexist: fullCoexist}
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
}
