package subs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

// The dialogs of the aggregate editor.

// aggLibRows is how many rule sets the rule library lists at once.
const aggLibRows = 60

// aggDialogs is the state of the editor's dialogs.
type aggDialogs struct {
	// A source opened large (展开编辑).
	srcOpen bool
	src     int

	// 自动处理: keep / drop patterns, one per line.
	auto    bool
	autoInc string
	autoExc string

	// 批量编辑: the rename rules.
	batch   bool
	renames []model.RenameRule

	// A group being edited (index into the spec's groups, -1 = a new custom one).
	group    bool
	gIdx     int
	gCustom  bool
	gName    string
	gType    string
	gFilter  string
	gRules   []string
	gExt     bool // a catalog group: with its extended rule sets
	gRuleAdd string
	gErr     string

	// 规则库.
	lib       bool
	libQ      string
	libTarget string
	libMsg    string

	// 批量导入 of custom rules.
	bulk     bool
	bulkText string
	bulkMsg  string

	// 保存为模板 and 我的模板.
	saveTpl   bool
	tplName   string
	tplErr    string
	tplSaving bool
	myTpl     bool
}

func (g *aggDialogs) openAuto(spec model.AggregateSpec) {
	g.auto, g.autoInc, g.autoExc = true, spec.Include, spec.Exclude
}

func (g *aggDialogs) openBatch(spec model.AggregateSpec) {
	g.batch = true
	g.renames = append([]model.RenameRule(nil), spec.Renames...)
	if len(g.renames) == 0 {
		g.renames = []model.RenameRule{{}}
	}
}

// openGroup opens the group editor on group i of spec, or on a new
// custom group when i < 0.
func (g *aggDialogs) openGroup(spec model.AggregateSpec, i int) {
	g.group, g.gIdx, g.gErr, g.gRuleAdd = true, i, "", ""
	if i < 0 {
		g.gCustom, g.gName, g.gType, g.gFilter, g.gRules = true, "", model.AggTypeSelect, "", nil
		return
	}
	gr := spec.Groups[i]
	g.gCustom, g.gName, g.gType, g.gFilter, g.gExt = gr.Custom, profile.AggGroupName(gr), profile.AggGroupType(gr), gr.Filter, gr.Extended
	g.gRules = append([]string(nil), gr.Rules...)
}

func (p *Page) aggDialogs(c *ui.Context, subs []model.Subscription) {
	p.aggSourceDialog(c, subs)
	p.aggAutoDialog(c)
	p.aggBatchDialog(c)
	p.aggGroupDialog(c)
	p.aggLibDialog(c)
	p.aggBulkDialog(c)
	p.aggSaveTplDialog(c)
	p.aggMyTplDialog(c)
}

// dialogButtons draws a dialog's right-aligned buttons.
func dialogButtons(c *ui.Context, cancel string, ok string, onCancel, onOK func()) {
	ui.Row(c).Gap(8).Justify(ui.End).Margin(6, 0, 0, 0).Children(func() {
		if cancel != "" && ui.Button(c, cancel).Clicked() {
			onCancel()
		}
		if ok != "" && ui.PrimaryButton(c, ok).Clicked() {
			onOK()
		}
	})
}

func dialogTitle(c *ui.Context, title, note string) {
	t := c.Theme()
	ui.Text(c, title).FontSize(15).FontWeight(600)
	if note != "" {
		ui.Text(c, note).FontSize(12).TextColor(t.TextMuted)
	}
}

func (p *Page) aggSourceDialog(c *ui.Context, subs []model.Subscription) {
	d := &p.agg
	g := &d.dlg
	if g.src >= len(d.sources) {
		g.srcOpen = false
	}
	if !g.srcOpen {
		return
	}
	f := &d.sources[g.src]
	var subLabels []string
	for _, l := range subChoices(subs, d.id) {
		subLabels = append(subLabels, l.label)
	}
	ui.Modal(c, &g.srcOpen, func() {
		ui.Column(c.Key(fmt.Sprintf("srcdlg-%d", f.uid))).Width(env.DialogWidth(c, 560)).Gap(10).Children(func() {
			dialogTitle(c, aggKindLabel(f.kind)+" #"+fmt.Sprint(g.src+1), "")
			field(c, i18n.T("显示名称（可选）"), func() {
				ui.TextInput(c.Key("name"), &f.name).Placeholder(aggKindLabel(f.kind)).Label(i18n.T("显示名称"))
			})
			p.aggSourceBody(c, f, subLabels, 10, 24)
			field(c, i18n.T("名称前缀"), func() {
				ui.TextInput(c.Key("prefix"), &f.prefix).Placeholder(i18n.T("可选，例如「机场A · 」")).Label(i18n.T("名称前缀"))
			})
			ui.Checkbox(c.Key("enabled"), &f.enabled, i18n.T("启用这个来源"))
			dialogButtons(c, "", i18n.T("完成"), nil, func() { g.srcOpen = false })
		})
	})
}

