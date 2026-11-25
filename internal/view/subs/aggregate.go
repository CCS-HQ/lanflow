package subs

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

// The aggregate subscription editor (docs/aggregate.md): it replaces the
// subscription table while open. Sources, node processing, groups and
// rules, and a preview that builds the profile without saving it.

// Editor tabs.
const (
	aggTabSources = iota
	aggTabNodes
	aggTabGroups
	aggTabPreview
)

// Source kinds, in the order of the kind switch.
var aggKinds = []string{model.AggSourceURL, model.AggSourceLinks, model.AggSourceSub}

// aggPreviewNodes is how many nodes the preview lists.
const aggPreviewNodes = 300

// aggSrcForm is one source being edited.
type aggSrcForm struct {
	uid     int // stable widget key
	id      string
	kind    int // index into aggKinds
	url     string
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
	tab     int

	name     string
	auto     int
	ua       string
	viaProxy bool
	timeout  string
	use      bool // new only: switch to it after saving

	sources []aggSrcForm
	nextUID int
	include string
	exclude string
	renames []model.RenameRule
	autoGrp bool
	regions map[string]bool
	modules map[string]bool

	saving     bool
	err        string
	previewing bool
	preview    *model.AggregatePreview
	previewOf  model.AggregateSpec // the spec the preview was built from
	previewErr string
	showYAML   bool
}

// openAggregate opens the editor: a new aggregate subscription when sub
// is nil, else the stored one (its spec is read in the background).
func (p *Page) openAggregate(sub *model.Subscription) {
	d := &p.agg
	*d = aggEditor{open: true, session: d.session + 1, use: !p.e.API.Status().HasSubscriptions}
	if sub == nil {
		d.name = i18n.T("聚合订阅")
		d.auto = autoIndex(12)
		d.fillSpec(profile.DefaultAggregateSpec(), nil)
		d.addSource(model.AggSourceURL)
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
	})
}

