package subs

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// The left column of the aggregate editor: the 配置生成器.

// aggNodeRows is how many nodes 节点管理 lists.
const aggNodeRows = 300

// aggIconButton draws a small borderless icon button.
func (p *Page) aggIconButton(c *ui.Context, icon, label string, disabled bool) bool {
	return ui.Button(c, "").Label(label).Tooltip(label).Disabled(disabled).Padding(3, 4).MinHeight(24).Background(ui.Color{}).
		Border(0, ui.Color{}).Children(func() { p.e.Kit.Icon(c, icon, 14) }).Clicked()
}

// aggTinyButton draws a half-height icon button, two of which stack (上移 / 下移).
func (p *Page) aggTinyButton(c *ui.Context, icon, label string, disabled bool) bool {
	return ui.Button(c, "").Label(label).Tooltip(label).Disabled(disabled).Padding(0, 3).MinHeight(12).Height(12).Background(ui.Color{}).
		Border(0, ui.Color{}).Children(func() { p.e.Kit.Icon(c, icon, 10) }).Clicked()
}

func (p *Page) aggLeft(c *ui.Context, subs []model.Subscription) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	aggPanel(c, "agg-left", 1.1, func() {
		ui.Box(c).TextColor(t.Accent).Children(func() { k.Icon(c, "sliders", 16) })
		ui.Text(c, i18n.T("配置生成器")).FontSize(15).FontWeight(600).SingleLine().Shrink(1)
		ui.Spacer(c)
		ui.Segmented(c.Key("agg-mode"), &d.mode, i18n.T("快捷模式"), i18n.T("高级模式")).Label(i18n.T("模式"))
	}, func() {
		if d.err != "" {
			k.Banner(c, "error", d.err, nil)
		}
		if d.mode == aggModeQuick {
			p.aggSourceCards(c, subs)
			p.aggTemplatePicker(c)
			p.aggSection(c, "settings", "settings", i18n.T("订阅设置"), "", func() { p.aggSettings(c) })
			return
		}
		p.aggSection(c, "sources", "layers", i18n.T("节点导入"), i18n.Tf("%d 个导入源", len(d.sources)), func() { p.aggSourceCards(c, subs) })
		nodes := i18n.T("无节点")
		if d.preview != nil {
			nodes = i18n.Tf("%d 个节点", len(d.preview.Nodes))
		}
		p.aggSection(c, "nodes", "rules", i18n.T("节点管理"), nodes, func() { p.aggNodes(c) })
		p.aggSection(c, "relays", "link", i18n.T("中转代理组"), i18n.T("可选"), func() { p.aggRelays(c) })
		on, total := 0, len(profile.AggregateModules)
		for _, g := range d.spec.Groups {
			if !g.Custom {
				on++
			}
		}
		p.aggSection(c, "groups", "layers", i18n.T("分流代理组"), fmt.Sprintf("%d/%d", on, total), func() { p.aggGroups(c) })
		p.aggSection(c, "rules", "search", i18n.T("自定义分流规则"), strconv.Itoa(len(d.spec.Rules)), func() { p.aggCustomRules(c) })
		views := d.ruleViews()
		p.aggSection(c, "order", "rules", i18n.T("规则管理"), i18n.Tf("可调 %d / 全部 %d", max(len(views)-1, 0), len(views)), func() { p.aggRuleOrder(c, views) })
		p.aggSection(c, "base", "doc", i18n.T("基础和 DNS 配置"), "YAML", func() { p.aggBase(c) })
		p.aggSection(c, "settings", "settings", i18n.T("订阅设置"), "", func() { p.aggSettings(c) })
	}, func() {
		gen := ui.Button(c, "").Label(i18n.T("生成配置")).Disabled(d.previewing || d.saving)
		gen.Children(func() {
			if d.previewing {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "bolt", 13)
			}
			ui.Text(c, i18n.T("生成配置")).SingleLine()
		})
		if gen.Clicked() {
			p.previewAggregate(subs)
		}
		tpl := ui.Button(c, "").Label(i18n.T("保存为模板"))
		tpl.Children(func() {
			k.Icon(c, "upload", 13)
			ui.Text(c, i18n.T("保存为模板")).SingleLine()
		})
		if tpl.Clicked() {
			d.dlg.saveTpl, d.dlg.tplName, d.dlg.tplErr = true, d.name, ""
		}
	})
}