func (p *Page) aggAutoDialog(c *ui.Context) {
	t := c.Theme()
	d := &p.agg
	g := &d.dlg
	ui.Modal(c, &g.auto, func() {
		ui.Column(c).Width(env.DialogWidth(c, 480)).Gap(10).Children(func() {
			dialogTitle(c, i18n.T("自动处理"), i18n.T("按节点的原始名称筛选，每行一条正则表达式。"))
			field(c, i18n.T("保留正则（匹配任意一条即保留，留空保留全部）"), func() {
				ui.TextArea(c.Key("inc"), &g.autoInc).Font("monospace").FontSize(12).Lines(3, 8).Placeholder(i18n.T("日本|新加坡")).Label(i18n.T("保留正则"))
			})
			field(c, i18n.T("排除正则（命中任意一条即排除）"), func() {
				ui.TextArea(c.Key("exc"), &g.autoExc).Font("monospace").FontSize(12).Lines(3, 8).Placeholder(i18n.T("剩余流量|套餐到期|注意事项")).Label(i18n.T("排除正则"))
			})
			if ui.Button(c, i18n.T("恢复默认排除")).Clicked() {
				g.autoExc = profile.DefaultAggregateExclude
			}
			ui.Text(c, i18n.T("默认排除剩余流量、到期时间等信息条目。")).FontSize(11.5).TextColor(t.TextMuted)
			dialogButtons(c, i18n.T("取消"), i18n.T("保存"), func() { g.auto = false }, func() {
				probe := d.spec.Clone()
				probe.Version = model.AggregateSpecVersion
				probe.Include, probe.Exclude = strings.TrimSpace(g.autoInc), strings.TrimSpace(g.autoExc)
				probe.Sources = []model.AggregateSource{{ID: "x", Kind: model.AggSourceLinks, Links: "ss://x"}}
				if err := profile.ValidateAggregateSpec(probe, i18n.Tf); err != nil {
					p.e.Toast(err.Error(), true)
					return
				}
				d.spec.Include, d.spec.Exclude = probe.Include, probe.Exclude
				g.auto = false
			})
		})
	})
}

