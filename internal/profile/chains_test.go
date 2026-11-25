package profile

import (
	"slices"
	"strings"
	"testing"

	"lanflow/internal/model"
)

const chainSub = `proxies:
  - {name: A, type: socks5, server: 127.0.0.1, port: 1081}
  - {name: B, type: socks5, server: 127.0.0.1, port: 1082, udp: true}
  - {name: C, type: ss, server: 1.1.1.3, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: W, type: wireguard, server: 1.1.1.4, port: 51820, ip: 10.0.0.2, private-key: eCtXsJZ27+4PbhDkHnB923tkUn2Gj59wZw5wFA75MnU=, public-key: Cr8hWlKvtDt7nrvf+f0brNQQzabAqrjfBvas9pmowjo=}
proxy-providers:
  sub:
    type: http
    url: https://example.com/sub
    path: ./providers/sub.yaml
proxy-groups:
  - name: 节点选择
    type: select
    proxies: [自动选择, 香港, A, B, C, DIRECT]
  - name: 自动选择
    type: url-test
    include-all: true
    url: https://www.gstatic.com/generate_204
    interval: 300
  - name: 香港
    type: select
    use: [sub]
  - name: 筛选
    type: url-test
    include-all-proxies: true
    filter: "(?i)b|c"
    url: https://www.gstatic.com/generate_204
    interval: 300
  - name: 外层
    type: select
    proxies: [节点选择, DIRECT]
rules:
  - GEOIP,CN,DIRECT
  - MATCH,节点选择
`

func composeChains(t *testing.T, sub string, chains []model.ProxyChain, rules []model.CustomRule) (map[string]any, Output) {
	t.Helper()
	out, err := compose(Input{Subscription: []byte(sub), ProxyChains: chains, CustomRules: rules, Managed: Managed{ProbePort: 7999}})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	return decode(t, out.YAML), out
}

func proxyByName(m map[string]any, name string) map[string]any {
	for _, p := range m["proxies"].([]any) {
		if pm := p.(map[string]any); pm["name"] == name {
			return pm
		}
	}
	return nil
}

func groupByName(m map[string]any, name string) map[string]any {
	for _, g := range m["proxy-groups"].([]any) {
		if gm := g.(map[string]any); gm["name"] == name {
			return gm
		}
	}
	return nil
}

func hasWarning(out Output, sub string) bool {
	return slices.ContainsFunc(out.Warnings, func(w string) bool { return strings.Contains(w, sub) })
}

func TestChainClonesAndGroups(t *testing.T) {
	t.Parallel()
	m, out := composeChains(t, chainSub, []model.ProxyChain{
		{ID: "c1", Name: "中转", Hops: []string{"A", "B", "C"}, Enabled: true},
	}, nil)

	b := proxyByName(m, ChainCloneName("c1", 2))
	c := proxyByName(m, ChainCloneName("c1", 3))
	if b == nil || c == nil {
		t.Fatalf("clones missing:\n%s", out.YAML)
	}
	if b["dialer-proxy"] != "A" || c["dialer-proxy"] != ChainCloneName("c1", 2) {
		t.Fatalf("dialer-proxy: %v %v", b["dialer-proxy"], c["dialer-proxy"])
	}
	if b["server"] != "127.0.0.1" || b["port"] != 1082 || c["type"] != "ss" || c["password"] != "pw" {
		t.Fatalf("clone does not copy the node: %v %v", b, c)
	}
	// originals untouched
	if proxyByName(m, "B")["dialer-proxy"] != nil || proxyByName(m, "C")["dialer-proxy"] != nil {
		t.Fatal("original node changed")
	}
	g := groupByName(m, "中转")
	if g == nil || g["type"] != "select" || !slices.Equal(toStrings(g["proxies"]), []string{ChainCloneName("c1", 3)}) {
		t.Fatalf("chain group: %v", g)
	}
	cg := groupByName(m, ChainGroupName)
	if cg == nil || cg["type"] != "select" || !slices.Equal(toStrings(cg["proxies"]), []string{"中转"}) {
		t.Fatalf("container: %v", cg)
	}
	if first := toStrings(groupByName(m, "节点选择")["proxies"]); first[len(first)-1] != ChainGroupName {
		t.Fatalf("container not appended to the first select group: %v", first)
	}
	if !slices.Contains(out.Groups, "中转") || slices.Contains(out.Groups, "GLOBAL") {
		t.Fatalf("groups: %v", out.Groups)
	}
	checkWithCore(t, out.YAML)
}

