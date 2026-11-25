package subs

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

func TestAggregateEditorCreatePreviewSave(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 760)
	tt.Frame()
	if err := tt.Click("导入"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("新建聚合订阅…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.agg.open || !tt.HasText("新建聚合订阅") || !tt.HasText("来源 1") {
		t.Fatalf("editor not open: %q", tt.Texts())
	}

	// Nothing filled in: saving says what is missing.
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.agg.err == "" || !p.agg.open {
		t.Fatalf("saved an empty source: err %q", p.agg.err)
	}

	p.agg.sources[0].url = "https://air.example.com/sub"
	p.agg.sources[0].prefix = "A · "
	p.agg.addSource(model.AggSourceLinks)
	p.agg.sources[1].links = "trojan://pw@tw.example.com:443?sni=tw.example.com#台湾%2009"
	p.agg.renames = []model.RenameRule{{Pattern: `\s+0(\d)$`, Replace: " $1"}}
	for _, tab := range []int{aggTabSources, aggTabNodes, aggTabGroups} {
		p.agg.tab = tab
		tt.Frame()
	}
	if !tt.HasText("香港节点") || !tt.HasText("广告拦截") {
		t.Fatalf("groups tab: %q", tt.Texts())
	}

	if err := tt.Click("预览"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	pv := p.agg.preview
	if pv == nil || p.agg.previewErr != "" {
		t.Fatalf("preview: %+v err %q", pv, p.agg.previewErr)
	}
	// six fake nodes minus the 剩余流量 entry, plus the typed link
	if len(pv.Nodes) != 6 || pv.Nodes[0].Name != "A · 香港 1" || pv.Nodes[5].Name != "台湾 9" {
		t.Fatalf("nodes = %+v", pv.Nodes)
	}
	if !tt.HasText("A · 香港 1") || !tt.HasText(profile.AggGroupSelect) {
		t.Fatalf("preview tab: %q", tt.Texts())
	}
	if err := tt.Click("查看生成的 YAML"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// A change marks the preview stale.
	p.agg.exclude = "台湾"
	tt.Frame()
	if !tt.HasText("设置改过了，预览可能已过时。") {
		t.Fatalf("no stale note: %q", tt.Texts())
	}

	p.agg.name = "全部节点"
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.agg.open {
		t.Fatalf("editor still open: %q", p.agg.err)
	}
	var sub model.Subscription
	for _, s := range api.Subscriptions() {
		if s.Source == model.SourceAggregate {
			sub = s
		}
	}
	if sub.Name != "全部节点" || sub.Total == 0 {
		t.Fatalf("saved = %+v", sub)
	}
	if got := (*toasts)[len(*toasts)-1]; got != "已保存「全部节点」" {
		t.Fatalf("toast %q", got)
	}
	if !tt.HasText("聚合") {
		t.Fatalf("source column: %q", tt.Texts())
	}

	// Editing loads the stored spec.
	if err := tt.Click("更多 全部节点"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("编辑聚合…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	d := &p.agg
	if !d.open || d.id != sub.ID || len(d.sources) != 2 || d.sources[1].links == "" || d.exclude != "台湾" || len(d.renames) != 1 {
		t.Fatalf("editor state = %+v", d)
	}
	if !tt.HasText("编辑聚合订阅") {
		t.Fatalf("edit title: %q", tt.Texts())
	}
	// A source reading another subscription: the choice defaults to the first one.
	d.addSource(model.AggSourceSub)
	tt.Frame()
	spec := d.spec(api.Subscriptions())
	if got := spec.Sources[2].SubID; got != "s1" {
		t.Fatalf("sub source = %q", got)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.open {
		t.Fatal("cancel kept the editor open")
	}
}

func TestSubChoicesSkipSelfAndAggregates(t *testing.T) {
	subs := []model.Subscription{
		{ID: "a", Name: "机场"}, {ID: "b", Name: "机场"}, {ID: "c", Name: "聚合", Source: model.SourceAggregate}, {ID: "d", Name: "自己"},
	}
	got := subChoices(subs, "d")
	if len(got) != 2 || got[0].label != "机场 (a)" || got[1].label != "机场 (b)" {
		t.Fatalf("choices = %+v", got)
	}
}