// aggSection draws a collapsible section of the advanced mode: a chevron,
// an icon, the title and a badge on the right.
func (p *Page) aggSection(c *ui.Context, key, icon, title, badge string, body func()) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	open := d.sections[key]
	cb := ui.CollapsibleBase(c.Key("sec-"+key), &open)
	cb.Trigger.Gap(8).AlignItems(ui.Center).Padding(6, 2).Label(title).Children(func() {
		ui.Box(c).TextColor(t.TextMuted).Rotate(90 * cb.Progress()).Children(func() { k.Icon(c, "chevron", 12) })
		ui.Box(c).TextColor(t.Accent).Children(func() { k.Icon(c, icon, 14) })
		ui.Text(c, title).FontSize(13.5).FontWeight(600).SingleLine().Shrink(1)
		ui.Spacer(c)
		if badge != "" {
			k.Tag(c, badge, "accent")
		}
	})
	cb.Panel(func() {
		ui.Column(c).Gap(8).Padding(2, 0, 6, 0).Children(body)
	})
	d.sections[key] = open
}

// ---- sources ----

var aggKindIcons = []string{"link", "doc", "server", "subs"}

func aggKindLabel(kind int) string {
	return []string{i18n.T("订阅链接"), i18n.T("YAML 配置"), i18n.T("节点链接"), i18n.T("已有订阅")}[kind]
}

func (p *Page) aggSourceCards(c *ui.Context, subs []model.Subscription) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	labels := subChoices(subs, d.id)
	var subLabels []string
	for _, l := range labels {
		subLabels = append(subLabels, l.label)
	}
	remove, swap := -1, -1
	for i := range d.sources {
		f := &d.sources[i]
		ui.Column(c.Key(fmt.Sprintf("src-%d", f.uid))).Gap(6).Padding(8, 10).Radius(10).Background(t.Background).
			Border(1, t.Border.Alpha(0.6)).Opacity(map[bool]float32{true: 1, false: 0.6}[f.enabled]).Children(func() {
			ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
				for ki, icon := range aggKindIcons {
					sel := f.kind == ki
					b := ui.Button(c, "").Label(aggKindLabel(ki)).Tooltip(aggKindLabel(ki)).Padding(3, 3).MinHeight(22).Selected(sel).Border(0, ui.Color{})
					if sel {
						b.Background(t.Accent.Alpha(0.18)).TextColor(t.Accent)
					} else {
						b.Background(ui.Color{}).TextColor(t.TextMuted)
					}
					if b.Children(func() { k.Icon(c, icon, 14) }).Clicked() {
						f.kind = ki
					}
				}
				title := aggKindLabel(f.kind) + " #" + strconv.Itoa(i+1)
				if strings.TrimSpace(f.name) != "" {
					title = f.name
				}
				ui.Text(c, title).FontSize(12.5).FontWeight(500).SingleLine().Shrink(1).MinWidth(56).Margin(0, 0, 0, 4)
				if r := d.sourceReport(f.id); r != nil {
					if r.Error != "" {
						ui.Text(c, "✕ "+i18n.T("失败")).FontSize(11).Padding(1, 6).Radius(999).Background(t.Danger.Alpha(0.14)).
							TextColor(t.Danger).SingleLine().Shrink(0).Tooltip(r.Error)
					} else {
						k.Tag(c, i18n.Tf("✓ %d 节点", r.Kept), "green")
					}
				}
				if !f.enabled {
					k.Tag(c, i18n.T("已停用"), "gray")
				}
				ui.Spacer(c)
				ui.Column(c).Gap(0).Shrink(0).Children(func() {
					if p.aggTinyButton(c, "up", i18n.T("上移"), i == 0) {
						swap = i - 1
					}
					if p.aggTinyButton(c, "down", i18n.T("下移"), i == len(d.sources)-1) {
						swap = i
					}
				})
				if p.aggIconButton(c, "expand", i18n.T("展开编辑"), false) {
					d.dlg.src, d.dlg.srcOpen = i, true
				}
				if p.aggIconButton(c, "check", i18n.T("解析并预览"), d.previewing) {
					p.previewAggregate(subs)
				}
				if p.aggIconButton(c, "close", i18n.T("删除来源"), false) {
					remove = i
				}
			})
			p.aggSourceBody(c, f, subLabels, 3, 6)
		})
	}
	if remove >= 0 {
		d.sources = append(d.sources[:remove:remove], d.sources[remove+1:]...)
	}
	if swap >= 0 && swap+1 < len(d.sources) {
		d.sources[swap], d.sources[swap+1] = d.sources[swap+1], d.sources[swap]
	}
	add := ui.Button(c, "").Label(i18n.T("添加订阅/节点源")).Disabled(len(d.sources) >= profile.AggMaxSources).FillWidth().
		Background(ui.Color{}).Border(1, t.Border).BorderStyle(ui.BorderDashed).Radius(10).MinHeight(32)
	add.Children(func() {
		k.Icon(c, "plus", 12)
		ui.Text(c, i18n.T("添加订阅/节点源")).FontSize(12.5)
	})
	if add.Clicked() {
		d.addSource(model.AggSourceURL)
	}
}