// fillSpec loads a stored spec into the form.
func (d *aggEditor) fillSpec(spec model.AggregateSpec, subs []model.Subscription) {
	d.include, d.exclude, d.autoGrp = spec.Include, spec.Exclude, spec.AutoGroup
	d.renames = append([]model.RenameRule(nil), spec.Renames...)
	d.regions, d.modules = map[string]bool{}, map[string]bool{}
	for _, id := range spec.Regions {
		d.regions[id] = true
	}
	for _, id := range spec.Modules {
		d.modules[id] = true
	}
	labels := subChoices(subs, d.id)
	d.sources = nil
	for _, src := range spec.Sources {
		f := aggSrcForm{uid: d.nextUID, id: src.ID, url: src.URL, links: src.Links, prefix: src.Prefix, enabled: !src.Disabled}
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

// spec builds the spec the form describes.
func (d *aggEditor) spec(subs []model.Subscription) model.AggregateSpec {
	labels := subChoices(subs, d.id)
	spec := model.AggregateSpec{
		Include: strings.TrimSpace(d.include), Exclude: strings.TrimSpace(d.exclude), AutoGroup: d.autoGrp,
	}
	for _, f := range d.sources {
		src := model.AggregateSource{ID: f.id, Kind: aggKinds[f.kind], Prefix: f.prefix, Disabled: !f.enabled}
		switch src.Kind {
		case model.AggSourceURL:
			src.URL = strings.TrimSpace(f.url)
		case model.AggSourceLinks:
			src.Links = strings.TrimSpace(f.links)
		case model.AggSourceSub:
			for _, l := range labels {
				if l.label == f.sub {
					src.SubID = l.id
				}
			}
		}
		spec.Sources = append(spec.Sources, src)
	}
	for _, r := range d.renames {
		if strings.TrimSpace(r.Pattern) != "" || r.Replace != "" {
			spec.Renames = append(spec.Renames, r)
		}
	}
	for _, r := range profile.AggregateRegions {
		if d.regions[r.ID] {
			spec.Regions = append(spec.Regions, r.ID)
		}
	}
	for _, m := range profile.AggregateModules {
		if d.modules[m.ID] {
			spec.Modules = append(spec.Modules, m.ID)
		}
	}
	return spec
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
	for i, src := range spec.Sources {
		if src.Kind == model.AggSourceSub && src.SubID == "" {
			return i18n.Tf("第 %d 个来源没有选择订阅。", i+1)
		}
	}
	if err := profile.ValidateAggregateSpec(spec, i18n.Tf); err != nil {
		return err.Error()
	}
	return ""
}

// aggregateView draws the editor in place of the table.
func (p *Page) aggregateView(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	subs := p.e.API.Subscriptions()
	title := i18n.T("新建聚合订阅")
	if d.id != "" {
		title = i18n.T("编辑聚合订阅")
	}
	k.Header(c, title, d.name, func() {
		if ui.Button(c, i18n.T("取消")).Disabled(d.saving).Clicked() {
			d.open = false
		}
		pv := ui.Button(c, "").Label(i18n.T("预览")).Disabled(d.loading || d.previewing || d.saving)
		pv.Children(func() {
			if d.previewing {
				ui.Spinner(c).Size(12, 12)
			}
			ui.Text(c, i18n.T("预览")).SingleLine()
		})
		if pv.Clicked() {
			d.tab = aggTabPreview
			p.previewAggregate(subs)
		}
		save := ui.PrimaryButton(c, "").Label(i18n.T("保存")).Disabled(d.loading || d.saving)
		save.Children(func() {
			if d.saving {
				ui.Spinner(c).Size(12, 12)
			}
			ui.Text(c, i18n.T("保存")).SingleLine()
		})
		if save.Clicked() {
			p.saveAggregate(subs)
		}
	})
	if d.loading {
		ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			ui.Spinner(c).Size(16, 16).Label(i18n.T("正在读取…"))
		})
		return
	}
	ui.Column(c.Key(d.session)).Grow(1).Padding(0, 24, 16, 24).Gap(12).Children(func() {
		ui.Segmented(c.Key("agg-tab"), &d.tab, i18n.Options("来源", "节点处理", "分组与规则", "预览")...).Label(i18n.T("聚合订阅设置"))
		if d.err != "" {
			k.Banner(c, "error", d.err, nil)
		}
		ui.Scroll(c.Key(fmt.Sprintf("agg-scroll-%d", d.tab))).Grow(1).Children(func() {
			ui.Column(c).Gap(16).Padding(0, 4, 8, 0).Children(func() {
				switch d.tab {
				case aggTabSources:
					p.aggSourcesTab(c, t, subs)
				case aggTabNodes:
					p.aggNodesTab(c, t)
				case aggTabGroups:
					p.aggGroupsTab(c, t)
				default:
					p.aggPreviewTab(c, t, subs)
				}
			})
		})
	})
}

