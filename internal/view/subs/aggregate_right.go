package subs

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// The right column of the aggregate editor: the 预览 of the generated
// profile, as group cards (可视化) or as YAML, and 导入澜流.

// aggGraphNodes is how many nodes the visualisation lists.
const aggGraphNodes = 200

func (p *Page) aggRight(c *ui.Context, subs []model.Subscription) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	aggPanel(c, "agg-right", 1, func() {
		ui.Box(c).TextColor(t.Accent).Children(func() { k.Icon(c, "eye", 16) })
		ui.Text(c, i18n.T("预览")).FontSize(15).FontWeight(600).SingleLine()
		ui.Spacer(c)
		ui.Segmented(c.Key("agg-view"), &d.view, "YAML", i18n.T("可视化")).Label(i18n.T("预览方式"))
	}, func() {
		switch {
		case d.previewing:
			ui.Column(c).Gap(8).AlignItems(ui.Center).Padding(60, 0).Children(func() {
				ui.Spinner(c).Size(18, 18)
				ui.Text(c, i18n.T("正在下载来源并生成配置…")).FontSize(12.5).TextColor(t.TextMuted)
			})
			return
		case d.preview == nil:
			if d.previewErr != "" {
				k.Banner(c, "error", d.previewErr, nil)
			}
			ui.Column(c).Gap(10).AlignItems(ui.Center).Padding(70, 16).TextColor(t.TextMuted).Children(func() {
				k.Icon(c, "graph", 40)
				ui.Text(c, i18n.T("添加节点后显示可视化关系图")).FontSize(13).TextAlign(ui.Center)
				ui.Text(c, i18n.T("在左侧填好来源，点「生成配置」下载来源并生成，不会保存。")).FontSize(12).TextAlign(ui.Center).MaxWidth(320)
			})
			return
		}
		if d.previewStale(subs) {
			k.Banner(c, "info", i18n.T("设置改过了，预览可能已过时。"), func() {
				if ui.Button(c, i18n.T("重新生成")).Clicked() {
					p.previewAggregate(subs)
				}
			})
		}
		if d.previewErr != "" {
			k.Banner(c, "error", d.previewErr, nil)
		}
		for _, w := range d.preview.Warnings {
			k.Banner(c, "warn", w, nil)
		}
		p.aggSourceSummary(c, subs)
		if d.view == aggViewYAML {
			yml := d.preview.YAML
			ui.TextArea(c.Key("agg-yaml"), &yml).Font("monospace").FontSize(11.5).Lines(16, 400).ReadOnly(true).Label(i18n.T("生成的 YAML"))
			return
		}
		p.aggGraph(c)
	}, func() {
		save := ui.PrimaryButton(c, "").Label(i18n.T("导入澜流")).Disabled(d.saving)
		save.Children(func() {
			if d.saving {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "check", 13)
			}
			ui.Text(c, map[bool]string{true: i18n.T("保存到澜流"), false: i18n.T("导入澜流")}[d.id != ""]).SingleLine()
		})
		if save.Clicked() {
			p.saveAggregate(subs)
		}
		if d.id == "" {
			ui.Checkbox(c.Key("agg-use-r"), &d.use, i18n.T("立即使用"))
		}
	})
}

// aggSourceSummary lists the sources that failed, and the traffic the
// link sources reported.
func (p *Page) aggSourceSummary(c *ui.Context, subs []model.Subscription) {
	t := c.Theme()
	d := &p.agg
	var up, total int64
	for _, r := range d.preview.Sources {
		up += r.Upload + r.Download
		total += r.Total
		if r.Error == "" {
			continue
		}
		desc := i18n.T("来源")
		for _, s := range d.previewOf.Sources {
			if s.ID == r.ID {
				desc = aggSourceDesc(s, subs)
			}
		}
		ui.Row(c.Key("fail-"+r.ID)).Gap(6).Padding(6, 10).Radius(8).Background(t.Danger.Alpha(0.08)).Children(func() {
			ui.Text(c, desc).FontSize(12).FontWeight(600).SingleLine().Shrink(0)
			ui.Text(c, r.Error).FontSize(12).TextColor(t.Danger).Shrink(1)
		})
	}
	if total > 0 {
		ui.Text(c, i18n.Tf("订阅流量：已用 %s / %s GB", env.FmtGB(up), env.FmtGB(total))).FontSize(11.5).TextColor(t.TextMuted).FontFeatures("tnum")
	}
}