func (p *Page) aggBatchDialog(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	g := &d.dlg
	ui.Modal(c, &g.batch, func() {
		ui.Column(c).Width(env.DialogWidth(c, 560)).Gap(10).Children(func() {
			dialogTitle(c, i18n.T("批量编辑"), i18n.T("按顺序把节点名称中匹配的部分替换掉，替换内容可用 $1 引用分组；之后再加来源的名称前缀，重名的节点自动编号。"))
			remove := -1
			for i := range g.renames {
				ui.Row(c.Key(fmt.Sprintf("rn-%d", i))).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c.Key("pat"), &g.renames[i].Pattern).Placeholder(`^\[(.+?)\]\s*`).Font("monospace").FontSize(12).Grow(1).Basis(0).Label(i18n.T("查找"))
					ui.Text(c, "→").TextColor(t.TextMuted)
					ui.TextInput(c.Key("rep"), &g.renames[i].Replace).Placeholder("$1 ").Font("monospace").FontSize(12).Grow(1).Basis(0).Label(i18n.T("替换为"))
					if p.aggIconButton(c, "close", i18n.T("删除"), false) {
						remove = i
					}
				})
			}
			if remove >= 0 {
				g.renames = append(g.renames[:remove:remove], g.renames[remove+1:]...)
			}
			add := ui.Button(c, "").Label(i18n.T("添加替换")).Disabled(len(g.renames) >= profile.AggMaxRenames)
			add.Children(func() {
				k.Icon(c, "plus", 12)
				ui.Text(c, i18n.T("添加替换"))
			})
			if add.Clicked() {
				g.renames = append(g.renames, model.RenameRule{})
			}
			// A preview on the current nodes' names.
			if d.preview != nil && len(d.preview.Nodes) > 0 {
				ui.Text(c, i18n.T("预览（基于当前节点名称）")).FontSize(12).TextColor(t.TextMuted).Margin(4, 0, 0, 0)
				ui.Column(c).Gap(2).Children(func() {
					for i, n := range d.preview.Nodes {
						if i == 6 {
							break
						}
						to := n.Name
						for _, r := range g.renames {
							if re, err := regexp.Compile(r.Pattern); err == nil && r.Pattern != "" {
								to = re.ReplaceAllString(to, r.Replace)
							}
						}
						ui.Row(c).Gap(6).Children(func() {
							ui.Text(c, n.Name).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Shrink(1)
							ui.Text(c, "→").FontSize(11.5).TextColor(t.TextMuted)
							ui.Text(c, strings.Join(strings.Fields(to), " ")).FontSize(11.5).SingleLine().Shrink(1)
						})
					}
				})
			}
			dialogButtons(c, i18n.T("取消"), i18n.T("完成"), func() { g.batch = false }, func() {
				var out []model.RenameRule
				for i, r := range g.renames {
					if strings.TrimSpace(r.Pattern) == "" && r.Replace == "" {
						continue
					}
					if strings.TrimSpace(r.Pattern) == "" {
						p.e.Toast(i18n.Tf("第 %d 条重命名规则缺少要匹配的内容", i+1), true)
						return
					}
					if _, err := regexp.Compile(r.Pattern); err != nil {
						p.e.Toast(i18n.Tf("第 %d 条重命名规则的正则表达式无效：%v", i+1, err), true)
						return
					}
					out = append(out, r)
				}
				d.spec.Renames = out
				g.batch = false
			})
		})
	})
}

func (p *Page) aggGroupDialog(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	g := &d.dlg
	types := profile.AggregateTypes()
	var typeLabels []string
	for _, ty := range types {
		typeLabels = append(typeLabels, typeLabel(ty))
	}
	ui.Modal(c, &g.group, func() {
		ui.Column(c).Width(env.DialogWidth(c, 480)).Gap(10).Children(func() {
			title := i18n.T("编辑分组")
			if g.gIdx < 0 {
				title = i18n.T("添加自定义分组")
			}
			dialogTitle(c, title, "")
			field(c, i18n.T("名称（代理组在配置中的名称）"), func() {
				ui.TextInput(c.Key("gname"), &g.gName).Placeholder("💼 " + i18n.T("工作")).Label(i18n.T("分组名称")).AutoFocus()
			})
			field(c, i18n.T("类型"), func() {
				lbl := typeLabel(g.gType)
				if ui.Select(c.Key("gtype"), &lbl, typeLabels).Label(i18n.T("类型")).Changed() {
					g.gType = types[max(indexOf(typeLabels, lbl), 0)]
				}
			})
			ui.Text(c, i18n.T("手动选择：默认跟随节点选择；自动测速 / 故障转移 / 负载均衡：在匹配的节点中自动挑选；拦截、直连：默认拒绝或直连，可再切换。")).FontSize(11.5).TextColor(t.TextMuted)
			var m *profile.AggregateModule
			if !g.gCustom && g.gIdx >= 0 && g.gIdx < len(d.spec.Groups) {
				m = profile.AggModule(d.spec.Groups[g.gIdx].ID)
			}
			if m != nil && len(m.Extra) > 0 {
				ui.Checkbox(c.Key("gext"), &g.gExt, i18n.Tf("包含扩展规则集（另 %d 个）", len(m.Extra)))
				ui.Text(c, strings.Join(m.Extra, "  ")).FontSize(11).Font("monospace").TextColor(t.TextMuted)
			}
			if g.gCustom {
				field(c, i18n.T("节点筛选（正则，可选）"), func() {
					ui.TextInput(c.Key("gfilter"), &g.gFilter).Placeholder(i18n.T("香港|HK")).Font("monospace").FontSize(12).Label(i18n.T("节点筛选"))
				})
				field(c, i18n.T("规则集"), func() {
					ui.Row(c).Gap(4).Wrap().Children(func() {
						for i, r := range g.gRules {
							ui.Row(c.Key("gr-"+r)).Gap(2).AlignItems(ui.Center).Padding(1, 2, 1, 8).Radius(999).Background(t.Accent.Alpha(0.12)).Children(func() {
								ui.Text(c, r).FontSize(11.5).Font("monospace")
								if p.aggIconButton(c, "close", i18n.T("移除"), false) {
									g.gRules = append(g.gRules[:i:i], g.gRules[i+1:]...)
								}
							})
						}
					})
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						in := ui.TextInput(c.Key("gadd"), &g.gRuleAdd).Placeholder("geosite:netflix / geoip:us").Font("monospace").FontSize(12).Label(i18n.T("添加规则集")).Grow(1)
						b := ui.Button(c, "").Label(i18n.T("添加"))
						b.Children(func() { k.Icon(c, "plus", 12) })
						if in.Submitted() || b.Clicked() {
							ref := strings.ToLower(strings.TrimSpace(g.gRuleAdd))
							if ref != "" && !strings.Contains(ref, ":") {
								ref = "geosite:" + ref
							}
							if ref != "" && indexOf(g.gRules, ref) < 0 {
								g.gRules = append(g.gRules, ref)
							}
							g.gRuleAdd = ""
						}
					})
				})
			}
			if g.gErr != "" {
				ui.Text(c, g.gErr).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
			}
			dialogButtons(c, i18n.T("取消"), i18n.T("保存"), func() { g.group = false }, func() { g.gErr = p.saveGroup() })
		})
	})
}