func TestChainClonesExcludedFromCollectingGroups(t *testing.T) {
	t.Parallel()
	m, out := composeChains(t, chainSub, []model.ProxyChain{
		{ID: "c1", Name: "中转", Hops: []string{"香港", "B"}, Enabled: true},
	}, nil)
	if f := groupByName(m, "自动选择")["filter"]; f != chainCloneFilter {
		t.Fatalf("include-all filter = %v", f)
	}
	if f := groupByName(m, "筛选")["filter"]; f != chainCloneFilter+"(?=.*?(?:(?i)b|c))" {
		t.Fatalf("include-all-proxies filter = %v", f)
	}
	probe := groupByName(m, probeGroupName)
	if probe["filter"] != chainCloneFilter || !slices.Contains(toStrings(probe["proxies"]), "中转") {
		t.Fatalf("probe group: %v", probe)
	}
	global := groupByName(m, "GLOBAL")
	if global == nil {
		t.Fatalf("no GLOBAL written:\n%s", out.YAML)
	}
	gm := toStrings(global["proxies"])
	if slices.ContainsFunc(gm, IsChainClone) {
		t.Fatalf("GLOBAL has clones: %v", gm)
	}
	for _, want := range []string{"DIRECT", "REJECT", "A", "B", "节点选择", "中转", ChainGroupName} {
		if !slices.Contains(gm, want) {
			t.Fatalf("GLOBAL lacks %s: %v", want, gm)
		}
	}
	if gm[0] != "DIRECT" || gm[2] != "A" {
		t.Fatalf("GLOBAL order: %v", gm)
	}
	checkWithCore(t, out.YAML)

	// A GLOBAL the configuration defines stays its own.
	sub := chainSub + "  - DOMAIN,x.com,GLOBAL\n"
	sub = strings.Replace(sub, "  - name: 外层\n", "  - name: GLOBAL\n    type: select\n    proxies: [节点选择]\n  - name: 外层\n", 1)
	m, _ = composeChains(t, sub, []model.ProxyChain{{ID: "c1", Name: "中转", Hops: []string{"A", "B"}, Enabled: true}}, nil)
	if !slices.Equal(toStrings(groupByName(m, "GLOBAL")["proxies"]), []string{"节点选择"}) {
		t.Fatal("configuration's GLOBAL rewritten")
	}

	// no chains: nothing changes
	m, _ = composeChains(t, chainSub, nil, nil)
	if groupByName(m, "GLOBAL") != nil || groupByName(m, "自动选择")["filter"] != nil || groupByName(m, ChainGroupName) != nil {
		t.Fatal("collecting groups touched without chains")
	}
}

