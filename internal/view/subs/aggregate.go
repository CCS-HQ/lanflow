package subs

import (
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// The aggregate subscription editor (docs/aggregate.md). It replaces the
// subscription table while open: on the left the 配置生成器 (a quick mode
// with source cards and templates, an advanced mode with every section),
// on the right the 预览 of the generated profile (a visualisation or the
// YAML). aggregate_left.go, aggregate_right.go and aggregate_dialogs.go
// draw the parts.

// Source kinds, in the order of a card's kind icons.
var aggKinds = []string{model.AggSourceURL, model.AggSourceYAML, model.AggSourceLinks, model.AggSourceSub}

// aggTwoColumns is the window width from which the generator and the
// preview stand side by side; a narrower window switches between them.
const aggTwoColumns = 1000

// Editor modes and preview views.
const (
	aggModeQuick = iota
	aggModeAdvanced
)

const (
	aggViewYAML = iota
	aggViewGraph
)

// aggSrcForm is one source being edited.
type aggSrcForm struct {
	uid     int // stable widget key
	id      string
	kind    int // index into aggKinds
	name    string
	url     string
	yaml    string
	links   string
	sub     string // the chosen subscription's label (subChoices)
	prefix  string
	enabled bool
}

// aggEditor is the editor's state; open replaces the table with it.
type aggEditor struct {
	open    bool
	loading bool // the stored spec is being read
	session int
	id      string // "" = new

	// The subscription record.
	name     string
	auto     int
	ua       string
	viaProxy bool
	timeout  string
	use      bool // new only: switch to it after saving

	// spec holds everything but the sources, which the cards edit.
	spec    model.AggregateSpec
	sources []aggSrcForm
	nextUID int

	mode     int // aggModeQuick / aggModeAdvanced
	narrow   int // a narrow window: 0 the generator, 1 the preview
	sections map[string]bool
	cats     map[string]bool
	nodeQ    string
	listenOn bool
	listen   string

	// The manual rule form (自定义分流规则 › 方法二).
	ruleType   string
	ruleValue  string
	ruleTarget string
	ruleNoRes  bool

	dlg aggDialogs

	templates []model.AggregateTemplate // 我的模板
	tplErr    string

	saving     bool
	err        string
	previewing bool
	preview    *model.AggregatePreview
	previewOf  model.AggregateSpec // the spec the preview was built from
	previewErr string
	view       int // aggViewYAML / aggViewGraph
	groupOpen  map[string]bool
}

// openAggregate opens the editor: a new aggregate subscription when sub
// is nil, else the stored one (its spec is read in the background).
func (p *Page) openAggregate(sub *model.Subscription) {
	d := &p.agg
	*d = aggEditor{open: true, session: d.session + 1, use: !p.e.API.Status().HasSubscriptions, view: aggViewGraph,
		sections: map[string]bool{"sources": true, "nodes": true, "groups": true}, cats: map[string]bool{profile.AggCatCore: true},
		groupOpen: map[string]bool{}, ruleType: model.AggRuleDomainSuffix}
	p.loadTemplates()
	if sub == nil {
		d.name = i18n.T("聚合订阅")
		d.auto = autoIndex(12)
		d.fillSpec(profile.DefaultAggregateSpec(), nil)
		// One card of each kind to fill in; the empty ones are left out.
		d.addSource(model.AggSourceURL)
		d.addSource(model.AggSourceYAML)
		d.addSource(model.AggSourceLinks)
		return
	}
	s := *sub
	d.id, d.name, d.auto, d.ua, d.viaProxy, d.timeout = s.ID, s.Name, autoIndex(s.AutoUpdateHours), s.UserAgent, s.ViaProxy, timeoutText(s.TimeoutSec)
	d.loading = true
	var spec model.AggregateSpec
	session := d.session
	p.e.Then(func() (err error) {
		spec, err = p.e.API.AggregateSpec(s.ID)
		return err
	}, func(err error) {
		if p.agg.session != session {
			return
		}
		p.agg.loading = false
		if err != nil {
			p.agg.open = false
			p.e.Toast(err.Error(), true)
			return
		}
		p.agg.fillSpec(spec, p.e.API.Subscriptions())
		// A stored subscription opens on the advanced mode when it uses
		// more than a template gives.
		if p.agg.spec.Template == "" || len(p.agg.spec.Rules) > 0 || len(p.agg.spec.Relays) > 0 {
			p.agg.mode = aggModeAdvanced
		}
	})
}

// loadTemplates reads the user's templates in the background.
func (p *Page) loadTemplates() {
	session := p.agg.session
	var list []model.AggregateTemplate
	p.e.Then(func() (err error) {
		list, err = p.e.API.AggregateTemplates()
		return err
	}, func(err error) {
		if p.agg.session != session {
			return
		}
		p.agg.templates, p.agg.tplErr = list, ""
		if err != nil {
			p.agg.tplErr = err.Error()
		}
	})
}

// fillSpec loads a stored spec into the form.
func (d *aggEditor) fillSpec(spec model.AggregateSpec, subs []model.Subscription) {
	spec = profile.UpgradeAggregateSpec(spec).Clone()
	labels := subChoices(subs, d.id)
	d.sources = nil
	for _, src := range spec.Sources {
		f := aggSrcForm{uid: d.nextUID, id: src.ID, name: src.Name, url: src.URL, yaml: src.YAML, links: src.Links, prefix: src.Prefix, enabled: !src.Disabled}
		d.nextUID++
		for i, k := range aggKinds {
			if k == src.Kind {
				f.kind = i
			}
		}
		for _, l := range labels {
			if l.id == src.SubID {
				f.sub = l.label
			}
		}
		d.sources = append(d.sources, f)
	}
	spec.Sources = nil
	d.spec = spec
	d.listenOn = spec.ListenBase > 0
	d.listen = ""
	if spec.ListenBase > 0 {
		d.listen = strconv.Itoa(spec.ListenBase)
	}
}

func (d *aggEditor) addSource(kind string) {
	f := aggSrcForm{uid: d.nextUID, enabled: true}
	d.nextUID++
	for i, k := range aggKinds {
		if k == kind {
			f.kind = i
		}
	}
	d.sources = append(d.sources, f)
}

// subChoice is a subscription a source can read from.
type subChoice struct{ id, label string }

// subChoices are the subscriptions an aggregate can use as a source: not
// itself and not another aggregate. Names that repeat get their ID.
func subChoices(subs []model.Subscription, self string) []subChoice {
	count := map[string]int{}
	for _, s := range subs {
		count[s.Name]++
	}
	var out []subChoice
	for _, s := range subs {
		if s.ID == self || s.Source == model.SourceAggregate {
			continue
		}
		label := s.Name
		if count[s.Name] > 1 {
			label = fmt.Sprintf("%s (%s)", s.Name, s.ID)
		}
		out = append(out, subChoice{s.ID, label})
	}
	return out
}

// fullSpec builds the spec the form describes. Source cards left empty
// are not part of it.
func (d *aggEditor) fullSpec(subs []model.Subscription) model.AggregateSpec {
	labels := subChoices(subs, d.id)
	spec := d.spec.Clone()
	spec.Version = model.AggregateSpecVersion
	spec.Include, spec.Exclude = strings.TrimSpace(spec.Include), strings.TrimSpace(spec.Exclude)
	spec.Sources = nil
	for _, f := range d.sources {
		src := model.AggregateSource{ID: f.id, Kind: aggKinds[f.kind], Name: strings.TrimSpace(f.name), Prefix: f.prefix, Disabled: !f.enabled}
		switch src.Kind {
		case model.AggSourceURL:
			src.URL = strings.TrimSpace(f.url)
		case model.AggSourceYAML:
			src.YAML = strings.TrimSpace(f.yaml)
		case model.AggSourceLinks:
			src.Links = strings.TrimSpace(f.links)
		case model.AggSourceSub:
			for _, l := range labels {
				if l.label == f.sub {
					src.SubID = l.id
				}
			}
		}
		if src.URL == "" && src.YAML == "" && src.Links == "" && src.SubID == "" {
			continue
		}
		spec.Sources = append(spec.Sources, src)
	}
	var renames []model.RenameRule
	for _, r := range spec.Renames {
		if strings.TrimSpace(r.Pattern) != "" || r.Replace != "" {
			renames = append(renames, r)
		}
	}
	spec.Renames = renames
	spec.ListenBase = 0
	if d.listenOn {
		spec.ListenBase, _ = strconv.Atoi(strings.TrimSpace(d.listen))
	}
	return spec
}

// nameSources gives the cards without one an ID, so a preview's reports
// find their card and the spec stays the same after a preview.
func (d *aggEditor) nameSources() {
	used := map[string]bool{}
	for _, f := range d.sources {
		used[f.id] = true
	}
	for i := range d.sources {
		for d.sources[i].id == "" {
			if id := fmt.Sprintf("n%d", d.nextUID); !used[id] {
				d.sources[i].id, used[id] = id, true
			}
			d.nextUID++
		}
	}
}

// record is the subscription record the form describes (download
// options, name, auto update).
func (d *aggEditor) record() model.Subscription {
	return model.Subscription{
		ID: d.id, Name: strings.TrimSpace(d.name), Source: model.SourceAggregate,
		AutoUpdateHours: autoHours[min(d.auto, len(autoHours)-1)], UserAgent: strings.TrimSpace(d.ua),
		ViaProxy: d.viaProxy, TimeoutSec: timeoutSec(d.timeout),
	}
}

// check reports what keeps the form from being previewed or saved.
func (d *aggEditor) check(spec model.AggregateSpec) string {
	if strings.TrimSpace(d.name) == "" {
		return i18n.T("请填写名称。")
	}
	if !validTimeout(d.timeout) {
		return i18n.Tf("下载超时须为 1–%d 之间的秒数，或留空。", maxTimeoutSec)
	}
	if d.listenOn && spec.ListenBase == 0 {
		return i18n.Tf("监听端口须为 1024–%d 之间的数字", profile.AggMaxListen)
	}
	if len(spec.Sources) == 0 {
		return i18n.T("请至少填写一个来源：订阅链接、YAML 配置、节点链接或已有订阅。")
	}
	if err := profile.ValidateAggregateSpec(spec, i18n.Tf); err != nil {
		return err.Error()
	}
	return ""
}

// ---- groups ----

// groupIndex is the position of group id in the spec, -1 if absent.
func (d *aggEditor) groupIndex(id string) int {
	for i, g := range d.spec.Groups {
		if g.ID == id {
			return i
		}
	}
	return -1
}

// setModule turns a catalog module on (at its catalog position among the
// groups present) or off.
func (d *aggEditor) setModule(id string, on bool) {
	i := d.groupIndex(id)
	if !on {
		if i >= 0 {
			d.spec.Groups = append(d.spec.Groups[:i:i], d.spec.Groups[i+1:]...)
			d.dropTargets(id)
		}
		d.spec.Template = ""
		return
	}
	if i >= 0 {
		return
	}
	rank := map[string]int{}
	for k, m := range profile.AggregateModules {
		rank[m.ID] = k
	}
	at := len(d.spec.Groups)
	for k, g := range d.spec.Groups {
		if r, ok := rank[g.ID]; ok && !g.Custom && r > rank[id] {
			at = k
			break
		}
	}
	gs := append([]model.AggregateGroup(nil), d.spec.Groups[:at]...)
	gs = append(gs, model.AggregateGroup{ID: id})
	d.spec.Groups = append(gs, d.spec.Groups[at:]...)
	d.spec.Template = ""
}

// dropTargets moves the custom rules that targeted a removed group to
// 节点选择.
func (d *aggEditor) dropTargets(id string) {
	for i := range d.spec.Rules {
		if d.spec.Rules[i].Target == id {
			d.spec.Rules[i].Target = profile.AggModSelect
		}
	}
}

// applyTemplate replaces the groups with a built-in template's.
func (d *aggEditor) applyTemplate(id string) {
	tp := profile.AggTemplate(id)
	if tp == nil {
		return
	}
	var custom []model.AggregateGroup
	for _, g := range d.spec.Groups {
		if g.Custom {
			custom = append(custom, g)
		}
	}
	groups := tp.Groups()
	// Keep the renames and type changes of groups that stay.
	for i := range groups {
		if k := d.groupIndex(groups[i].ID); k >= 0 {
			ext := groups[i].Extended
			groups[i] = d.spec.Groups[k]
			groups[i].Extended = ext
		}
	}
	d.spec.Groups = append(groups, custom...)
	keep := map[string]bool{"DIRECT": true, "REJECT": true}
	for _, g := range d.spec.Groups {
		keep[g.ID] = true
	}
	for i := range d.spec.Rules {
		if !keep[d.spec.Rules[i].Target] {
			d.spec.Rules[i].Target = profile.AggModSelect
		}
	}
	d.spec.Template = id
}

// applyMyTemplate replaces everything but the sources with a template of
// the user's own.
func (d *aggEditor) applyMyTemplate(t model.AggregateTemplate) {
	spec := profile.UpgradeAggregateSpec(t.Spec).Clone()
	spec.Sources = nil
	spec.Hidden, spec.NodeOrder = d.spec.Hidden, d.spec.NodeOrder
	spec.Template = "my:" + t.ID
	d.spec = spec
	d.listenOn = spec.ListenBase > 0
	if spec.ListenBase > 0 {
		d.listen = strconv.Itoa(spec.ListenBase)
	}
}

// groupLabel is how the editor names group g.
func groupLabel(g model.AggregateGroup) string {
	if g.Name != "" {
		return g.Name
	}
	if m := profile.AggModule(g.ID); m != nil {
		return m.Emoji + " " + i18n.T(m.Label)
	}
	return g.ID
}

// targetChoices are the targets a custom rule can have: the spec's groups,
// DIRECT and REJECT, as labels and IDs.
func (d *aggEditor) targetChoices() (labels, ids []string) {
	for _, g := range d.spec.Groups {
		labels, ids = append(labels, groupLabel(g)), append(ids, g.ID)
	}
	return append(labels, "DIRECT", "REJECT"), append(ids, "DIRECT", "REJECT")
}

// typeLabel names a group type.
func typeLabel(typ string) string {
	switch typ {
	case model.AggTypeURLTest:
		return i18n.T("自动测速")
	case model.AggTypeFallback:
		return i18n.T("故障转移")
	case model.AggTypeLoadBalance:
		return i18n.T("负载均衡")
	case model.AggTypeRejectFirst:
		return i18n.T("拦截")
	case model.AggTypeDirectFirst:
		return i18n.T("直连")
	}
	return i18n.T("手动选择")
}

// ---- nodes ----

// moveNode moves the node name one place up (dir -1) or down (+1) in the
// preview's order and records the whole order.
func (d *aggEditor) moveNode(name string, dir int) {
	if d.preview == nil {
		return
	}
	var names []string
	for _, n := range d.preview.Nodes {
		names = append(names, n.Name)
	}
	i := indexOf(names, name)
	j := i + dir
	if i < 0 || j < 0 || j >= len(names) {
		return
	}
	names[i], names[j] = names[j], names[i]
	d.spec.NodeOrder = names
	nodes := d.preview.Nodes
	nodes[i], nodes[j] = nodes[j], nodes[i]
}

// toggleHidden hides or shows node name.
func (d *aggEditor) toggleHidden(name string) {
	if i := indexOf(d.spec.Hidden, name); i >= 0 {
		d.spec.Hidden = append(d.spec.Hidden[:i:i], d.spec.Hidden[i+1:]...)
	} else {
		d.spec.Hidden = append(d.spec.Hidden, name)
	}
	if d.preview != nil {
		for i := range d.preview.Nodes {
			if d.preview.Nodes[i].Name == name {
				d.preview.Nodes[i].Hidden = !d.preview.Nodes[i].Hidden
			}
		}
	}
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// ---- rules ----

// ruleViews are the rules in their final order, for 规则管理.
func (d *aggEditor) ruleViews() []model.AggregateRuleView {
	return profile.AggregateRuleViews(d.spec, i18n.Tf)
}

// moveRule moves the rule at i one place (dir -1 up, +1 down); MATCH stays last.
func (d *aggEditor) moveRule(views []model.AggregateRuleView, i, dir int) {
	j := i + dir
	last := len(views) - 1 // MATCH
	if i < 0 || i >= last || j < 0 || j >= last {
		return
	}
	keys := make([]string, 0, last)
	for _, v := range views[:last] {
		keys = append(keys, v.Key)
	}
	keys[i], keys[j] = keys[j], keys[i]
	d.spec.RuleOrder = keys
}

// addRule adds a custom rule; it reports what is wrong instead when the
// rule cannot be added.
func (d *aggEditor) addRule(r model.AggregateRule) string {
	r.Value = strings.TrimSpace(r.Value)
	if r.Value == "" {
		return i18n.T("没有填写规则值")
	}
	if len(d.spec.Rules) >= profile.AggMaxRules {
		return i18n.Tf("自定义规则不能超过 %d 条", profile.AggMaxRules)
	}
	key := profile.AggregateRuleKey(r.Type, r.Value)
	for _, x := range d.spec.Rules {
		if profile.AggregateRuleKey(x.Type, x.Value) == key {
			return i18n.T("这条规则已经添加过了")
		}
	}
	probe := d.spec.Clone()
	probe.Version = model.AggregateSpecVersion
	probe.Sources = []model.AggregateSource{{ID: "x", Kind: model.AggSourceLinks, Links: "ss://x"}}
	probe.Rules = append(probe.Rules, r)
	if err := profile.ValidateAggregateSpec(probe, i18n.Tf); err != nil {
		return err.Error()
	}
	d.spec.Rules = append(d.spec.Rules, r)
	return ""
}

// ---- preview and save ----

// previewStale reports whether the form changed since the preview.
func (d *aggEditor) previewStale(subs []model.Subscription) bool {
	return d.preview != nil && !reflect.DeepEqual(d.previewOf, d.fullSpec(subs))
}

// sourceReport is the preview's report on the source with id, nil if none.
func (d *aggEditor) sourceReport(id string) *model.AggregateSourceReport {
	if d.preview == nil || id == "" {
		return nil
	}
	for i := range d.preview.Sources {
		if d.preview.Sources[i].ID == id {
			return &d.preview.Sources[i]
		}
	}
	return nil
}

// aggSourceDesc names a source: a link's host, the subscription it reads,
// or its kind.
func aggSourceDesc(src model.AggregateSource, subs []model.Subscription) string {
	switch src.Kind {
	case model.AggSourceURL:
		if u, err := url.Parse(src.URL); err == nil && u.Host != "" {
			return u.Hostname()
		}
		return i18n.T("订阅链接")
	case model.AggSourceSub:
		for _, s := range subs {
			if s.ID == src.SubID {
				return s.Name
			}
		}
		return i18n.T("已有订阅")
	case model.AggSourceYAML:
		return i18n.T("YAML 配置")
	}
	return i18n.T("节点链接")
}

// previewAggregate builds the profile in the background.
func (p *Page) previewAggregate(subs []model.Subscription) {
	d := &p.agg
	d.nameSources()
	spec := d.fullSpec(subs)
	if msg := d.check(spec); msg != "" {
		d.err = msg
		return
	}
	d.err, d.previewing = "", true
	d.narrow = 1
	rec, session := d.record(), d.session
	var pv model.AggregatePreview
	p.e.Then(func() (err error) {
		pv, err = p.e.API.PreviewAggregate(rec, spec)
		return err
	}, func(err error) {
		if p.agg.session != session {
			return
		}
		p.agg.previewing = false
		p.agg.previewOf = spec
		p.agg.preview = &pv
		p.agg.previewErr = ""
		if err != nil {
			p.agg.previewErr = err.Error()
			if len(pv.Sources) == 0 {
				p.agg.preview = nil
			}
		}
	})
}

// saveAggregate builds, checks and stores the subscription; the editor
// closes when it worked.
func (p *Page) saveAggregate(subs []model.Subscription) {
	d := &p.agg
	spec := d.fullSpec(subs)
	if msg := d.check(spec); msg != "" {
		d.err = msg
		return
	}
	d.err, d.saving = "", true
	rec, use, session := d.record(), d.use && d.id == "", d.session
	var saved model.Subscription
	p.e.Then(func() (err error) {
		saved, err = p.e.API.SaveAggregate(rec, spec, use)
		return err
	}, func(err error) {
		if p.agg.session != session {
			return
		}
		p.agg.saving = false
		if err != nil {
			p.agg.err = err.Error()
			return
		}
		p.agg.open = false
		p.e.Toast(i18n.Tf("已导入澜流：「%s」", saved.Name), false)
	})
}

// aggregateView draws the editor in place of the table.
func (p *Page) aggregateView(c *ui.Context) {
	k := p.e.Kit
	d := &p.agg
	subs := p.e.API.Subscriptions()
	title := i18n.T("新建聚合订阅")
	if d.id != "" {
		title = i18n.T("编辑聚合订阅")
	}
	w, _ := c.Size()
	two := w <= 0 || w >= aggTwoColumns
	k.Header(c, title, d.name, func() {
		if !two {
			ui.Segmented(c.Key("agg-narrow"), &d.narrow, i18n.T("配置"), i18n.T("预览")).Label(i18n.T("显示"))
		}
		if ui.Button(c, i18n.T("取消")).Disabled(d.saving).Clicked() {
			d.open = false
		}
	})
	if d.loading {
		ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Spinner(c).Size(16, 16).Label(i18n.T("正在读取…"))
		})
		return
	}
	ui.Row(c.Key(d.session)).Grow(1).Shrink(1).Basis(0).Padding(0, 24, 16, 24).Gap(16).AlignItems(ui.Stretch).Children(func() {
		if two || d.narrow == 0 {
			p.aggLeft(c, subs)
		}
		if two || d.narrow == 1 {
			p.aggRight(c, subs)
		}
	})
	p.aggDialogs(c, subs)
}

// aggPanel draws one of the two rounded columns: a header row, a
// scrolling body and a footer of buttons.
func aggPanel(c *ui.Context, key string, grow float32, header, body, footer func()) {
	t := c.Theme()
	ui.Column(c.Key(key)).Grow(grow).Basis(0).Shrink(1).MinWidth(0).Radius(14).Background(t.Surface).
		Border(1, t.Border.Alpha(0.65)).Clip().Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(12, 16).MinHeight(52).Shrink(0).Children(header)
		ui.Scroll(c.Key(key + "-scroll")).Grow(1).Shrink(1).Basis(0).Children(func() {
			ui.Column(c).Gap(10).Padding(0, 14, 12, 14).Children(body)
		})
		if footer != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Justify(ui.Center).Padding(10, 14).Shrink(0).
				BorderWidth(1, 0, 0, 0).BorderColor(t.Border.Alpha(0.5)).Children(footer)
		}
	})
}