// saveGroup applies the group dialog; it returns what is wrong instead.
func (p *Page) saveGroup() string {
	d := &p.agg
	g := &d.dlg
	name := strings.TrimSpace(g.gName)
	if name == "" {
		return i18n.T("请填写名称。")
	}
	groups := append([]model.AggregateGroup(nil), d.spec.Groups...)
	var gr model.AggregateGroup
	if g.gIdx < 0 {
		gr = model.AggregateGroup{ID: fmt.Sprintf("c-%d", d.nextUID), Custom: true}
		d.nextUID++
	} else {
		gr = groups[g.gIdx]
	}
	if gr.Custom {
		gr.Name, gr.Type, gr.Filter, gr.Rules = name, g.gType, strings.TrimSpace(g.gFilter), append([]string(nil), g.gRules...)
	} else {
		m := profile.AggModule(gr.ID)
		gr.Name, gr.Type, gr.Extended = name, g.gType, g.gExt && len(m.Extra) > 0
		if name == m.Name() {
			gr.Name = ""
		}
		if g.gType == m.Type {
			gr.Type = ""
		}
	}
	if g.gIdx < 0 {
		groups = append(groups, gr)
	} else {
		groups[g.gIdx] = gr
	}
	probe := d.spec.Clone()
	probe.Version = model.AggregateSpecVersion
	probe.Groups = groups
	probe.Sources = []model.AggregateSource{{ID: "x", Kind: model.AggSourceLinks, Links: "ss://x"}}
	if err := profile.ValidateAggregateSpec(probe, i18n.Tf); err != nil {
		return err.Error()
	}
	d.spec.Groups = groups
	d.spec.Template = ""
	g.group = false
	return ""
}