func (p *Page) aggSourcesTab(c *ui.Context, t *ui.Theme, subs []model.Subscription) {
	k := p.e.Kit
	d := &p.agg
	k.Section(c, i18n.T("基本"), "", func() {
		k.Row(c, i18n.T("名称"), "", func() {
			ui.TextInput(c.Key("agg-name"), &d.name).Label(i18n.T("名称")).Width(260)
		})
		k.Row(c, i18n.T("自动更新"), i18n.T("到时重新下载各来源并重新生成"), func() {
			ui.Segmented(c.Key("agg-auto"), &d.auto, i18n.Options(autoLabels...)...).Label(i18n.T("自动更新"))
		})
		k.Row(c, "User-Agent", i18n.T("下载订阅链接来源时使用"), func() {
			ui.TextInput(c.Key("agg-ua"), &d.ua).Placeholder(i18n.T("留空使用默认 User-Agent")).Label("User-Agent").Width(260)
		})
		k.Row(c, i18n.T("通过代理更新"), "", func() {
			ui.Switch(c.Key("agg-via"), &d.viaProxy).Label(i18n.T("通过代理更新"))
		})
		k.Row(c, i18n.T("下载超时（秒）"), "", func() {
			ui.TextInput(c.Key("agg-timeout"), &d.timeout).Placeholder(i18n.Tf("留空为 %d 秒", defaultTimeoutSec)).Width(120).FontFeatures("tnum").Label(i18n.T("下载超时（秒）"))
		})
		if d.id == "" {
			k.Row(c, i18n.T("保存后立即使用"), "", func() {
				ui.Switch(c.Key("agg-use"), &d.use).Label(i18n.T("保存后立即使用"))
			})
		}
	})
	labels := subChoices(subs, d.id)
	var subLabels []string
	for _, l := range labels {
		subLabels = append(subLabels, l.label)
	}
	k.Section(c, i18n.T("来源"), i18n.T("各来源的节点按顺序合并。订阅链接可以是 Clash/Mihomo 配置，也可以是节点分享链接（含 Base64）。"), func() {
		remove := -1
		for i := range d.sources {
			f := &d.sources[i]
			ui.Column(c.Key(fmt.Sprintf("src-%d", f.uid))).Gap(8).Padding(10, 12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, i18n.Tf("来源 %d", i+1)).FontWeight(600).FontSize(12)
					ui.Segmented(c.Key("kind"), &f.kind, i18n.Options("订阅链接", "节点链接", "已有订阅")...).Label(i18n.T("来源类型"))
					ui.Spacer(c)
					ui.Switch(c.Key("on"), &f.enabled).Label(i18n.T("启用"))
					if ui.Button(c, "").Label(i18n.T("删除来源")).Tooltip(i18n.T("删除来源")).Padding(0, 4).Children(func() {
						ui.Text(c, "✕").FontSize(11)
					}).Clicked() {
						remove = i
					}
				})
				switch aggKinds[f.kind] {
				case model.AggSourceURL:
					ui.TextInput(c.Key("url"), &f.url).Placeholder("https://").Label(i18n.T("订阅链接")).FillWidth()
				case model.AggSourceLinks:
					ui.TextArea(c.Key("links"), &f.links).Font("monospace").FontSize(12).Lines(3, 10).
						Placeholder(i18n.T("每行一个：ss://、vmess://、vless://、trojan://、hysteria2://、tuic://、anytls:// 等")).Label(i18n.T("节点链接"))
				case model.AggSourceSub:
					if len(subLabels) == 0 {
						ui.Text(c, i18n.T("还没有其他订阅可选。")).FontSize(12).TextColor(t.TextMuted)
					} else {
						if f.sub == "" {
							f.sub = subLabels[0]
						}
						ui.Select(c.Key("sub"), &f.sub, subLabels).Label(i18n.T("订阅"))
						ui.Text(c, i18n.T("使用这个订阅上次下载的内容；先更新它，再更新聚合订阅即可拿到新节点。")).FontSize(11).TextColor(t.TextMuted)
					}
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, i18n.T("名称前缀")).FontSize(12).TextColor(t.TextMuted)
					ui.TextInput(c.Key("prefix"), &f.prefix).Placeholder(i18n.T("可选，例如「机场A · 」")).Label(i18n.T("名称前缀")).Width(220)
				})
			})
		}
		if remove >= 0 {
			d.sources = append(d.sources[:remove:remove], d.sources[remove+1:]...)
		}
		ui.Row(c).Gap(8).Padding(8, 12).Wrap().Children(func() {
			add := func(label, kind string) {
				ui.Button(c, "").Label(label).Disabled(len(d.sources) >= profile.AggMaxSources).Children(func() {
					k.Icon(c, "plus", 12)
					ui.Text(c, label)
				}).OnClick(func() { d.addSource(kind) })
			}
			add(i18n.T("订阅链接"), model.AggSourceURL)
			add(i18n.T("节点链接"), model.AggSourceLinks)
			add(i18n.T("已有订阅"), model.AggSourceSub)
		})
	})
}

