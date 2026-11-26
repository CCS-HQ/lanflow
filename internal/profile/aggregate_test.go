package profile

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

const aggYAMLSource = `common: &common
  udp: true
  cipher: aes-128-gcm
proxies:
  - {name: "🇭🇰 香港 01", type: ss, server: hk1.example.com, port: 8388, password: p1, cipher: aes-128-gcm}
  - <<: *common
    name: "日本 东京 02"
    type: ss
    server: jp.example.com
    port: 8388
    password: p2
  - {name: "剩余流量：10 GB", type: ss, server: info.example.com, port: 1, password: x, cipher: aes-128-gcm}
  - {name: "broken"}
`

func aggLinks() string {
	user := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret"))
	vmess := base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"US 洛杉矶","add":"us.example.com","port":"443","id":"11111111-1111-1111-1111-111111111111","aid":0,"net":"ws","path":"/ws","tls":"tls"}`))
	return strings.Join([]string{
		"ss://" + user + "@sg.example.com:8388#Singapore%2001",
		"vmess://" + vmess,
		"trojan://pw@tw.example.com:443?sni=tw.example.com#%E5%8F%B0%E6%B9%BE%2001",
		"hysteria2://auth@hy.example.com:443?sni=hy.example.com#HY2",
		"ssr://notsupported",
	}, "\n")
}

func aggSpec(sources ...model.AggregateSource) model.AggregateSpec {
	spec := DefaultAggregateSpec()
	spec.Sources = sources
	return spec
}

func aggInputs(spec model.AggregateSpec, data ...string) []AggregateInput {
	var in []AggregateInput
	for i, s := range spec.Sources {
		in = append(in, AggregateInput{Source: s, Data: []byte(data[i])})
	}
	return in
}

func TestBuildAggregateMergesSources(t *testing.T) {
	t.Parallel()
	spec := aggSpec(
		model.AggregateSource{ID: "a", Kind: model.AggSourceURL, URL: "https://a.example.com/sub", Prefix: "A·"},
		model.AggregateSource{ID: "b", Kind: model.AggSourceLinks, Links: "x"},
	)
	// 完整版: region groups, and the extended rule sets.
	spec.Template, spec.Groups = "full", AggTemplate("full").Groups()
	b64 := base64.StdEncoding.EncodeToString([]byte(aggLinks()))
	pv, data, err := BuildAggregate(spec, aggInputs(spec, aggYAMLSource, b64), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range pv.Nodes {
		names = append(names, n.Name)
	}
	want := []string{"A·🇭🇰 香港 01", "A·日本 东京 02", "Singapore 01", "US 洛杉矶", "台湾 01", "HY2"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("nodes = %q, want %q", names, want)
	}
	if r := pv.Sources[0]; r.Found != 3 || r.Kept != 2 || r.Skipped != 1 {
		t.Fatalf("yaml source report = %+v", r)
	}
	if r := pv.Sources[1]; r.Found != 4 || r.Kept != 4 || r.Skipped != 1 {
		t.Fatalf("links source report = %+v", r)
	}
	y := string(data)
	if strings.Contains(y, "&common") || strings.Contains(y, "<<") || strings.Contains(y, "*common") {
		t.Fatalf("anchors leaked into the profile:\n%s", y)
	}
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Type    string   `yaml:"type"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Proxies[1]["udp"] != true || doc.Proxies[1]["cipher"] != "aes-128-gcm" {
		t.Fatalf("merge key not expanded: %v", doc.Proxies[1])
	}
	groups := map[string][]string{}
	var order []string
	for _, g := range doc.Groups {
		groups[g.Name] = g.Proxies
		order = append(order, g.Name)
	}
	sel, auto, final := AggModule(AggModSelect).Name(), AggModule(AggModAuto).Name(), AggModule(AggModFinal).Name()
	if order[0] != sel || order[1] != auto {
		t.Fatalf("group order = %q", order)
	}
	if got := groups["🇭🇰 香港节点"]; len(got) != 1 || got[0] != "A·🇭🇰 香港 01" {
		t.Fatalf("香港节点 = %q", got)
	}
	if got := groups["🇺🇸 美国节点"]; len(got) != 1 || got[0] != "US 洛杉矶" {
		t.Fatalf("美国节点 = %q", got)
	}
	if _, ok := groups["🇰🇷 韩国节点"]; ok {
		t.Fatal("a region that was not chosen was generated")
	}
	if !strings.Contains(y, "GEOSITE,perplexity,🤖 AI 服务") || !strings.Contains(y, "GEOSITE,openai,🤖 AI 服务") {
		t.Fatal("the extended rule sets of 完整版 are missing")
	}
	if got := groups["🍏 苹果服务"]; got[0] != "DIRECT" {
		t.Fatalf("苹果服务 = %q, want DIRECT first", got)
	}
	if got := groups["🛑 广告拦截"]; got[0] != "REJECT" {
		t.Fatalf("广告拦截 = %q", got)
	}
	if got := groups[sel]; got[0] != auto || got[len(got)-1] != "DIRECT" {
		t.Fatalf("节点选择 = %q", got)
	}
	if doc.Rules[len(doc.Rules)-1] != "MATCH,"+final {
		t.Fatalf("last rule = %q", doc.Rules[len(doc.Rules)-1])
	}
	for _, r := range []string{"GEOIP,CN,🔒 国内服务,no-resolve", "GEOIP,LAN,🏠 私有网络,no-resolve", "GEOSITE,youtube,📹 YouTube", "GEOSITE,geolocation-!cn,🌍 非中国", "IP-CIDR,91.108.4.0/22,📲 Telegram,no-resolve"} {
		if !contains(doc.Rules, r) {
			t.Fatalf("no rule %q in %q", r, doc.Rules)
		}
	}
	if len(pv.GroupViews) != len(doc.Groups) || len(pv.RuleViews) == 0 || pv.RuleSets == 0 {
		t.Fatalf("views: %d groups, %d rules, %d sets", len(pv.GroupViews), len(pv.RuleViews), pv.RuleSets)
	}
	if len(pv.Warnings) != 0 {
		t.Fatalf("warnings = %q", pv.Warnings)
	}
	if pv.Rules != len(doc.Rules) || len(pv.Groups) != len(doc.Groups) {
		t.Fatalf("preview counts %d/%d, profile %d/%d", pv.Rules, len(pv.Groups), len(doc.Rules), len(doc.Groups))
	}
	if err := CheckYAML(data); err != nil {
		t.Fatal(err)
	}
	checkAggregateWithCore(t, data)
}