// aggSourceBody draws the input of a source for its kind; minL / maxL are
// the text areas' line counts.
func (p *Page) aggSourceBody(c *ui.Context, f *aggSrcForm, subLabels []string, minL, maxL int) {
	t := c.Theme()
	switch aggKinds[f.kind] {
	case model.AggSourceURL:
		ui.TextInput(c.Key("url"), &f.url).Placeholder("https://example.com/sub?token=xxx").Label(i18n.T("订阅链接")).FillWidth()
	case model.AggSourceYAML:
		ui.TextArea(c.Key("yaml"), &f.yaml).Font("monospace").FontSize(12).Lines(max(minL, 6), max(maxL, 6)).
			Placeholder("proxies:\n  - name: " + i18n.T("节点名称") + "\n    type: vmess\n    ...").Label(i18n.T("YAML 配置"))
	case model.AggSourceLinks:
		// Tall enough for the example of every scheme.
		ui.TextArea(c.Key("links"), &f.links).Font("monospace").FontSize(12).Lines(max(minL, 10), max(maxL, 10)).
			Placeholder("ss://...\nssr://...\nvmess://...\nvless://...\ntrojan://...\nanytls://...\nhysteria2://... / hy2://...\ntuic://...\n(socks5://... / http://...)").Label(i18n.T("节点链接"))
		ui.Text(c, i18n.T("每行一条分享链接，也可以粘贴 Clash YAML 格式的节点（单个、列表或带 proxies 的配置），两种可以混在一起。")).FontSize(11).TextColor(t.TextMuted)
	case model.AggSourceSub:
		if len(subLabels) == 0 {
			ui.Text(c, i18n.T("还没有其他订阅可选。")).FontSize(12).TextColor(t.TextMuted)
			return
		}
		if f.sub == "" {
			f.sub = subLabels[0]
		}
		ui.Select(c.Key("sub"), &f.sub, subLabels).Label(i18n.T("订阅"))
		ui.Text(c, i18n.T("使用这个订阅上次下载的内容；先更新它，再更新聚合订阅即可拿到新节点。")).FontSize(11).TextColor(t.TextMuted)
	}
}

// ---- quick mode: templates ----