func (p *Page) aggLibDialog(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	g := &d.dlg
	labels, ids := d.targetChoices()
	if g.libTarget == "" || indexOf(labels, g.libTarget) < 0 {
		g.libTarget = labels[0]
	}
	ui.Modal(c, &g.lib, func() {
		ui.Column(c).Width(env.DialogWidth(c, 560)).Gap(10).Children(func() {
			dialogTitle(c, i18n.T("规则库"), i18n.Tf("共 %d 个规则集：GeoSite 分类与 GeoIP 国家 / 服务。选好目标代理组，点规则集即可添加。", profile.RuleSetCount()))
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.SearchField(c.Key("libq"), &g.libQ).Placeholder(i18n.T("搜索：netflix、google、steam、telegram…")).Label(i18n.T("搜索规则集")).Grow(1).AutoFocus()
				ui.Select(c.Key("libt"), &g.libTarget, labels).Label(i18n.T("目标代理组"))
			})
			if g.libMsg != "" {
				ui.Text(c, g.libMsg).FontSize(12).TextColor(t.Success).Role(ui.RoleStatus)
			}
			have := map[string]bool{}
			for _, r := range d.spec.Rules {
				have[profile.AggregateRuleKey(r.Type, r.Value)] = true
			}
			ui.Scroll(c.Key("libscroll")).MaxHeight(360).Children(func() {
				ui.Column(c).Gap(3).Padding(0, 6, 0, 0).Children(func() {
					sets := profile.SearchRuleSets(g.libQ, aggLibRows)
					if len(sets) == 0 {
						ui.Text(c, i18n.T("没有匹配的规则集")).FontSize(12).TextColor(t.TextMuted).Padding(10, 0)
					}
					for _, s := range sets {
						added := have[s.Ref]
						b := ui.Button(c.Key("lib-"+s.Ref), "").Label(s.Ref).FillWidth().Justify(ui.Start).Padding(5, 10).Background(t.Background).Border(0, ui.Color{}).Disabled(added)
						b.Children(func() {
							kind := "GeoSite"
							if s.IP {
								kind = "GeoIP"
							}
							ui.Text(c, kind).FontSize(10.5).FontWeight(600).TextColor(t.Accent).Width(48).Shrink(0)
							ui.Text(c, s.Name).FontSize(12.5).Font("monospace").SingleLine().Grow(1).Shrink(1)
							if added {
								ui.Text(c, i18n.T("已添加")).FontSize(11.5).TextColor(t.TextMuted)
							} else {
								k.Icon(c, "plus", 12)
							}
						})
						if b.Clicked() {
							typ := model.AggRuleGeoSite
							if s.IP {
								typ = model.AggRuleGeoIP
							}
							r := model.AggregateRule{Type: typ, Value: s.Name, Target: ids[indexOf(labels, g.libTarget)]}
							if msg := d.addRule(r); msg != "" {
								g.libMsg = msg
							} else {
								g.libMsg = i18n.Tf("已添加 %s → %s", s.Ref, g.libTarget)
							}
						}
					}
				})
			})
			dialogButtons(c, "", i18n.T("完成"), nil, func() { g.lib = false })
		})
	})
}

func (p *Page) aggBulkDialog(c *ui.Context) {
	t := c.Theme()
	d := &p.agg
	g := &d.dlg
	labels, ids := d.targetChoices()
	ui.Modal(c, &g.bulk, func() {
		ui.Column(c).Width(env.DialogWidth(c, 520)).Gap(10).Children(func() {
			dialogTitle(c, i18n.T("批量导入规则"), i18n.T("每行一条：类型,值[,目标代理组]。目标可写代理组名称、DIRECT 或 REJECT，省略时用节点选择。"))
			ui.TextArea(c.Key("bulk"), &g.bulkText).Font("monospace").FontSize(12).Lines(8, 18).
				Placeholder(i18n.T("DOMAIN-SUFFIX,example.com\nDOMAIN-KEYWORD,openai,🤖 AI 服务\nIP-CIDR,10.0.0.0/8,DIRECT\nGEOSITE,netflix")).Label(i18n.T("规则"))
			if g.bulkMsg != "" {
				ui.Text(c, g.bulkMsg).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
			}
			dialogButtons(c, i18n.T("取消"), i18n.T("导入"), func() { g.bulk = false }, func() {
				added := 0
				var bad []string
				for i, line := range strings.Split(g.bulkText, "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					parts := strings.Split(line, ",")
					if len(parts) < 2 {
						bad = append(bad, fmt.Sprint(i+1))
						continue
					}
					r := model.AggregateRule{Type: strings.ToUpper(strings.TrimSpace(parts[0])), Value: strings.TrimSpace(parts[1]), Target: profile.AggModSelect}
					for _, extra := range parts[2:] {
						x := strings.TrimSpace(extra)
						switch {
						case x == "no-resolve":
							r.NoResolve = true
						case indexOf(labels, x) >= 0:
							r.Target = ids[indexOf(labels, x)]
						case x != "":
							for _, gr := range d.spec.Groups {
								if profile.AggGroupName(gr) == x {
									r.Target = gr.ID
								}
							}
						}
					}
					if msg := d.addRule(r); msg != "" {
						bad = append(bad, fmt.Sprint(i+1))
						continue
					}
					added++
				}
				if len(bad) > 0 {
					g.bulkMsg = i18n.Tf("已导入 %d 条；第 %s 行无法导入（格式不对、重复或目标不存在）。", added, strings.Join(bad, i18n.T("、")))
					return
				}
				g.bulk = false
				p.e.Toast(i18n.Tf("已导入 %d 条规则", added), false)
			})
		})
	})
}