func TestBuildAggregateFilterRenameDedupe(t *testing.T) {
	t.Parallel()
	src := `proxies:
  - {name: "HK 01", type: ss, server: a, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "HK 01", type: ss, server: b, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "节点选择", type: ss, server: c, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "🚀 节点选择", type: ss, server: c2, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "LANFLOW-PROBE", type: ss, server: d, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "JP 01", type: ss, server: e, port: 1, password: p, cipher: aes-128-gcm}
`
	spec := model.AggregateSpec{
		Version: model.AggregateSpecVersion,
		Sources: []model.AggregateSource{{ID: "a", Kind: model.AggSourceSub, SubID: "s1"}},
		Exclude: "JP\n\nTW",
		Renames: []model.RenameRule{{Pattern: `^HK (\d+)$`, Replace: "香港-$1"}},
		Groups:  []model.AggregateGroup{{ID: AggModSelect}, {ID: AggModFinal}},
	}
	pv, data, err := BuildAggregate(spec, aggInputs(spec, src), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range pv.Nodes {
		names = append(names, n.Name)
	}
	if got := strings.Join(names, "|"); got != "香港-01|香港-01 2|节点选择|🚀 节点选择 2|PROBE" {
		t.Fatalf("names = %q", got)
	}
	if strings.Contains(string(data), "自动选择") {
		t.Fatal("自动选择 generated with AutoGroup off")
	}
	spec.Include = "^HK\n^LANFLOW"
	pv, _, _ = BuildAggregate(spec, aggInputs(spec, src), nil)
	if len(pv.Nodes) != 3 {
		t.Fatalf("include kept %d nodes", len(pv.Nodes))
	}
	checkAggregateWithCore(t, data)
}

func TestBuildAggregateSourceFailures(t *testing.T) {
	t.Parallel()
	spec := aggSpec(
		model.AggregateSource{ID: "a", Kind: model.AggSourceURL, URL: "https://a.example.com"},
		model.AggregateSource{ID: "b", Kind: model.AggSourceLinks, Links: "x"},
	)
	in := aggInputs(spec, "", aggLinks())
	in[0].Err = errors.New("下载失败：HTTP 404")
	pv, _, err := BuildAggregate(spec, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Sources[0].Error == "" || len(pv.Nodes) != 4 {
		t.Fatalf("report = %+v, nodes %d", pv.Sources, len(pv.Nodes))
	}
	// a stray line among typed links is skipped, not fatal
	in[1].Data = []byte(aggLinks() + "\nnot a link")
	if pv, _, err = BuildAggregate(spec, in, nil); err != nil || pv.Sources[1].Kept != 4 || pv.Sources[1].Skipped != 2 {
		t.Fatalf("stray line: err %v, report %+v", err, pv.Sources[1])
	}
	in[1].Data = []byte("just some text")
	if _, _, err := BuildAggregate(spec, in, nil); err == nil || !strings.Contains(err.Error(), "没有可用的节点") {
		t.Fatalf("err = %v", err)
	}
	// a profile whose nodes are all in providers is reported
	in[1].Data = []byte("proxy-providers:\n  p: {type: http, url: 'https://x', path: ./p.yaml}\n")
	pv, _, err = BuildAggregate(spec, in, nil)
	if err == nil || len(pv.Warnings) != 1 {
		t.Fatalf("err = %v, warnings %q", err, pv.Warnings)
	}
}

func TestValidateAggregateSpec(t *testing.T) {
	t.Parallel()
	ok := model.AggregateSource{ID: "a", Kind: model.AggSourceLinks, Links: "ss://x"}
	for _, tc := range []struct {
		name string
		spec model.AggregateSpec
		want string
	}{
		{"no sources", model.AggregateSpec{}, "至少需要一个启用的来源"},
		{"all disabled", model.AggregateSpec{Sources: []model.AggregateSource{{ID: "a", Kind: model.AggSourceLinks, Links: "x", Disabled: true}}}, "至少需要一个启用的来源"},
		{"bad url", model.AggregateSpec{Sources: []model.AggregateSource{{Kind: model.AggSourceURL, URL: "ftp://x"}}}, "订阅链接无效"},
		{"empty links", model.AggregateSpec{Sources: []model.AggregateSource{{Kind: model.AggSourceLinks}}}, "没有填写节点链接"},
		{"no sub", model.AggregateSpec{Sources: []model.AggregateSource{{Kind: model.AggSourceSub}}}, "没有选择订阅"},
		{"bad kind", model.AggregateSpec{Sources: []model.AggregateSource{{Kind: "x"}}}, "类型无效"},
		{"dup id", model.AggregateSpec{Sources: []model.AggregateSource{ok, ok}}, "ID 重复"},
		{"bad regexp", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Exclude: "x\n("}, "「排除」第 2 行的正则表达式无效"},
		{"bad rename", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Renames: []model.RenameRule{{Pattern: "["}}}, "重命名规则的正则表达式无效"},
		{"empty rename", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Renames: []model.RenameRule{{Replace: "x"}}}, "缺少要匹配的内容"},
		{"module", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Groups: []model.AggregateGroup{{ID: "x"}}}), "未知的代理组"},
		{"dup group", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Groups: []model.AggregateGroup{{ID: "ads"}, {ID: "ads"}}}), "代理组重复"},
		{"dup name", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Groups: []model.AggregateGroup{{ID: "ads"}, {ID: "c1", Custom: true, Name: "🛑 广告拦截"}}}), "名称重复"},
		{"custom no name", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Groups: []model.AggregateGroup{{ID: "c1", Custom: true}}}), "没有名称"},
		{"bad type", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Groups: []model.AggregateGroup{{ID: "ads", Type: "x"}}}), "类型无效"},
		{"bad set", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Groups: []model.AggregateGroup{{ID: "c1", Custom: true, Name: "C", Rules: []string{"geosite:nope-nope"}}}}), "规则集无效"},
		{"relay", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Relays: []model.AggregateRelay{{Name: "R", Relay: "x"}}}), "落地节点"},
		{"rule target", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Rules: []model.AggregateRule{{Type: "DOMAIN", Value: "a.com", Target: "nope"}}}), "目标代理组不存在"},
		{"rule cidr", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Rules: []model.AggregateRule{{Type: "IP-CIDR", Value: "1.2.3", Target: "DIRECT"}}}), "IP 段"},
		{"rule set", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Rules: []model.AggregateRule{{Type: "GEOSITE", Value: "nope-nope", Target: "DIRECT"}}}), "规则集不存在"},
		{"rule source", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, RuleSource: "x"}), "规则来源无效"},
		{"rule base", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, RuleSource: model.AggRulesRemote, RuleBaseURL: "x"}), "规则集 URL 无效"},
		{"listen", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, ListenBase: 80}), "监听端口"},
		{"base", v2(model.AggregateSpec{Sources: []model.AggregateSource{ok}, Base: "- a"}), "基础和 DNS 配置"},
		{"yaml source", model.AggregateSpec{Sources: []model.AggregateSource{{Kind: model.AggSourceYAML}}}, "没有填写 YAML"},
		{"ok", model.AggregateSpec{Sources: []model.AggregateSource{ok}}, ""},
	} {
		err := ValidateAggregateSpec(tc.spec, nil)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// the catalog: unique IDs and names, filters compile, rule sets exist
	seen := map[string]bool{}
	for _, m := range AggregateModules {
		if seen["m:"+m.ID] || seen[m.Name()] {
			t.Errorf("module %s duplicated", m.ID)
		}
		seen["m:"+m.ID], seen[m.Name()] = true, true
		if _, err := regexp.Compile(m.Filter); err != nil {
			t.Errorf("module %s: filter: %v", m.ID, err)
		}
		for _, r := range append(append([]string(nil), m.Rules...), m.Extra...) {
			if !validRuleSetRef(r) {
				t.Errorf("module %s: rule set %q not in the library", m.ID, r)
			}
			if seen["r:"+r] {
				t.Errorf("module %s: rule set %q is another module's too", m.ID, r)
			}
			seen["r:"+r] = true
		}
	}
	// The templates' sizes, as the quick mode shows them.
	sizes := map[string][2]int{"lite": {7, 6}, "standard": {14, 21}, "full": {33, 91}}
	for _, tp := range AggregateTemplates {
		for _, id := range tp.Modules {
			if AggModule(id) == nil {
				t.Errorf("template %s: unknown module %s", tp.ID, id)
			}
		}
		if got := [2]int{len(tp.Groups()), tp.RuleSets()}; got != sizes[tp.ID] {
			t.Errorf("template %s: %d groups, %d rule sets", tp.ID, got[0], got[1])
		}
	}
}