func TestChainContainerName(t *testing.T) {
	t.Parallel()
	// a non-select group of the same name: the alternative name
	sub := strings.Replace(chainSub, "  - name: 外层\n", "  - name: 链式代理\n    type: url-test\n    proxies: [A]\n    url: https://www.gstatic.com/generate_204\n  - name: 外层\n", 1)
	m, out := composeChains(t, sub, []model.ProxyChain{{ID: "c1", Name: "中转", Hops: []string{"A", "B"}, Enabled: true}}, nil)
	if g := groupByName(m, ChainGroupAltName); g == nil || !slices.Equal(toStrings(g["proxies"]), []string{"中转"}) {
		t.Fatalf("alt container: %v\n%s", g, out.YAML)
	}
	if !slices.Equal(toStrings(groupByName(m, ChainGroupName)["proxies"]), []string{"A"}) {
		t.Fatal("subscription's group changed")
	}
	checkWithCore(t, out.YAML)

	// a select group of the same name gets the chains appended
	sub = strings.Replace(chainSub, "  - name: 外层\n", "  - name: 链式代理\n    type: select\n    proxies: [DIRECT]\n  - name: 外层\n", 1)
	m, _ = composeChains(t, sub, []model.ProxyChain{{ID: "c1", Name: "中转", Hops: []string{"A", "B"}, Enabled: true}}, nil)
	if !slices.Equal(toStrings(groupByName(m, ChainGroupName)["proxies"]), []string{"DIRECT", "中转"}) {
		t.Fatalf("select container: %v", groupByName(m, ChainGroupName))
	}

	// no enabled chain: no empty container
	m, _ = composeChains(t, chainSub, []model.ProxyChain{{ID: "c1", Name: "中转", Hops: []string{"A", "B"}}}, nil)
	if groupByName(m, ChainGroupName) != nil || groupByName(m, "中转") != nil || proxyByName(m, ChainCloneName("c1", 2)) != nil {
		t.Fatal("disabled chain generated")
	}
}

func TestChainValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ch   model.ProxyChain
		warn string
	}{
		{"missing hop", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"A", "不存在"}}, "不在当前配置的 proxies 里"},
		{"missing entry", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"不存在", "B"}}, "不在当前配置的节点或代理组里"},
		{"provider node as exit", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"A", "香港 01"}}, "订阅提供方的节点不能做中间或落地"},
		{"duplicate hop", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"A", "B", "A"}}, "出现了两次"},
		{"group in the middle", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"A", "香港", "B"}}, "是代理组"},
		{"one hop", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"A"}}, "2–5 跳"},
		{"six hops", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"A", "B", "C", "W", "D", "E"}}, "2–5 跳"},
		{"reserved hop", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"A", "LANFLOW-MITM-abc"}}, "澜流生成的出站"},
		{"entry is the first select group", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"节点选择", "B"}}, "形成环路"},
		{"entry contains the first select group", model.ProxyChain{ID: "x", Name: "X", Hops: []string{"外层", "B"}}, "形成环路"},
	}
	for _, tc := range cases {
		tc.ch.Enabled = true
		m, out := composeChains(t, chainSub, []model.ProxyChain{tc.ch}, nil)
		if !hasWarning(out, tc.warn) {
			t.Errorf("%s: no warning %q: %v", tc.name, tc.warn, out.Warnings)
		}
		if groupByName(m, "X") != nil || groupByName(m, ChainGroupName) != nil {
			t.Errorf("%s: invalid chain generated", tc.name)
		}
		if slices.ContainsFunc(namesOfAny(m["proxies"]), IsChainClone) {
			t.Errorf("%s: clone generated", tc.name)
		}
	}

	// names
	for _, n := range []string{"LANFLOW-X", "lanflow-probe2", "A", "节点选择", "DIRECT", ChainGroupName} {
		m, out := composeChains(t, chainSub, []model.ProxyChain{{ID: "x", Name: n, Hops: []string{"A", "B"}, Enabled: true}}, nil)
		if len(out.Warnings) == 0 || proxyByName(m, ChainCloneName("x", 2)) != nil {
			t.Errorf("name %q accepted: %v", n, out.Warnings)
		}
	}
	if _, err := NormalizeProxyChain(model.ProxyChain{Name: "LANFLOW-CHAIN-1", Hops: []string{"A", "B"}}); err == nil {
		t.Error("reserved chain name accepted")
	}
	// one bad chain does not stop the others
	m, _ := composeChains(t, chainSub, []model.ProxyChain{
		{ID: "x", Name: "X", Hops: []string{"A", "不存在"}, Enabled: true},
		{ID: "y", Name: "Y", Hops: []string{"A", "B"}, Enabled: true},
	}, nil)
	if groupByName(m, "Y") == nil || !slices.Equal(toStrings(groupByName(m, ChainGroupName)["proxies"]), []string{"Y"}) {
		t.Fatal("valid chain dropped with an invalid one")
	}
}

