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
	if order[0] != AggGroupSelect || order[1] != AggGroupAuto {
		t.Fatalf("group order = %q", order)
	}
	if got := groups["香港节点"]; len(got) != 1 || got[0] != "A·🇭🇰 香港 01" {
		t.Fatalf("香港节点 = %q", got)
	}
	if got := groups["美国节点"]; len(got) != 1 || got[0] != "US 洛杉矶" {
		t.Fatalf("美国节点 = %q", got)
	}
	if _, ok := groups["韩国节点"]; ok {
		t.Fatal("a region that was not chosen was generated")
	}
	if got := groups["苹果服务"]; got[0] != "DIRECT" {
		t.Fatalf("苹果服务 = %q, want DIRECT first", got)
	}
	if got := groups["广告拦截"]; got[0] != "REJECT" {
		t.Fatalf("广告拦截 = %q", got)
	}
	if doc.Rules[len(doc.Rules)-1] != "MATCH,"+AggGroupFinal {
		t.Fatalf("last rule = %q", doc.Rules[len(doc.Rules)-1])
	}
	if !contains(doc.Rules, "GEOIP,CN,DIRECT,no-resolve") || !contains(doc.Rules, "GEOSITE,youtube,YouTube") || !contains(doc.Rules, "GEOSITE,geolocation-!cn,"+AggGroupSelect) {
		t.Fatalf("rules = %q", doc.Rules)
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
  - {name: "LANFLOW-PROBE", type: ss, server: d, port: 1, password: p, cipher: aes-128-gcm}
  - {name: "JP 01", type: ss, server: e, port: 1, password: p, cipher: aes-128-gcm}
`
	spec := model.AggregateSpec{
		Sources: []model.AggregateSource{{ID: "a", Kind: model.AggSourceSub, SubID: "s1"}},
		Exclude: "JP",
		Renames: []model.RenameRule{{Pattern: `^HK (\d+)$`, Replace: "香港-$1"}},
	}
	pv, data, err := BuildAggregate(spec, aggInputs(spec, src), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range pv.Nodes {
		names = append(names, n.Name)
	}
	if got := strings.Join(names, "|"); got != "香港-01|香港-01 2|节点选择 2|PROBE" {
		t.Fatalf("names = %q", got)
	}
	if strings.Contains(string(data), "自动选择") {
		t.Fatal("自动选择 generated with AutoGroup off")
	}
	spec.Include = "^HK"
	pv, _, _ = BuildAggregate(spec, aggInputs(spec, src), nil)
	if len(pv.Nodes) != 2 {
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
		{"bad regexp", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Exclude: "("}, "「排除」的正则表达式无效"},
		{"bad rename", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Renames: []model.RenameRule{{Pattern: "["}}}, "重命名规则的正则表达式无效"},
		{"empty rename", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Renames: []model.RenameRule{{Replace: "x"}}}, "缺少要匹配的内容"},
		{"region", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Regions: []string{"mars"}}, "未知的地区分组"},
		{"module", model.AggregateSpec{Sources: []model.AggregateSource{ok}, Modules: []string{"x"}}, "未知的分流模块"},
		{"ok", model.AggregateSpec{Sources: []model.AggregateSource{ok}}, ""},
	} {
		err := ValidateAggregateSpec(tc.spec, nil)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// the catalogs compile and have unique IDs and group names
	seen := map[string]bool{}
	for _, r := range AggregateRegions {
		if seen[r.ID] || seen[r.Group] {
			t.Errorf("region %s duplicated", r.ID)
		}
		seen[r.ID], seen[r.Group] = true, true
		if _, err := regexp.Compile(aggRegionMatch[r.ID]); err != nil || aggRegionMatch[r.ID] == "" {
			t.Errorf("region %s: match %q: %v", r.ID, aggRegionMatch[r.ID], err)
		}
	}
	for _, m := range AggregateModules {
		if seen["m:"+m.ID] || seen[m.Group] {
			t.Errorf("module %s duplicated", m.ID)
		}
		seen["m:"+m.ID], seen[m.Group] = true, true
	}
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
