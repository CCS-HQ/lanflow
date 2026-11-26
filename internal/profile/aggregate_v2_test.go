package profile

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

const aggThreeNodes = `proxies:
  - {name: "香港 01", type: ss, server: hk.example.com, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "日本 01", type: ss, server: jp.example.com, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "中转 SZ", type: ss, server: sz.example.com, port: 1, password: p, cipher: aes-128-gcm, dialer-proxy: other}
`

type aggDoc struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name    string   `yaml:"name"`
		Type    string   `yaml:"type"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	Providers map[string]map[string]any `yaml:"rule-providers"`
	Rules     []string                  `yaml:"rules"`
	Listeners []map[string]any          `yaml:"listeners"`
	MixedPort int                       `yaml:"mixed-port"`
	DNS       map[string]any            `yaml:"dns"`
}

func buildAgg(t *testing.T, spec model.AggregateSpec) (model.AggregatePreview, aggDoc, []byte) {
	t.Helper()
	spec.Version = model.AggregateSpecVersion
	if spec.Sources == nil {
		spec.Sources = []model.AggregateSource{{ID: "y", Kind: model.AggSourceYAML, YAML: aggThreeNodes}}
	}
	pv, data, err := BuildAggregate(spec, aggInputs(spec, aggThreeNodes), nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc aggDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return pv, doc, data
}

func TestUpgradeAggregateSpecV1(t *testing.T) {
	t.Parallel()
	old := model.AggregateSpec{AutoGroup: true, Regions: []string{"hk", "jp"}, Modules: []string{"social", "cn", "games"}}
	got := UpgradeAggregateSpec(old)
	var ids []string
	for _, g := range got.Groups {
		ids = append(ids, g.ID)
	}
	want := "select|auto|private|cn|final|hk|jp|twitter|meta|steam|games"
	if strings.Join(ids, "|") != want || got.Version != model.AggregateSpecVersion || got.Regions != nil || got.AutoGroup {
		t.Fatalf("upgraded = %q %+v", ids, got)
	}
	if again := UpgradeAggregateSpec(got); len(again.Groups) != len(got.Groups) {
		t.Fatal("upgrading twice changed the spec")
	}
}

func TestBuildAggregateRemoteRules(t *testing.T) {
	t.Parallel()
	pv, doc, data := buildAgg(t, model.AggregateSpec{
		Groups:     AggTemplate("lite").Groups(),
		RuleSource: model.AggRulesRemote, RuleBaseURL: "https://rules.example.com/geo/",
	})
	p := doc.Providers["geoip-private"]
	if p == nil || p["url"] != "https://rules.example.com/geo/geoip/private.mrs" || p["behavior"] != "ipcidr" || p["format"] != "mrs" {
		t.Fatalf("providers = %v", doc.Providers)
	}
	if !contains(doc.Rules, "RULE-SET,geoip-cn,🔒 国内服务,no-resolve") || !contains(doc.Rules, "RULE-SET,geosite-geolocation-!cn,🌍 非中国") {
		t.Fatalf("rules = %q", doc.Rules)
	}
	if pv.RuleSets != len(doc.Providers) || len(pv.Warnings) != 0 {
		t.Fatalf("sets %d, providers %d, warnings %q", pv.RuleSets, len(doc.Providers), pv.Warnings)
	}
	checkAggregateWithCore(t, data)
}

func TestBuildAggregateRelayListenBaseAndOrder(t *testing.T) {
	t.Parallel()
	pv, doc, data := buildAgg(t, model.AggregateSpec{
		Groups:     AggTemplate("lite").Groups(),
		Relays:     []model.AggregateRelay{{Name: "🔗 深圳中转", Relay: "中转", Landing: "香港|日本"}},
		ListenBase: 7900,
		NodeOrder:  []string{"日本 01"},
		Hidden:     []string{"no such node"},
		Base:       "mixed-port: 7899\ndns:\n  enable: true\nproxies: []\nrules: [MATCH,DIRECT]\n",
	})
	names := []string{}
	for _, p := range doc.Proxies {
		names = append(names, p["name"].(string))
	}
	if strings.Join(names, "|") != "日本 01|香港 01|中转 SZ" {
		t.Fatalf("order = %q", names)
	}
	if doc.Proxies[0]["dialer-proxy"] != "🔗 深圳中转" || doc.Proxies[2]["dialer-proxy"] != nil {
		t.Fatalf("dialer-proxy: %v", doc.Proxies)
	}
	last := doc.Groups[len(doc.Groups)-1]
	if last.Name != "🔗 深圳中转" || strings.Join(last.Proxies, "|") != "中转 SZ" {
		t.Fatalf("relay group = %+v", last)
	}
	if doc.Groups[0].Proxies[1] != "🔗 深圳中转" {
		t.Fatalf("节点选择 does not offer the relay: %q", doc.Groups[0].Proxies)
	}
	if len(doc.Listeners) != 3 || doc.Listeners[0]["port"] != 7900 || doc.Listeners[0]["proxy"] != "日本 01" {
		t.Fatalf("listeners = %v", doc.Listeners)
	}
	if pv.Nodes[0].Port != 7900 || pv.Nodes[0].Relay != "🔗 深圳中转" {
		t.Fatalf("node view = %+v", pv.Nodes[0])
	}
	if doc.MixedPort != 7899 || doc.DNS["enable"] != true || doc.Rules[len(doc.Rules)-1] == "MATCH,DIRECT" {
		t.Fatalf("base: port %d dns %v rules %q", doc.MixedPort, doc.DNS, doc.Rules)
	}
	checkAggregateWithCore(t, data)
}

func TestBuildAggregateCustomRulesGroupsAndOrder(t *testing.T) {
	t.Parallel()
	groups := append(AggTemplate("lite").Groups(),
		model.AggregateGroup{ID: "c-work", Custom: true, Name: "💼 工作", Type: model.AggTypeSelect, Rules: []string{"geosite:slack"}},
		model.AggregateGroup{ID: "c-hk", Custom: true, Name: "🇭🇰 香港测速", Type: model.AggTypeURLTest, Filter: "香港"},
		model.AggregateGroup{ID: "c-none", Custom: true, Name: "空", Type: model.AggTypeURLTest, Filter: "火星"},
	)
	spec := model.AggregateSpec{
		Groups: groups,
		Rules: []model.AggregateRule{
			{Type: model.AggRuleDomainSuffix, Value: "corp.example.com", Target: "c-work"},
			{Type: model.AggRuleIPCIDR, Value: "10.8.0.0/16", Target: "DIRECT", NoResolve: true},
			{Type: model.AggRuleGeoSite, Value: "cn", Target: "c-work"}, // overrides 国内服务's geosite:cn
		},
	}
	pv, doc, data := buildAgg(t, spec)
	if doc.Rules[0] != "DOMAIN-SUFFIX,corp.example.com,💼 工作" || doc.Rules[1] != "IP-CIDR,10.8.0.0/16,DIRECT,no-resolve" || doc.Rules[2] != "GEOSITE,cn,💼 工作" {
		t.Fatalf("rules = %q", doc.Rules)
	}
	if strings.Count(strings.Join(doc.Rules, "\n"), "GEOSITE,cn,") != 1 {
		t.Fatal("geosite:cn twice")
	}
	byName := map[string][]string{}
	for _, g := range doc.Groups {
		byName[g.Name] = g.Proxies
	}
	if got := byName["🇭🇰 香港测速"]; strings.Join(got, "|") != "香港 01" {
		t.Fatalf("filtered custom group = %q", got)
	}
	if _, ok := byName["空"]; ok {
		t.Fatal("a filtered group with no node was generated")
	}
	checkAggregateWithCore(t, data)

	// RuleOrder moves rules; unlisted ones follow their predecessor, MATCH stays last.
	keys := []string{}
	for _, r := range pv.RuleViews {
		keys = append(keys, r.Key)
	}
	spec.RuleOrder = []string{"geosite:category-ads-all", "DOMAIN-SUFFIX,corp.example.com"}
	pv2, doc2, _ := buildAgg(t, spec)
	at := func(rule string) int {
		for i, r := range doc2.Rules {
			if r == rule {
				return i
			}
		}
		return -1
	}
	ds := at("DOMAIN-SUFFIX,corp.example.com,💼 工作")
	if !strings.HasPrefix(doc2.Rules[0], "GEOSITE,category-ads-all,") || ds <= 0 || doc2.Rules[ds+1] != "IP-CIDR,10.8.0.0/16,DIRECT,no-resolve" ||
		doc2.Rules[ds+2] != "GEOSITE,cn,💼 工作" || at("GEOSITE,private,🏠 私有网络") > ds || !strings.HasPrefix(doc2.Rules[len(doc2.Rules)-1], "MATCH,") {
		t.Fatalf("ordered rules = %q (keys %q)", doc2.Rules, keys)
	}
	if len(pv2.RuleViews) != len(pv.RuleViews) {
		t.Fatal("ordering lost rules")
	}

	// The rule list without nodes matches the built one.
	views := AggregateRuleViews(spec, nil)
	if len(views) != len(pv2.RuleViews)+0 || views[0].Key != pv2.RuleViews[0].Key {
		t.Fatalf("rule views without nodes: %d vs %d", len(views), len(pv2.RuleViews))
	}
}

func TestBuildAggregateHiddenNodes(t *testing.T) {
	t.Parallel()
	pv, doc, _ := buildAgg(t, model.AggregateSpec{Groups: AggTemplate("lite").Groups(), Hidden: []string{"香港 01"}})
	if len(doc.Proxies) != 2 || len(pv.Nodes) != 3 || !pv.Nodes[0].Hidden {
		t.Fatalf("proxies %d, nodes %+v", len(doc.Proxies), pv.Nodes)
	}
	spec := model.AggregateSpec{Version: model.AggregateSpecVersion, Sources: []model.AggregateSource{{ID: "y", Kind: model.AggSourceYAML, YAML: "x"}},
		Hidden: []string{"香港 01", "日本 01", "中转 SZ"}}
	if _, _, err := BuildAggregate(spec, aggInputs(spec, aggThreeNodes), nil); err == nil || !strings.Contains(err.Error(), "隐藏") {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchRuleSets(t *testing.T) {
	t.Parallel()
	got := SearchRuleSets("netflix", 5)
	if len(got) == 0 || got[0].Ref != "geosite:netflix" {
		t.Fatalf("netflix: %+v", got)
	}
	found := false
	for _, s := range SearchRuleSets("netflix", 0) {
		found = found || s.Ref == "geoip:netflix" && s.IP
	}
	if !found {
		t.Fatal("geoip:netflix not offered")
	}
	if len(SearchRuleSets("", 0)) != RuleSetCount() || RuleSetCount() < 1000 {
		t.Fatalf("library has %d sets", RuleSetCount())
	}
}

func TestUnescapeAstral(t *testing.T) {
	t.Parallel()
	in := `a: "\U0001F680 节点选择"` + "\n" + `b: "x\\U0001F680"` + "\n" + `c: "\U0000004A"` + "\n"
	want := `a: "🚀 节点选择"` + "\n" + `b: "x\\U0001F680"` + "\n" + `c: "\U0000004A"` + "\n"
	if got := string(unescapeAstral([]byte(in))); got != want {
		t.Fatalf("got %q", got)
	}
	_, _, data := buildAgg(t, model.AggregateSpec{Groups: AggTemplate("lite").Groups()})
	if !strings.Contains(string(data), "🚀 节点选择") || strings.Contains(string(data), `\U0001F`) {
		t.Fatalf("profile still escapes emoji:\n%s", data)
	}
}

func TestAggregateLinksAcceptYAML(t *testing.T) {
	t.Parallel()
	spec := aggSpec(model.AggregateSource{ID: "a", Kind: model.AggSourceLinks, Links: "x"})
	node := func(name string) string {
		return "{name: " + name + ", type: ss, server: " + name + ".example.com, port: 8388, cipher: aes-128-gcm, password: p}"
	}
	for _, tc := range []struct {
		name, text   string
		kept, skiped int
	}{
		{"list", "- " + node("a") + "\n- " + node("b") + "\n- {name: broken}", 2, 1},
		{"block list", "- name: a\n  type: trojan\n  server: a.example.com\n  port: 443\n  password: p", 1, 0},
		{"one", "name: a\ntype: ss\nserver: a.example.com\nport: 8388\ncipher: aes-128-gcm\npassword: p", 1, 0},
		{"mixed", aggLinks() + "\n- " + node("a") + "\n- " + node("b"), 6, 1},
		{"mixed profile", "proxies:\n  - " + node("a") + "\n" + aggLinks(), 5, 1},
	} {
		in := aggInputs(spec, tc.text)
		pv, _, err := BuildAggregate(spec, in, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if r := pv.Sources[0]; r.Kept != tc.kept || r.Skipped != tc.skiped {
			t.Fatalf("%s: report %+v", tc.name, r)
		}
	}
}

func TestAggregateFullTemplateLoads(t *testing.T) {
	t.Parallel()
	for _, src := range []string{model.AggRulesGeo, model.AggRulesRemote} {
		spec := aggSpec(model.AggregateSource{ID: "a", Kind: model.AggSourceLinks, Links: "x"})
		spec.Template, spec.Groups, spec.RuleSource = "full", AggTemplate("full").Groups(), src
		_, data, err := BuildAggregate(spec, aggInputs(spec, aggLinks()), nil)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		checkAggregateWithCore(t, data)
	}
}

func TestAggregateSSRLinks(t *testing.T) {
	t.Parallel()
	b := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	ssr := "ssr://" + b("1.2.3.4:8388:auth_aes128_md5:aes-256-cfb:tls1.2_ticket_auth:"+b("pw")+
		"/?obfsparam="+b("cdn.example.com")+"&protoparam="+b("1:abc")+"&remarks="+b("香港 SSR")+"&group="+b("g"))
	v6 := "ssr://" + b("2001:db8::1:443:origin:aes-128-ctr:plain:"+b("p2"))
	spec := aggSpec(model.AggregateSource{ID: "a", Kind: model.AggSourceLinks, Links: "x"})
	pv, data, err := BuildAggregate(spec, aggInputs(spec, ssr+"\n"+v6+"\nssr://broken"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := pv.Sources[0]; r.Kept != 2 || r.Skipped != 1 {
		t.Fatalf("report %+v", r)
	}
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	p := doc.Proxies[0]
	if p["name"] != "香港 SSR" || p["type"] != "ssr" || p["server"] != "1.2.3.4" || p["port"] != 8388 || p["password"] != "pw" ||
		p["cipher"] != "aes-256-cfb" || p["obfs"] != "tls1.2_ticket_auth" || p["protocol"] != "auth_aes128_md5" ||
		p["obfs-param"] != "cdn.example.com" || p["protocol-param"] != "1:abc" {
		t.Fatalf("ssr node = %v", p)
	}
	if q := doc.Proxies[1]; q["server"] != "2001:db8::1" || q["port"] != 443 || q["name"] != "[2001:db8::1]:443" {
		t.Fatalf("ipv6 ssr node = %v", q)
	}
	checkAggregateWithCore(t, data)
}