func (p *Page) aggTemplatePicker(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	ui.Text(c, i18n.T("选择模板")).FontSize(12.5).TextColor(t.TextMuted).Margin(6, 0, 0, 2)
	card := func(key string, selected bool, title, desc string, right func(), onClick func()) {
		b := ui.Button(c.Key(key), "").Label(title).FillWidth().Padding(10, 12).Radius(10).Justify(ui.Start).Selected(selected)
		if selected {
			b.Background(t.Accent.Alpha(0.12)).Border(1.5, t.Accent)
		} else {
			b.Background(t.Background).Border(1, t.Border.Alpha(0.6))
		}
		b.TextColor(t.Text) // a selected button would take the accent's text color
		b.Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Grow(1).Children(func() {
				ring := t.Border
				if selected {
					ring = t.Accent
				}
				ui.Box(c).Size(14, 14).Radius(7).Border(1.5, ring).Center().Shrink(0).Children(func() {
					if selected {
						ui.Box(c).Size(7, 7).Radius(4).Background(t.Accent)
					}
				})
				ui.Column(c).Gap(2).Grow(1).Shrink(1).AlignItems(ui.Start).Children(func() {
					ui.Text(c, title).FontSize(13.5).FontWeight(600).SingleLine()
					ui.Text(c, desc).FontSize(12).TextColor(t.TextMuted).SingleLine()
				})
				right()
			})
		})
		if b.Clicked() {
			onClick()
		}
	}
	for _, tp := range profile.AggregateTemplates {
		groups := tp.Groups()
		card("tpl-"+tp.ID, d.spec.Template == tp.ID, i18n.T(tp.Label), i18n.T(tp.Desc), func() {
			ui.Column(c).Gap(2).AlignItems(ui.End).Shrink(0).Children(func() {
				ui.Text(c, i18n.Tf("%d 代理组", len(groups))).FontSize(12).FontFeatures("tnum").SingleLine()
				ui.Text(c, i18n.Tf("%d 规则集", tp.RuleSets())).FontSize(12).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine()
			})
		}, func() { d.applyTemplate(tp.ID) })
	}
	mine := strings.HasPrefix(d.spec.Template, "my:")
	desc := i18n.T("选择并应用你保存的配置模板")
	if mine {
		for _, tp := range d.templates {
			if "my:"+tp.ID == d.spec.Template {
				desc = i18n.Tf("当前：%s", tp.Name)
			}
		}
	}
	card("tpl-mine", mine, i18n.T("我的模板"), desc, func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).TextColor(t.TextMuted).Children(func() {
			ui.Text(c, i18n.Tf("%d 个", len(d.templates))).FontSize(12).SingleLine()
			k.Icon(c, "chevron", 11)
		})
	}, func() { d.dlg.myTpl = true })
	if d.spec.Template == "" {
		ui.Text(c, i18n.T("当前使用自定义的代理组，可在高级模式中查看和修改。")).FontSize(11.5).TextColor(t.TextMuted).Margin(0, 2)
	}
}

// ---- advanced: nodes ----

func (p *Page) aggNodes(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
		ui.SearchField(c.Key("node-q"), &d.nodeQ).Placeholder(i18n.T("搜索节点…")).Label(i18n.T("搜索节点")).Grow(1).Basis(120)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(0, 8).MinHeight(28).Radius(8).Border(1, t.Border.Alpha(0.6)).Children(func() {
			ui.Text(c, i18n.T("监听端口")).FontSize(12).SingleLine()
			ui.Switch(c.Key("listen-on"), &d.listenOn).Label(i18n.T("监听端口"))
		})
		if ui.Button(c, i18n.T("批量编辑")).Clicked() {
			d.dlg.openBatch(d.spec)
		}
		if ui.Button(c, i18n.T("自动处理")).Clicked() {
			d.dlg.openAuto(d.spec)
		}
	})
	if d.listenOn {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("起始端口")).FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c.Key("listen"), &d.listen).Placeholder("7900").Width(90).FontFeatures("tnum").Label(i18n.T("起始端口"))
			ui.Text(c, i18n.T("每个节点一个 mixed 端口，按节点顺序依次加一，局域网设备可直接指定节点。")).FontSize(11.5).TextColor(t.TextMuted).Shrink(1)
		})
	}
	if d.preview == nil {
		ui.Text(c, i18n.T("请先在上方导入节点，再点「生成配置」。")).FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).Padding(10, 0)
		return
	}
	q := strings.ToLower(strings.TrimSpace(d.nodeQ))
	shown := 0
	var move struct {
		name string
		dir  int
	}
	var hide string
	ui.Column(c).Gap(4).Children(func() {
		for i, n := range d.preview.Nodes {
			if q != "" && !strings.Contains(strings.ToLower(n.Name), q) {
				continue
			}
			if shown == aggNodeRows {
				ui.Text(c, i18n.Tf("还有更多节点未列出，请用搜索查找")).FontSize(11.5).TextColor(t.TextMuted)
				break
			}
			shown++
			ui.Row(c.Key("node-"+n.Name)).Gap(8).AlignItems(ui.Center).Padding(4, 8).Radius(8).Background(t.Background).
				Opacity(map[bool]float32{true: 0.45, false: 1}[n.Hidden]).Children(func() {
				ui.Text(c, strings.ToUpper(n.Type)).FontSize(10).FontWeight(600).Padding(1, 5).Radius(4).
					Background(t.Danger.Alpha(0.14)).TextColor(t.Danger).SingleLine().Shrink(0)
				ui.Text(c, n.Name).FontSize(12.5).SingleLine().Shrink(1).Grow(1).Selectable()
				if n.Relay != "" {
					k.Tag(c, "⇢ "+n.Relay, "accent")
				}
				if n.Port > 0 {
					k.Tag(c, ":"+strconv.Itoa(n.Port), "gray")
				}
				if p.aggIconButton(c, "up", i18n.T("上移"), i == 0 || q != "") {
					move.name, move.dir = n.Name, -1
				}
				if p.aggIconButton(c, "down", i18n.T("下移"), i == len(d.preview.Nodes)-1 || q != "") {
					move.name, move.dir = n.Name, 1
				}
				icon, label := "eye", i18n.T("隐藏节点")
				if n.Hidden {
					icon, label = "eye-off", i18n.T("显示节点")
				}
				if p.aggIconButton(c, icon, label, false) {
					hide = n.Name
				}
			})
		}
	})
	if move.name != "" {
		d.moveNode(move.name, move.dir)
	}
	if hide != "" {
		d.toggleHidden(hide)
	}
	if len(d.spec.NodeOrder) > 0 || len(d.spec.Hidden) > 0 {
		if ui.Button(c, i18n.T("恢复节点顺序与显示")).Clicked() {
			d.spec.NodeOrder, d.spec.Hidden = nil, nil
		}
	}
}