func v2(s model.AggregateSpec) model.AggregateSpec {
	s.Version = model.AggregateSpecVersion
	return s
}

func TestStoreAggregateSpec(t *testing.T) {
	t.Parallel()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub, err := st.Put(model.Subscription{Name: "聚合", Source: model.SourceAggregate})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AggregateSpec(sub.ID); ok {
		t.Fatal("spec before it was set")
	}
	spec := aggSpec(model.AggregateSource{ID: "a", Kind: model.AggSourceLinks, Links: "ss://x"})
	if err := st.SetAggregateSpec(sub.ID, spec); err != nil {
		t.Fatal(err)
	}
	got, ok := st.AggregateSpec(sub.ID)
	if !ok || got.Sources[0].Links != "ss://x" || got.Exclude != DefaultAggregateExclude {
		t.Fatalf("spec = %+v", got)
	}
	if err := st.Delete(sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AggregateSpec(sub.ID); ok {
		t.Fatal("spec kept after delete")
	}
	if err := st.SetAggregateSpec(sub.ID, spec); err == nil {
		t.Fatal("spec set for a deleted subscription")
	}
}

// checkAggregateWithCore runs `mihomo -t` on an aggregate profile, with
// the geo databases Lanflow ships so the GEOSITE / GEOIP rules load
// without a download; skipped without ./bin/mihomo and ./bin/geo.
func checkAggregateWithCore(t *testing.T, y []byte) {
	t.Helper()
	bin, _ := filepath.Abs(mihomoTestPath())
	geo, _ := filepath.Abs(filepath.Join("..", "..", "bin", "geo"))
	if _, err := os.Stat(bin); err != nil {
		t.Logf("%s not present: core validation skipped", bin)
		return
	}
	dir := t.TempDir()
	for _, f := range []string{"geosite.dat", "geoip.metadb"} {
		data, err := os.ReadFile(filepath.Join(geo, f))
		if err != nil {
			t.Logf("%s not present: core validation skipped", f)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(f, y, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-d", dir, "-f", f).CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t: %v\n%s\n%s", err, out, y)
	}
}