func (p *Page) aggNodesTab(c *ui.Context, t *ui.Theme) {
	k := p.e.Kit
	d := &p.agg
	k.Section(c, i18n.T("筛选"), i18n.T("按节点的原始名称筛选，使用正则表达式。"), func() {
		k.Row(c, i18n.T("保留"), i18n.T("只保留名称匹配的节点；留空保留全部"), func() {
			ui.TextInput(c.Key("agg-inc"), &d.include).Placeholder("(?i)HK|JP|SG").Font("monospace").FontSize(12).Label(i18n.T("保留")).Width(300)
		})
		k.Row(c, i18n.T("排除"), i18n.T("去掉名称匹配的节点；默认去掉剩余流量、到期时间等信息条目"), func() {
			ui.TextInput(c.Key("agg-exc"), &d.exclude).Font("monospace").FontSize(12).Label(i18n.T("排除")).Width(300)
		})
	})
	k.Section(c, i18n.T("重命名"), i18n.T("筛选之后按顺序替换节点名称中匹配的部分，替换内容可用 $1 引用分组；最后加上来源的名称前缀，重名的节点自动编号。"), func() {
		remove := -1
		for i := range d.renames {
			ui.Row(c.Key(fmt.Sprintf("rn-%d", i))).Gap(8).PaddingX(12).MinHeight(36).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("pat"), &d.renames[i].Pattern).Placeholder(`^\[(.+?)\]\s*`).Font("monospace").FontSize(12).Grow(1).Basis(0).Label(i18n.T("匹配"))
				ui.Text(c, "→").TextColor(t.TextMuted)
				ui.TextInput(c.Key("rep"), &d.renames[i].Replace).Placeholder("$1 ").Font("monospace").FontSize(12).Grow(1).Basis(0).Label(i18n.T("替换为"))
				if ui.Button(c, "").Label(i18n.T("删除")).Tooltip(i18n.T("删除")).Padding(0, 4).Children(func() {
					ui.Text(c, "✕").FontSize(11)
				}).Clicked() {
					remove = i
				}
			})
		}
		if remove >= 0 {
			d.renames = append(d.renames[:remove:remove], d.renames[remove+1:]...)
		}
		ui.Row(c).PaddingX(12).MinHeight(36).Children(func() {
			ui.Button(c, "").Disabled(len(d.renames) >= profile.AggMaxRenames).Children(func() {
				k.Icon(c, "plus", 12)
				ui.Text(c, i18n.T("添加"))
			}).OnClick(func() { d.renames = append(d.renames, model.RenameRule{}) })
		})
	})
}

func (p *Page) aggGroupsTab(c *ui.Context, t *ui.Theme) {
	k := p.e.Kit
	d := &p.agg
	k.Section(c, i18n.T("代理组"), i18n.Tf("「%s」总会生成，包含下面的组和全部节点。", profile.AggGroupSelect), func() {
		k.Row(c, i18n.T("自动选择"), i18n.T("按延迟自动选用最快的节点"), func() {
			ui.Switch(c.Key("agg-autogrp"), &d.autoGrp).Label(i18n.T("自动选择"))
		})
		ui.Column(c).Gap(6).Padding(8, 12).Children(func() {
			ui.Text(c, i18n.T("地区分组"))
			ui.Text(c, i18n.T("按名称把节点归入地区，组内自动选用最快的；没有节点的地区不生成。")).FontSize(11).TextColor(t.TextMuted)
			ui.Row(c).Gap(12).Wrap().Children(func() {
				for _, r := range profile.AggregateRegions {
					v := d.regions[r.ID]
					if ui.Checkbox(c.Key("rg-"+r.ID), &v, i18n.T(r.Group)).Changed() {
						d.regions[r.ID] = v
					}
				}
			})
		})
	})
	k.Section(c, i18n.T("分流规则"), i18n.Tf("规则使用澜流自带的 GeoSite / GeoIP 数据库，启动内核不需要下载规则。局域网与私有地址始终直连，没有命中的流量走「%s」。", profile.AggGroupFinal), func() {
		ui.Column(c).Gap(6).Padding(8, 12).Children(func() {
			ui.Row(c).Gap(12).Wrap().Children(func() {
				for _, m := range profile.AggregateModules {
					v := d.modules[m.ID]
					if ui.Checkbox(c.Key("md-"+m.ID), &v, i18n.T(m.Group)).Changed() {
						d.modules[m.ID] = v
					}
				}
			})
			ui.Text(c, i18n.Tf("勾选的服务各自生成一个代理组，可在代理页单独选择节点；「国外网站」走「%s」，「国内直连」直接连接。", profile.AggGroupSelect)).FontSize(11).TextColor(t.TextMuted)
		})
	})
}