// ---- advanced: relays ----

func (p *Page) aggRelays(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	ui.Text(c, i18n.T("落地节点经中转组连接（dialer-proxy）。两项都按节点名称用正则匹配；中转节点本身不会再经中转。")).FontSize(11.5).TextColor(t.TextMuted)
	remove := -1
	for i := range d.spec.Relays {
		r := &d.spec.Relays[i]
		ui.Column(c.Key(fmt.Sprintf("relay-%d", i))).Gap(6).Padding(8, 10).Radius(10).Background(t.Background).Border(1, t.Border.Alpha(0.6)).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c.Key("name"), &r.Name).Placeholder(i18n.T("中转组名称")).Label(i18n.T("中转组名称")).Grow(1)
				if p.aggIconButton(c, "trash", i18n.T("删除中转组"), false) {
					remove = i
				}
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.T("中转节点")).FontSize(12).TextColor(t.TextMuted).Width(56).Shrink(0)
				ui.TextInput(c.Key("relay"), &r.Relay).Placeholder(i18n.T("中转|IPLC|IEPL")).Font("monospace").FontSize(12).Label(i18n.T("中转节点")).Grow(1)
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.T("落地节点")).FontSize(12).TextColor(t.TextMuted).Width(56).Shrink(0)
				ui.TextInput(c.Key("landing"), &r.Landing).Placeholder(i18n.T("落地|家宽|Residential")).Font("monospace").FontSize(12).Label(i18n.T("落地节点")).Grow(1)
			})
		})
	}
	if remove >= 0 {
		d.spec.Relays = append(d.spec.Relays[:remove:remove], d.spec.Relays[remove+1:]...)
	}
	ui.MenuButton(c.Key("relay-add"), i18n.T("添加中转组"), func(m *ui.Menu) {
		for _, preset := range profile.AggRelayPresets {
			if m.Item(preset.Name).Chosen() {
				d.spec.Relays = append(d.spec.Relays, preset)
			}
		}
		m.Separator()
		if m.Item(i18n.T("自定义名称")).Chosen() {
			d.spec.Relays = append(d.spec.Relays, model.AggregateRelay{Name: "🔗 " + i18n.T("中转")})
		}
	}).Disabled(len(d.spec.Relays) >= profile.AggMaxRelays)
	_ = k
}

// ---- advanced: groups ----