func (p *Page) aggSaveTplDialog(c *ui.Context) {
	t := c.Theme()
	d := &p.agg
	g := &d.dlg
	md := ui.Modal(c, &g.saveTpl, func() {
		ui.Column(c).Width(env.DialogWidth(c, 420)).Gap(10).Children(func() {
			dialogTitle(c, i18n.T("保存为模板"), i18n.T("保存代理组、规则、节点处理与基础配置，不含来源。之后可在快捷模式「我的模板」里套用。"))
			field(c, i18n.T("模板名称"), func() {
				ui.TextInput(c.Key("tplname"), &g.tplName).Label(i18n.T("模板名称")).AutoFocus().ReadOnly(g.tplSaving)
			})
			if g.tplErr != "" {
				ui.Text(c, g.tplErr).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
			}
			dialogButtons(c, i18n.T("取消"), i18n.T("保存"), func() { g.saveTpl = false }, func() {
				if g.tplSaving {
					return
				}
				spec := d.fullSpec(nil)
				g.tplSaving, g.tplErr = true, ""
				session := d.session
				var saved model.AggregateTemplate
				name := g.tplName
				p.e.Then(func() (err error) {
					saved, err = p.e.API.SaveAggregateTemplate(model.AggregateTemplate{Name: name, Spec: spec})
					return err
				}, func(err error) {
					if p.agg.session != session {
						return
					}
					p.agg.dlg.tplSaving = false
					if err != nil {
						p.agg.dlg.tplErr = err.Error()
						return
					}
					p.agg.dlg.saveTpl = false
					p.agg.spec.Template = "my:" + saved.ID
					p.agg.templates = append(p.agg.templates, saved)
					p.e.Toast(i18n.Tf("已保存模板「%s」", saved.Name), false)
				})
			})
		})
	})
	if md.Dismissed() && g.tplSaving {
		g.saveTpl = true
	}
}

func (p *Page) aggMyTplDialog(c *ui.Context) {
	t := c.Theme()
	d := &p.agg
	g := &d.dlg
	ui.Modal(c, &g.myTpl, func() {
		ui.Column(c).Width(env.DialogWidth(c, 480)).Gap(10).Children(func() {
			dialogTitle(c, i18n.T("我的模板"), i18n.T("套用模板会替换代理组、规则、节点处理与基础配置，来源保持不变。"))
			if d.tplErr != "" {
				ui.Text(c, d.tplErr).FontSize(12).TextColor(t.Danger)
			}
			if len(d.templates) == 0 {
				ui.Text(c, i18n.T("还没有模板。在编辑器底部点「保存为模板」即可保存当前设置。")).FontSize(12.5).TextColor(t.TextMuted).Padding(12, 0)
			}
			del := -1
			for i, tp := range d.templates {
				groups := len(profile.UpgradeAggregateSpec(tp.Spec).Groups)
				ui.Row(c.Key("mt-"+tp.ID)).Gap(8).AlignItems(ui.Center).Padding(8, 10).Radius(10).Background(t.Background).Border(1, t.Border.Alpha(0.6)).Children(func() {
					ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
						ui.Text(c, tp.Name).FontSize(13.5).FontWeight(600).SingleLine()
						ui.Text(c, i18n.Tf("%d 代理组 · %d 条自定义规则 · %s", groups, len(tp.Spec.Rules), tp.Created.Format("2006-01-02"))).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
					})
					if ui.Button(c, i18n.T("套用")).Clicked() {
						d.applyMyTemplate(tp)
						g.myTpl = false
						p.e.Toast(i18n.Tf("已套用模板「%s」", tp.Name), false)
					}
					if p.aggIconButton(c, "trash", i18n.T("删除模板"), false) {
						del = i
					}
				})
			}
			if del >= 0 {
				tp := d.templates[del]
				p.e.Confirm(i18n.T("删除模板"), i18n.Tf("删除模板「%s」？已用它生成的订阅不受影响。", tp.Name), i18n.T("删除"), true, func() {
					session := d.session
					p.e.Then(func() error { return p.e.API.DeleteAggregateTemplate(tp.ID) }, func(err error) {
						if p.agg.session != session {
							return
						}
						if err != nil {
							p.e.Toast(err.Error(), true)
							return
						}
						p.loadTemplates()
					})
				})
			}
			dialogButtons(c, "", i18n.T("关闭"), nil, func() { g.myTpl = false })
		})
	})
}