// aggGraph draws the visualisation: totals, a card per proxy group (its
// type, default choice and, opened, its rules), and the node list.
func (p *Page) aggGraph(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.agg
	pv := d.preview
	visible := 0
	for _, n := range pv.Nodes {
		if !n.Hidden {
			visible++
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		stat := func(n int, label string, col ui.Color) {
			ui.Column(c).Gap(2).Grow(1).Basis(0).AlignItems(ui.Center).Padding(10, 6).Radius(10).Background(t.Background).Children(func() {
				ui.Text(c, strconv.Itoa(n)).FontSize(20).FontWeight(600).TextColor(col).FontFeatures("tnum")
				ui.Text(c, label).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
			})
		}
		stat(visible, i18n.T("节点"), t.Accent)
		stat(len(pv.Groups), i18n.T("代理组"), t.Success)
		stat(pv.RuleSets, i18n.T("规则集"), t.Warning)
	})
	for _, g := range pv.GroupViews {
		open := d.groupOpen[g.Name]
		cb := ui.CollapsibleBase(c.Key("gv-"+g.Name), &open)
		cb.Trigger.Gap(8).AlignItems(ui.Center).Padding(8, 10).Radius(10).Background(t.Background).Border(1, t.Accent.Alpha(0.25)).Label(g.Name).Children(func() {
			ui.Box(c).TextColor(t.TextMuted).Rotate(90 * cb.Progress()).Opacity(map[bool]float32{true: 1, false: 0}[len(g.Rules) > 0]).Children(func() { k.Icon(c, "chevron", 11) })
			ui.Text(c, g.Name).FontSize(13).FontWeight(500).SingleLine().Shrink(1)
			ui.Spacer(c)
			ui.Column(c).Gap(1).AlignItems(ui.End).Shrink(0).Children(func() {
				ui.Text(c, typeLabel(g.Type)).FontSize(11.5).SingleLine()
				def := g.Default
				switch def {
				case "DIRECT":
					def = i18n.T("直连")
				case "REJECT":
					def = i18n.T("拒绝")
				}
				ui.Text(c, i18n.Tf("默认：%s", def)).FontSize(11).TextColor(t.TextMuted).SingleLine().MaxWidth(160)
			})
		})
		cb.Panel(func() {
			ui.Column(c).Gap(3).Padding(4, 12, 6, 30).Children(func() {
				for _, r := range g.Rules {
					ui.Text(c, r).FontSize(11.5).Font("monospace").TextColor(t.TextMuted).SingleLine()
				}
			})
		})
		d.groupOpen[g.Name] = open
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Margin(6, 0, 0, 2).Children(func() {
		ui.Box(c).TextColor(t.TextMuted).Children(func() { k.Icon(c, "server", 13) })
		ui.Text(c, i18n.T("节点列表")).FontSize(12.5).FontWeight(600)
		ui.Spacer(c)
		ui.Text(c, i18n.Tf("共 %d 个", visible)).FontSize(11.5).TextColor(t.TextMuted)
	})
	shown := 0
	ui.Column(c).Gap(3).Children(func() {
		for _, n := range pv.Nodes {
			if n.Hidden {
				continue
			}
			if shown == aggGraphNodes {
				ui.Text(c, i18n.Tf("还有 %d 个节点未列出", visible-aggGraphNodes)).FontSize(11.5).TextColor(t.TextMuted)
				break
			}
			shown++
			ui.Row(c.Key("gn-"+n.Name)).Gap(8).AlignItems(ui.Center).Padding(4, 10).Radius(8).Background(t.Background).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(t.Danger.Alpha(0.8)).Shrink(0)
				ui.Text(c, n.Name).FontSize(12).SingleLine().Shrink(1).Grow(1).Selectable()
				ui.Text(c, strings.ToUpper(n.Type)).FontSize(10).FontWeight(600).Padding(1, 5).Radius(4).
					Background(t.Danger.Alpha(0.14)).TextColor(t.Danger).SingleLine().Shrink(0)
			})
		}
	})
}