func (p *Page) aggGroups(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	src := 0
	if d.spec.RuleSource == model.AggRulesRemote {
		src = 1
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Text(c, i18n.T("规则来源")).FontSize(12).TextColor(t.TextMuted)
		if ui.Segmented(c.Key("rule-src"), &src, i18n.T("自带数据库"), i18n.T("远程规则集")).Label(i18n.T("规则来源")).Changed() {
			d.spec.RuleSource = []string{model.AggRulesGeo, model.AggRulesRemote}[src]
		}
	})
	if src == 1 {
		ui.TextInput(c.Key("rule-base"), &d.spec.RuleBaseURL).Placeholder(profile.DefaultRuleBaseURL).Font("monospace").FontSize(11.5).
			Label(i18n.T("规则集 URL")).FillWidth()
		ui.Text(c, i18n.T("规则集（.mrs）由内核首次启动时下载，之后每天更新；留空使用 MetaCubeX meta-rules-dat。")).FontSize(11.5).TextColor(t.TextMuted)
	} else {
		ui.Text(c, i18n.T("使用澜流自带的 GeoSite / GeoIP 数据库，启动内核不需要下载规则；少数只有远程版的 IP 规则集会被跳过。")).FontSize(11.5).TextColor(t.TextMuted)
	}
	ui.Text(c, i18n.T("分流规则组")).FontSize(12).TextColor(t.TextMuted).Margin(4, 0, 0, 2)
	for _, cat := range profile.AggregateCategories {
		var mods []profile.AggregateModule
		on := 0
		for _, m := range profile.AggregateModules {
			if m.Category == cat.ID {
				mods = append(mods, m)
				if d.groupIndex(m.ID) >= 0 {
					on++
				}
			}
		}
		p.aggCategory(c, cat.ID, i18n.T(cat.Label), fmt.Sprintf("%d/%d", on, len(mods)), func() {
			for _, m := range mods {
				p.aggModuleRow(c, m)
			}
		})
	}
	var custom []int
	for i, g := range d.spec.Groups {
		if g.Custom {
			custom = append(custom, i)
		}
	}
	p.aggCategory(c, "custom", i18n.T("自定义分组"), strconv.Itoa(len(custom)), func() {
		for _, i := range custom {
			g := d.spec.Groups[i]
			ui.Row(c.Key("cg-"+g.ID)).Gap(8).AlignItems(ui.Center).Padding(6, 10).Radius(8).Background(t.Background).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
					ui.Text(c, g.Name).FontSize(13).FontWeight(500).SingleLine()
					meta := typeLabel(profile.AggGroupType(g)) + " · " + i18n.Tf("%d 规则集", len(g.Rules))
					if g.Filter != "" {
						meta += " · " + g.Filter
					}
					ui.Text(c, meta).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
				})
				if p.aggIconButton(c, "edit", i18n.T("编辑分组"), false) {
					d.dlg.openGroup(d.spec, i)
				}
				if p.aggIconButton(c, "trash", i18n.T("删除分组"), false) {
					d.spec.Groups = append(d.spec.Groups[:i:i], d.spec.Groups[i+1:]...)
					d.dropTargets(g.ID)
					d.spec.Template = ""
				}
			})
		}
		add := ui.Button(c, "").Label(i18n.T("添加自定义分组")).Disabled(len(d.spec.Groups) >= profile.AggMaxGroups)
		add.Children(func() {
			k.Icon(c, "plus", 12)
			ui.Text(c, i18n.T("添加自定义分组"))
		})
		if add.Clicked() {
			d.dlg.openGroup(d.spec, -1)
		}
	})
}

// aggCategory draws a collapsible category row of 分流规则组.
func (p *Page) aggCategory(c *ui.Context, id, title, count string, body func()) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	open := d.cats[id]
	cb := ui.CollapsibleBase(c.Key("cat-"+id), &open)
	cb.Trigger.Gap(8).AlignItems(ui.Center).Padding(7, 10).Radius(8).Background(t.Background).Label(title).Children(func() {
		ui.Box(c).TextColor(t.TextMuted).Rotate(90 * cb.Progress()).Children(func() { k.Icon(c, "chevron", 11) })
		ui.Text(c, title).FontSize(12.5).SingleLine()
		ui.Spacer(c)
		k.Tag(c, count, "gray")
	})
	cb.Panel(func() {
		ui.Column(c).Gap(4).Padding(4, 0, 4, 12).Children(body)
	})
	d.cats[id] = open
}

func (p *Page) aggModuleRow(c *ui.Context, m profile.AggregateModule) {
	t := c.Theme()
	d := &p.agg
	i := d.groupIndex(m.ID)
	on := i >= 0
	g := model.AggregateGroup{ID: m.ID}
	if on {
		g = d.spec.Groups[i]
	}
	ui.Row(c.Key("mod-"+m.ID)).Gap(8).AlignItems(ui.Center).Padding(6, 10).Radius(8).Background(t.Background).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, groupLabel(g)).FontSize(13).FontWeight(500).SingleLine().Shrink(1)
				if on && p.aggIconButton(c, "edit", i18n.T("编辑分组"), false) {
					d.dlg.openGroup(d.spec, i)
				}
			})
			meta := i18n.T(m.Desc) + " · " + typeLabel(profile.AggGroupType(g))
			if n := len(profile.AggGroupRules(g)); n > 0 {
				meta += " · " + i18n.Tf("%d 规则集", n)
			}
			ui.Text(c, meta).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
		})
		v := on
		sw := ui.Switch(c.Key("on"), &v).Label(groupLabel(g)).Disabled(m.Required)
		if m.Required {
			sw.Tooltip(i18n.T("必需的代理组"))
		}
		if v != on {
			d.setModule(m.ID, v)
		}
	})
}