func (p *Page) aggPreviewTab(c *ui.Context, t *ui.Theme, subs []model.Subscription) {
	k := p.e.Kit
	d := &p.agg
	switch {
	case d.previewing:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(16, 0).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, i18n.T("正在下载来源并生成配置…")).TextColor(t.TextMuted)
		})
		return
	case d.preview == nil && d.previewErr == "":
		k.Empty(c, "subs", i18n.T("还没有预览"), i18n.T("预览会下载各来源并生成配置，但不保存。"), func() {
			if ui.PrimaryButton(c, i18n.T("生成预览")).Clicked() {
				p.previewAggregate(subs)
			}
		})
		return
	}
	if !reflect.DeepEqual(d.previewOf, d.spec(subs)) {
		k.Banner(c, "info", i18n.T("设置改过了，预览可能已过时。"), func() {
			if ui.Button(c, i18n.T("重新预览")).Clicked() {
				p.previewAggregate(subs)
			}
		})
	}
	if d.previewErr != "" {
		k.Banner(c, "error", d.previewErr, nil)
	}
	pv := d.preview
	if pv == nil {
		return
	}
	k.Section(c, i18n.T("来源"), "", func() {
		for i, r := range pv.Sources {
			idx := aggSourceIndex(d.previewOf, r.ID, i)
			label := i18n.Tf("来源 %d", idx+1)
			ui.Row(c.Key("pvs-"+r.ID)).Gap(8).Padding(8, 12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, label).FontWeight(600).FontSize(12).Width(56).Shrink(0)
				if idx < len(d.previewOf.Sources) {
					ui.Text(c, aggSourceDesc(d.previewOf.Sources[idx], subs)).FontSize(12).TextColor(t.TextMuted).SingleLine().Grow(1).Basis(0).Shrink(1)
				}
				if r.Error != "" {
					ui.Text(c, r.Error).FontSize(12).TextColor(t.Danger).Grow(2).Basis(0).Shrink(1)
					return
				}
				msg := i18n.Tf("读到 %d 个节点，保留 %d 个", r.Found, r.Kept)
				if r.Skipped > 0 {
					msg += i18n.Tf("，%d 条无法识别", r.Skipped)
				}
				ui.Text(c, msg).FontSize(12).SingleLine().Shrink(0)
				if r.Total > 0 {
					ui.Text(c, fmt.Sprintf("%s / %s GB", env.FmtGB(r.Upload+r.Download), env.FmtGB(r.Total))).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().Shrink(0)
				}
			})
		}
	})
	for _, w := range pv.Warnings {
		k.Banner(c, "warn", w, nil)
	}
	k.Section(c, i18n.Tf("结果：%d 个节点 · %d 个代理组 · %d 条规则", len(pv.Nodes), len(pv.Groups), pv.Rules), "", func() {
		ui.Row(c).Gap(6).Padding(8, 12).Wrap().Children(func() {
			for _, g := range pv.Groups {
				k.Tag(c, g, "gray")
			}
		})
		ui.Column(c).Gap(2).Padding(4, 12, 8, 12).Children(func() {
			for i, n := range pv.Nodes {
				if i == aggPreviewNodes {
					ui.Text(c, i18n.Tf("还有 %d 个节点未列出", len(pv.Nodes)-aggPreviewNodes)).FontSize(11).TextColor(t.TextMuted)
					break
				}
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, n.Name).FontSize(12).SingleLine().Shrink(1).Selectable()
					ui.Text(c, n.Type).FontSize(11).TextColor(t.TextMuted)
				})
			}
		})
	})
	if ui.Button(c, map[bool]string{false: i18n.T("查看生成的 YAML"), true: i18n.T("收起 YAML")}[d.showYAML]).Clicked() {
		d.showYAML = !d.showYAML
	}
	if d.showYAML {
		yml := pv.YAML
		ui.TextArea(c.Key("agg-yaml"), &yml).Font("monospace").FontSize(12).Lines(12, 40).ReadOnly(true).Label(i18n.T("生成的 YAML"))
	}
}

// aggSourceDesc names a source in the preview: a link's host, the
// subscription it reads, or 节点链接.
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
	}
	return i18n.T("节点链接")
}

// aggSourceIndex is the position of source id in spec (for its label),
// fallback when it is not there.
func aggSourceIndex(spec model.AggregateSpec, id string, fallback int) int {
	for i, s := range spec.Sources {
		if s.ID == id && id != "" {
			return i
		}
	}
	return fallback
}

// previewAggregate builds the profile in the background.
func (p *Page) previewAggregate(subs []model.Subscription) {
	d := &p.agg
	spec := d.spec(subs)
	if msg := d.check(spec); msg != "" {
		d.err = msg
		return
	}
	d.err, d.previewing = "", true
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
	spec := d.spec(subs)
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
		p.e.Toast(i18n.Tf("已保存「%s」", saved.Name), false)
	})
}
