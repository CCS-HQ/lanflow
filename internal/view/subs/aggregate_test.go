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
	p := NewAggregates(e)
	tt := ui.NewTester(p.View, 1200, 1600)
	tt.Frame()
	if !tt.HasText("还没有聚合订阅") {
		t.Fatalf("empty list: %q", tt.Texts())
	}
	if err := tt.Click("新建聚合订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.agg.open || !tt.HasText("新建聚合订阅") || !tt.HasText("订阅链接 #1") || !tt.HasText("标准版") {
		t.Fatalf("editor not open: %q", tt.Texts())
	}

	// Nothing filled in: saving says what is missing.
	if err := tt.Click("导入澜流"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.agg.err == "" || !p.agg.open {
		t.Fatalf("saved an empty source: err %q", p.agg.err)
	}

	// Quick mode: a card of each kind to start with; the empty ones are
	// left out. The kind icons of a new card, a template.
	if len(p.agg.sources) != 3 || aggKinds[p.agg.sources[1].kind] != model.AggSourceYAML || aggKinds[p.agg.sources[2].kind] != model.AggSourceLinks {
		t.Fatalf("starting cards = %+v", p.agg.sources)
	}
	p.agg.sources[0].url = "https://air.example.com/sub"
	p.agg.sources[0].prefix = "A · "
	p.agg.sources[2].links = "trojan://pw@tw.example.com:443?sni=tw.example.com#台湾%2009"
	if err := tt.Click("添加订阅/节点源"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// the new card's 节点链接 kind button
	var kinds []ui.AccessInfo
	for _, a := range tt.AccessAll() {
		if a.Role == ui.RoleButton && a.Name == "节点链接" {
			kinds = append(kinds, a)
		}
	}
	if len(kinds) != 4 {
		t.Fatalf("kind buttons: %d", len(kinds))
	}
	tt.ClickAt(kinds[3].Bounds.X+kinds[3].Bounds.W/2, kinds[3].Bounds.Y+kinds[3].Bounds.H/2)
	tt.Frame()
	if aggKinds[p.agg.sources[3].kind] != model.AggSourceLinks {
		t.Fatalf("kind = %d", p.agg.sources[3].kind)
	}
	if spec := p.agg.fullSpec(nil); len(spec.Sources) != 2 {
		t.Fatalf("empty cards in the spec: %+v", spec.Sources)
	}
	if err := tt.Click("精简版"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.agg.spec.Template != "lite" || len(p.agg.spec.Groups) != 7 {
		t.Fatalf("template: %q %d groups", p.agg.spec.Template, len(p.agg.spec.Groups))
	}

	if err := tt.Click("生成配置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	pv := p.agg.preview
	if pv == nil || p.agg.previewErr != "" {
		t.Fatalf("preview: %+v err %q", pv, p.agg.previewErr)
	}
	// six fake nodes minus the 剩余流量 entry, plus the typed link
	if len(pv.Nodes) != 6 || pv.Nodes[0].Name != "A · 香港 01" || pv.Nodes[5].Name != "台湾 09" {
		t.Fatalf("nodes = %+v", pv.Nodes)
	}
	if !tt.HasText("A · 香港 01") || !tt.HasText("🚀 节点选择") || !tt.HasText("✓ 5 节点") {
		t.Fatalf("preview: %q", tt.Texts())
	}
	// Nothing changed since: no stale note.
	tt.Frame()
	if p.agg.previewStale(nil) || tt.HasText("设置改过了，预览可能已过时。") {
		t.Fatalf("stale right after the preview: %+v / %+v", p.agg.previewOf.Sources, p.agg.fullSpec(nil).Sources)
	}
	if err := tt.Click("YAML"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// Advanced mode: a module, a custom rule, node processing.
	if err := tt.Click("高级模式"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	d := &p.agg
	d.setModule("youtube", true)
	if d.groupIndex("youtube") < 0 || d.spec.Template != "" {
		t.Fatal("module not added")
	}
	if msg := d.addRule(model.AggregateRule{Type: model.AggRuleDomainSuffix, Value: "corp.example.com", Target: "youtube"}); msg != "" {
		t.Fatal(msg)
	}
	if msg := d.addRule(model.AggregateRule{Type: model.AggRuleDomainSuffix, Value: "corp.example.com", Target: "youtube"}); msg == "" {
		t.Fatal("a duplicate rule was added")
	}
	views := d.ruleViews()
	if views[0].Key != "DOMAIN-SUFFIX,corp.example.com" || views[len(views)-1].Key != "MATCH" {
		t.Fatalf("rule views = %+v", views)
	}
	d.moveRule(views, 0, 1)
	if d.ruleViews()[1].Key != "DOMAIN-SUFFIX,corp.example.com" {
		t.Fatal("rule not moved")
	}
	d.setModule("youtube", false)
	if d.spec.Rules[0].Target != profile.AggModSelect {
		t.Fatalf("rule target of a removed group = %q", d.spec.Rules[0].Target)
	}
	d.spec.Exclude = "台湾"
	tt.Frame()
	if !tt.HasText("设置改过了，预览可能已过时。") {
		t.Fatalf("no stale note: %q", tt.Texts())
	}

	d.name = "全部节点"
	if err := tt.Click("导入澜流"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.open {
		t.Fatalf("editor still open: %q", d.err)
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
	if got := (*toasts)[len(*toasts)-1]; got != "已导入澜流：「全部节点」" {
		t.Fatalf("toast %q", got)
	}

	// The list shows it, with what its spec holds.
	tt.Frame()
	tt.Frame()
	if !tt.HasText("全部节点") || !tt.HasText("2 个来源 · 高级配置 · 更新于 刚刚") {
		t.Fatalf("list: %q", tt.Texts())
	}

	// Editing loads the stored spec, on the advanced mode (it has rules).
	if err := tt.Click("编辑 全部节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.open || d.id != sub.ID || len(d.sources) != 2 || d.sources[1].links == "" || d.spec.Exclude != "台湾" || len(d.spec.Rules) != 1 || d.mode != aggModeAdvanced {
		t.Fatalf("editor state = %+v", d)
	}
	// A source reading another subscription: the choice defaults to the first one.
	d.addSource(model.AggSourceSub)
	tt.Frame()
	spec := d.fullSpec(api.Subscriptions())
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

func TestAggregateTemplates(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 1600)
	tt.Frame()
	p.openAggregate(nil)
	tt.Frame()
	d := &p.agg
	d.spec.Rules = []model.AggregateRule{{Type: model.AggRuleDomain, Value: "a.example.com", Target: "DIRECT"}}
	if err := tt.Click("保存为模板"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	d.dlg.tplName = "我的分流"
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	list, _ := api.AggregateTemplates()
	if len(list) != 1 || list[0].Name != "我的分流" || len(list[0].Spec.Sources) != 0 || len(list[0].Spec.Rules) != 1 {
		t.Fatalf("templates = %+v", list)
	}
	if d.spec.Template != "my:"+list[0].ID {
		t.Fatalf("template mark = %q", d.spec.Template)
	}
	d.applyTemplate("full")
	d.spec.Rules = nil
	if err := tt.Click("我的模板"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("套用"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(d.spec.Rules) != 1 || d.spec.Template != "my:"+list[0].ID || len(d.sources) != 3 {
		t.Fatalf("applied: %+v", d.spec)
	}
}

func TestAggregateGroupDialog(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	p.openAggregate(nil)
	d := &p.agg
	d.dlg.openGroup(d.spec, -1)
	d.dlg.gName, d.dlg.gType, d.dlg.gRules = "💼 工作", model.AggTypeSelect, []string{"geosite:slack"}
	if msg := p.saveGroup(); msg != "" {
		t.Fatal(msg)
	}
	g := d.spec.Groups[len(d.spec.Groups)-1]
	if !g.Custom || g.Name != "💼 工作" || g.Rules[0] != "geosite:slack" {
		t.Fatalf("custom group = %+v", g)
	}
	// renaming a catalog group to its own name keeps no override
	i := d.groupIndex("ads")
	d.dlg.openGroup(d.spec, i)
	d.dlg.gType = model.AggTypeDirectFirst
	if msg := p.saveGroup(); msg != "" {
		t.Fatal(msg)
	}
	if g := d.spec.Groups[i]; g.Name != "" || g.Type != model.AggTypeDirectFirst {
		t.Fatalf("ads = %+v", g)
	}
	// a duplicate name is refused
	d.dlg.openGroup(d.spec, -1)
	d.dlg.gName = "💼 工作"
	if msg := p.saveGroup(); msg == "" {
		t.Fatal("a duplicate group name was accepted")
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