// ---- advanced: custom rules ----

func (p *Page) aggCustomRules(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	box := func(key string, body func()) {
		ui.Column(c.Key(key)).Gap(8).Padding(10, 12).Radius(10).Background(t.Background).Border(1, t.Border.Alpha(0.6)).Children(body)
	}
	box("rl-lib", func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("方法一：搜索规则集")).FontSize(12.5).FontWeight(600)
			k.Tag(c, i18n.T("推荐"), "orange")
			ui.Spacer(c)
			ui.Text(c, i18n.Tf("%d 规则", profile.RuleSetCount())).FontSize(11.5).TextColor(t.TextMuted).FontFeatures("tnum")
		})
		lib := ui.Button(c, "").Label(i18n.T("打开规则库")).FillWidth()
		lib.Children(func() {
			k.Icon(c, "search", 12)
			ui.Text(c, i18n.Tf("从 %d 个规则集中搜索并添加", profile.RuleSetCount()))
		})
		if lib.Clicked() {
			d.dlg.lib, d.dlg.libQ, d.dlg.libMsg = true, "", ""
		}
	})
	labels, ids := d.targetChoices()
	if d.ruleTarget == "" || indexOf(labels, d.ruleTarget) < 0 {
		d.ruleTarget = labels[0]
	}
	box("rl-manual", func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("方法二：手动添加规则")).FontSize(12.5).FontWeight(600)
			ui.Spacer(c)
			if ui.Button(c, i18n.T("批量导入")).Clicked() {
				d.dlg.bulk, d.dlg.bulkText, d.dlg.bulkMsg = true, "", ""
			}
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Select(c.Key("rt"), &d.ruleType, profile.AggregateRuleTypes()).Label(i18n.T("规则类型"))
			ui.TextInput(c.Key("rv"), &d.ruleValue).Placeholder(i18n.T("规则值，例如 example.com")).Label(i18n.T("规则值")).Grow(1).Basis(140)
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Select(c.Key("rtg"), &d.ruleTarget, labels).Label(i18n.T("目标代理组")).Grow(1).Basis(140)
			if d.ruleType == model.AggRuleIPCIDR || d.ruleType == model.AggRuleIPCIDR6 {
				ui.Checkbox(c.Key("rnr"), &d.ruleNoRes, "no-resolve")
			}
			add := ui.PrimaryButton(c, "").Label(i18n.T("添加规则"))
			add.Children(func() {
				k.Icon(c, "plus", 12)
				ui.Text(c, i18n.T("添加规则"))
			})
			if add.Clicked() {
				r := model.AggregateRule{Type: d.ruleType, Value: d.ruleValue, Target: ids[indexOf(labels, d.ruleTarget)], NoResolve: d.ruleNoRes}
				if msg := d.addRule(r); msg != "" {
					p.e.Toast(msg, true)
				} else {
					d.ruleValue = ""
				}
			}
		})
	})
	if len(d.spec.Rules) == 0 {
		return
	}
	remove := -1
	ui.Column(c).Gap(4).Children(func() {
		for i, r := range d.spec.Rules {
			tl := r.Target
			if j := indexOf(ids, r.Target); j >= 0 {
				tl = labels[j]
			}
			ui.Row(c.Key(fmt.Sprintf("cr-%d", i))).Gap(8).AlignItems(ui.Center).Padding(5, 10).Radius(8).Background(t.Background).Children(func() {
				ui.Text(c, r.Type).FontSize(10.5).FontWeight(600).TextColor(t.Accent).SingleLine().Shrink(0)
				ui.Text(c, r.Value).FontSize(12.5).Font("monospace").SingleLine().Shrink(1).Grow(1).Selectable()
				ui.Text(c, "→ "+tl).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1)
				if p.aggIconButton(c, "trash", i18n.T("删除规则"), false) {
					remove = i
				}
			})
		}
	})
	if remove >= 0 {
		d.spec.Rules = append(d.spec.Rules[:remove:remove], d.spec.Rules[remove+1:]...)
	}
}

// ---- advanced: rule order ----

