package proxies

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

func TestChainCandidates(t *testing.T) {
	px := model.ProxySnapshot{
		Groups: []model.ProxyGroup{
			{Name: "节点选择", Type: model.GroupSelect, All: []string{"香港", "A", "B"}},
			{Name: "香港", Type: model.GroupSelect, All: []string{"HK 1"}},
			{Name: "外层", Type: model.GroupSelect, All: []string{"节点选择"}},
			{Name: "中转", Type: model.GroupSelect, All: []string{profile.ChainCloneName("x", 2)}},
			{Name: "绕回", Type: model.GroupURLTest, All: []string{"中转", "A"}},
			{Name: "GLOBAL", Type: model.GroupSelect, All: []string{"DIRECT", "REJECT", "A", "B", "节点选择", "香港", "外层", "中转", "绕回"}},
		},
		Proxies: map[string]model.Proxy{
			"A": {Name: "A", Type: "Socks5", UDP: true}, "B": {Name: "B", Type: "Hysteria2"},
			"HK 1": {Name: "HK 1", Provider: "sub"}, profile.ChainCloneName("x", 2): {Name: profile.ChainCloneName("x", 2)},
			"DIRECT": {Name: "DIRECT"}, "REJECT": {Name: "REJECT"},
		},
	}
	chains := []model.ProxyChain{{ID: "x", Name: "中转", Hops: []string{"A", "B"}, Enabled: true}}
	entries, nodes := chainCandidates(px, chains)
	if !slices.Equal(nodes, []string{"A", "B"}) {
		t.Errorf("nodes = %v (provider nodes, clones and builtins left out)", nodes)
	}
	// 节点选择 gets the chain group, 外层 reaches it, 绕回 has a chain
	if !slices.Equal(entries, []string{"A", "B", "香港"}) {
		t.Errorf("entries = %v", entries)
	}
	groups := groupNames(px)
	hints := chainHints([]string{"香港", "B"}, px, groups)
	if len(hints) != 1 || !strings.Contains(hints[0], "取决于") {
		t.Errorf("UDP through a group: %v", hints)
	}
	if hints := chainHints([]string{"A", "B"}, px, groups); len(hints) != 0 {
		t.Errorf("UDP-capable entry: %v", hints)
	}
	px.Proxies["A"] = model.Proxy{Name: "A"}
	if hints := chainHints([]string{"A", "B"}, px, groups); len(hints) != 1 || !strings.Contains(hints[0], "不支持 UDP") {
		t.Errorf("entry without UDP: %v", hints)
	}
}

func TestChainGroupAndDialog(t *testing.T) {
	api := fake.NewInstant()
	if _, err := api.SaveManualNode(model.ManualNode{Name: "东京 VPS", Type: model.ProxyTypeSOCKS5, Server: "1.2.3.4", Port: 1080}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SetProxyChains([]model.ProxyChain{{Name: "中转", Hops: []string{"香港 01", "东京 03"}, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	_ = api.SaveUIState(model.UIState{OpenGroups: map[string]bool{"中转": true, "节点选择": true, profile.ManualGroupName: true}})
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 700)
	if !tt.HasText("中转") || !tt.HasText(profile.ChainGroupName) || !tt.HasText("香港 01 → 东京 03") {
		t.Fatalf("chain group: %q", tt.Texts())
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, profile.ChainClonePrefix) {
			t.Fatalf("clone name shown: %q", s)
		}
	}

	// a provider's node enters through its group
	if err := tt.RightClick("美国 05"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("以所在代理组为入口新建链路…"); err != nil {
		t.Fatalf("%v: %q", err, tt.Menu())
	}
	if p.chains.draft.hops[0] != "节点选择" {
		t.Fatalf("entry: %+v", p.chains.draft)
	}
	p.chains.open = false
	tt.Frame()

	// a node of the configuration can be the exit
	tt.SetSize(900, 1600)
	if err := tt.RightClick("重新测速 东京 VPS"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("以此为落地新建链路…"); err != nil {
		t.Fatalf("%v: %q", err, tt.Menu())
	}
	if !p.chains.open || !p.chains.editing || p.chains.draft.hops[1] != "东京 VPS" {
		t.Fatalf("editor: %+v", p.chains)
	}
	p.chains.draft.name = "第二条"
	p.chains.draft.hops[0] = "香港 01"
	tt.Frame()
	if !tt.HasText("你 → 香港 01 → 东京 VPS → 目标网站") {
		t.Fatalf("path: %q", tt.Texts())
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	cs := api.ProxyChains()
	if len(cs) != 2 || cs[1].Name != "第二条" || !slices.Equal(cs[1].Hops, []string{"香港 01", "东京 VPS"}) || p.chains.editing {
		t.Fatalf("saved: %+v %v", cs, *toasts)
	}

	// an incomplete draft is refused in the dialog
	p.newChain("", "")
	p.chains.draft.name = "x"
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.chains.err, "第 1 跳") || len(api.ProxyChains()) != 2 {
		t.Fatalf("incomplete draft: %q", p.chains.err)
	}
}