func namesOfAny(v any) []string {
	var out []string
	for _, p := range v.([]any) {
		out = append(out, p.(map[string]any)["name"].(string))
	}
	return out
}

func TestChainPlaceholder(t *testing.T) {
	t.Parallel()
	rules := []model.CustomRule{{Type: "DOMAIN-SUFFIX", Payload: "example.com", Target: "X"}}
	// disabled and referenced by a custom rule: REJECT stands in
	m, out := composeChains(t, chainSub, []model.ProxyChain{{ID: "x", Name: "X", Hops: []string{"A", "B"}}}, rules)
	if g := groupByName(m, "X"); g == nil || !slices.Equal(toStrings(g["proxies"]), []string{"REJECT"}) {
		t.Fatalf("placeholder: %v", g)
	}
	if !hasWarning(out, "命中它的流量被拒绝") || groupByName(m, ChainGroupName) != nil {
		t.Fatalf("warnings: %v", out.Warnings)
	}
	if !slices.Contains(toStrings(m["rules"]), "DOMAIN-SUFFIX,example.com,X") {
		t.Fatal("custom rule lost")
	}
	checkWithCore(t, out.YAML)

	// invalid (hop gone after a subscription update), referenced by a rule
	// of the configuration: the same
	sub := strings.Replace(chainSub, "  - GEOIP,CN,DIRECT\n", "  - GEOIP,CN,DIRECT\n  - DOMAIN,a.com,X\n", 1)
	m, out = composeChains(t, sub, []model.ProxyChain{{ID: "x", Name: "X", Hops: []string{"A", "不存在"}, Enabled: true}}, nil)
	if g := groupByName(m, "X"); g == nil || !slices.Equal(toStrings(g["proxies"]), []string{"REJECT"}) {
		t.Fatalf("placeholder for an invalid chain: %v", g)
	}
	checkWithCore(t, out.YAML)

	// not referenced: nothing at all
	m, _ = composeChains(t, chainSub, []model.ProxyChain{{ID: "x", Name: "X", Hops: []string{"A", "B"}}}, nil)
	if groupByName(m, "X") != nil {
		t.Fatal("placeholder without a reference")
	}

	// an enabled chain is a valid custom rule target
	m, out = composeChains(t, chainSub, []model.ProxyChain{{ID: "x", Name: "X", Hops: []string{"A", "B"}, Enabled: true}}, rules)
	if g := groupByName(m, "X"); g == nil || toStrings(g["proxies"])[0] != ChainCloneName("x", 2) {
		t.Fatalf("chain as rule target: %v", g)
	}
	checkWithCore(t, out.YAML)
}

func TestChainWarnings(t *testing.T) {
	t.Parallel()
	_, out := composeChains(t, chainSub, []model.ProxyChain{{ID: "x", Name: "X", Hops: []string{"A", "W"}, Enabled: true}}, nil)
	if !hasWarning(out, "WireGuard") {
		t.Fatalf("no WireGuard warning: %v", out.Warnings)
	}
}

// A dialer-proxy the subscription or an override sets stays on the
// original node; only the clone's is replaced (with a warning).
func TestChainKeepsOwnDialerProxy(t *testing.T) {
	t.Parallel()
	in := Input{
		Subscription: []byte(strings.Replace(chainSub, "password: pw}", "password: pw, dialer-proxy: A}", 1)),
		ProxyChains:  []model.ProxyChain{{ID: "x", Name: "X", Hops: []string{"B", "C"}, Enabled: true}},
	}
	out, err := compose(in)
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out.YAML)
	if proxyByName(m, "C")["dialer-proxy"] != "A" {
		t.Fatalf("original dialer-proxy changed:\n%s", out.YAML)
	}
	if proxyByName(m, ChainCloneName("x", 2))["dialer-proxy"] != "B" {
		t.Fatal("clone dialer-proxy not the previous hop")
	}
	if !hasWarning(out, "自带 dialer-proxy") {
		t.Fatalf("no warning: %v", out.Warnings)
	}
	checkWithCore(t, out.YAML)
}