func (p *Page) aggRuleOrder(c *ui.Context, views []model.AggregateRuleView) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("全部规则可调整顺序，MATCH 固定在最后。")).FontSize(11.5).TextColor(t.TextMuted).Grow(1).Shrink(1)
		if len(d.spec.RuleOrder) > 0 && ui.Button(c, i18n.T("恢复默认顺序")).Clicked() {
			d.spec.RuleOrder = nil
		}
	})
	move, dir := -1, 0
	ui.Column(c).Gap(4).Children(func() {
		for i, v := range views {
			last := i == len(views)-1
			ui.Row(c.Key("ro-"+v.Key)).Gap(8).AlignItems(ui.Center).Padding(5, 8).Radius(8).Background(t.Background).Children(func() {
				ui.Text(c, strconv.Itoa(i+1)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Width(22).TextAlign(ui.End).Shrink(0)
				ui.Column(c).Gap(1).Grow(1).Shrink(1).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, v.Label).FontSize(12.5).SingleLine().Shrink(1)
						if v.Custom {
							k.Tag(c, i18n.T("自定义"), "accent")
						}
					})
					ui.Text(c, v.Line).FontSize(11).Font("monospace").TextColor(t.TextMuted).SingleLine()
				})
				if !last {
					if p.aggIconButton(c, "up", i18n.T("上移"), i == 0) {
						move, dir = i, -1
					}
					if p.aggIconButton(c, "down", i18n.T("下移"), i >= len(views)-2) {
						move, dir = i, 1
					}
				}
			})
		}
	})
	if move >= 0 {
		d.moveRule(views, move, dir)
	}
}

// ---- advanced: base and DNS ----

func (p *Page) aggBase(c *ui.Context) {
	t := c.Theme()
	d := &p.agg
	ui.TextArea(c.Key("base"), &d.spec.Base).Font("monospace").FontSize(12).Lines(6, 18).
		Placeholder("# " + i18n.T("基础配置") + "\nmixed-port: 7890\nallow-lan: true\n...\n# " + i18n.T("DNS 配置") + "\ndns:\n  enable: true\n  ...").Label(i18n.T("基础和 DNS 配置"))
	ui.Text(c, i18n.T("合并到生成配置的顶层；proxies、proxy-groups、rules 等由生成器写出的键会被忽略。澜流自身的端口、DNS 等设置仍会覆盖这里的同名项。")).FontSize(11.5).TextColor(t.TextMuted)
}

// ---- subscription settings ----

func (p *Page) aggSettings(c *ui.Context) {
	t := c.Theme()
	d := &p.agg
	row := func(label string, control func()) {
		ui.Row(c).Gap(10).AlignItems(ui.Center).MinHeight(32).Wrap().Children(func() {
			ui.Text(c, label).FontSize(12.5).Width(96).Shrink(0)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Grow(1).Shrink(1).Children(control)
		})
	}
	row(i18n.T("名称"), func() {
		ui.TextInput(c.Key("agg-name"), &d.name).Label(i18n.T("名称")).Grow(1)
	})
	row(i18n.T("自动更新"), func() {
		opts := i18n.Options(autoLabels...)
		lbl := opts[min(d.auto, len(opts)-1)]
		if ui.Select(c.Key("agg-auto"), &lbl, opts).Label(i18n.T("自动更新")).Changed() {
			d.auto = max(indexOf(opts, lbl), 0)
		}
	})
	row("User-Agent", func() {
		ui.TextInput(c.Key("agg-ua"), &d.ua).Placeholder(i18n.T("留空使用默认 User-Agent")).Label("User-Agent").Grow(1)
	})
	row(i18n.T("下载超时（秒）"), func() {
		ui.TextInput(c.Key("agg-timeout"), &d.timeout).Placeholder(i18n.Tf("留空为 %d 秒", defaultTimeoutSec)).Width(120).FontFeatures("tnum").Label(i18n.T("下载超时（秒）"))
	})
	ui.Checkbox(c.Key("agg-via"), &d.viaProxy, i18n.T("通过代理下载订阅链接来源"))
	if d.id == "" {
		ui.Checkbox(c.Key("agg-use"), &d.use, i18n.T("导入后立即使用"))
	}
	ui.Text(c, i18n.T("到了自动更新时间，澜流会重新下载各来源并重新生成配置。")).FontSize(11.5).TextColor(t.TextMuted)
}
