package rules

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/rules/ruleform"
)

// The 规则 page on a phone (env.Env.Phone, docs/phone-ui.md): a toolbar
// of two rows, and one list that scrolls the custom rules, the
// subscription's rules and the rule sets, each rule an item of two lines
// rather than a table row the narrow screen would cut off. The list
// builds only the items in view, as the desktop's table does, so a
// subscription of many thousand rules scrolls as well. The desktop's
// layout is untouched.

// phoneKind is what an item of the phone's list shows.
type phoneKind int

const (
	phoneHead     phoneKind = iota // a section's title and note
	phoneCustom                    // custom rule list[at]
	phoneSub                       // subscription rule rules[at]
	phoneProvider                  // rule set provs[at]
	phoneMsg                       // a muted line: an empty section, a note
)

// phoneItem is one item of the phone's list.
type phoneItem struct {
	kind        phoneKind
	at          int
	title, note string
	first, last bool // the first and last row of a group: rounded corners
	key         any
}

// phoneView draws the page on a phone.
func (p *Page) phoneView(c *ui.Context, t *ui.Theme, running bool) {
	e := p.e
	ui.Column(c).Fill().Children(func() {
		p.phoneTools(c, running)
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			p.search.Focus(c)
		}
		if !running {
			e.Kit.Empty(c, "rules", i18n.T("内核未启动"), i18n.T("启动内核后这里会显示当前生效的规则及命中次数。"), func() {
				ui.PrimaryButton(c, i18n.T("启动内核")).MinHeight(env.TouchSize).OnClick(func() { e.Run(e.API.StartCore, "") })
			})
			return
		}
		rules := e.API.Rules()
		q := strings.TrimSpace(p.query)

		// The custom rules shown, and their hit counts in the core.
		all := e.API.CustomRules()
		hits := map[string]model.Rule{}
		for _, r := range rules {
			if r.Custom {
				hits[r.Type+","+r.Payload] = r
			}
		}
		var custom []model.CustomRule
		lq := p.lowerQuery()
		for _, r := range all {
			if matches(lq, r.Type, env.RuleTypeName(r.Type), r.Payload, r.Target, env.TargetName(r.Target)) {
				custom = append(custom, r)
			}
		}
		var provs []model.RuleProvider
		var subPos []int
		subTotal := 0
		if p.filter != 1 {
			subPos, subTotal = p.subRows(rules)
			provs = e.API.RuleProviders()
		}

		var items []phoneItem
		add := func(it phoneItem) { items = append(items, it) }
		group := func(kind phoneKind, n int, key func(i int) any) {
			for i := range n {
				add(phoneItem{kind: kind, at: i, first: i == 0, last: i == n-1, key: key(i)})
			}
		}
		if p.filter != 2 {
			add(phoneItem{kind: phoneHead, title: i18n.T("自定义规则"), note: i18n.T("优先于订阅规则，修改后自动重新加载配置"), key: "h:custom"})
			if len(custom) == 0 {
				msg := i18n.T("还没有自定义规则。可点击「添加规则」，或在连接页选中一条连接后添加。")
				if q != "" {
					msg = i18n.T("没有匹配的自定义规则")
				}
				add(phoneItem{kind: phoneMsg, title: msg, key: "m:custom"})
			}
			group(phoneCustom, len(custom), func(i int) any { return "c:" + custom[i].ID })
		}
		if p.filter != 1 {
			subName := ""
			for _, s := range e.API.Subscriptions() {
				if s.Current {
					subName = s.Name
				}
			}
			note := i18n.Tf("共 %d 条", subTotal)
			if subName != "" {
				note = subName + " · " + note
			}
			add(phoneItem{kind: phoneHead, title: i18n.T("订阅规则"), note: note, key: "h:sub"})
			if len(subPos) == 0 {
				msg := i18n.T("订阅中没有规则")
				if q != "" {
					msg = i18n.Tf("没有匹配“%s”的规则", q)
				}
				add(phoneItem{kind: phoneMsg, title: msg, key: "m:sub"})
			}
			group(phoneSub, len(subPos), func(i int) any { return rules[subPos[i]].Index })
			add(phoneItem{kind: phoneMsg, title: i18n.T("命中次数自本次内核启动起统计。「停用」只在本次运行有效，重新加载配置后恢复；要长期修改，请使用自定义规则或覆写。"), key: "m:sub-note"})
			if len(provs) > 0 {
				add(phoneItem{kind: phoneHead, title: i18n.T("规则集"), note: i18n.T("订阅引用的 rule-providers，可单独更新"), key: "h:providers"})
				group(phoneProvider, len(provs), func(i int) any { return "p:" + provs[i].Name })
			}
		}

		p.phoneList.Key = func(i int) any {
			if i < len(items) {
				return items[i].key
			}
			return i
		}
		ui.List(c.Key("phone-rules"), &p.phoneList, len(items), func(i int) {
			if i >= len(items) {
				return
			}
			it := items[i]
			switch it.kind {
			case phoneHead:
				ui.Column(c).Gap(2).Padding(18, 20, 6, 20).Children(func() {
					ui.Text(c, it.title).FontSize(13).FontWeight(600)
					if it.note != "" {
						ui.Text(c, it.note).FontSize(11).TextColor(t.TextMuted)
					}
				})
			case phoneMsg:
				ui.Text(c, it.title).FontSize(12).TextColor(t.TextMuted).Padding(6, 20)
			case phoneCustom:
				p.phoneGroupRow(c, t, it, func() { p.phoneCustomRow(c, t, custom, it.at, all, hits, q) })
			case phoneSub:
				p.phoneGroupRow(c, t, it, func() { p.phoneSubRow(c, t, rules[subPos[it.at]], q) })
			case phoneProvider:
				p.phoneGroupRow(c, t, it, func() { p.providerRows(c, t, provs[it.at:it.at+1]) })
			}
		}).Grow(1).Basis(0).FillWidth().Label(i18n.T("规则"))
	})
}

// phoneGroupRow draws a row of a rounded group: the group's box is drawn
// row by row, so that the list can build only the rows in view.
func (p *Page) phoneGroupRow(c *ui.Context, t *ui.Theme, it phoneItem, row func()) {
	ui.Column(c).Padding(0, 16).Children(func() {
		box := ui.Column(c).Background(t.Surface).BorderColor(t.Border)
		var top, bottom float32
		if it.first {
			top = 10
		}
		if it.last {
			bottom = 10
		}
		box.Radius(top, top, bottom, bottom)
		// Sides on every row, the top on the first, a hairline under
		// each row (the group's bottom on the last).
		var tw float32
		if it.first {
			tw = 1
		}
		box.BorderWidth(tw, 1, 1, 1)
		box.Children(row)
	})
}

// phoneTools are the toolbar on a phone: the search with 测试 and 添加,
// then the source filter across the width.
func (p *Page) phoneTools(c *ui.Context, running bool) {
	e := p.e
	ui.Column(c).Gap(8).Padding(10, 16, 4, 16).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索规则")).Label(i18n.T("搜索规则")).
				Grow(1).Basis(0).MinWidth(0).MinHeight(36).Bind(&p.search)
			ui.Button(c, i18n.T("测试…")).Disabled(!running).MinHeight(env.TouchSize).Shrink(0).OnClick(func() {
				p.testOpen = true
			})
			ui.PrimaryButton(c, "").Label(i18n.T("添加规则")).Size(env.TouchSize, env.TouchSize).Shrink(0).Children(func() {
				e.Kit.Icon(c, "plus", 16)
			}).OnClick(func() {
				p.form.Open(model.CustomRule{Target: e.API.Proxies().MainGroup})
			})
		})
		ui.Segmented(c.Key("filter"), &p.filter, i18n.T("全部"), i18n.T("自定义"), i18n.T("订阅")).Label(i18n.T("规则来源")).FillWidth()
	})
}

// phoneCustomRow is a custom rule in two lines: its type and content,
// then where it goes and its hits; ⋯ opens its menu.
func (p *Page) phoneCustomRow(c *ui.Context, t *ui.Theme, list []model.CustomRule, i int, all []model.CustomRule, hits map[string]model.Rule, q string) {
	e := p.e
	rule := list[i]
	first := i == 0 && len(all) > 0 && all[0].ID == rule.ID
	h, hasHit := hits[rule.Type+","+rule.Payload]
	ui.Row(c.Key(rule.ID)).Gap(8).AlignItems(ui.Center).MinHeight(env.TouchSize+12).Padding(6, 4, 6, 14).Children(func() {
		ui.Column(c).Gap(4).Grow(1).Basis(0).MinWidth(0).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				e.Kit.Tag(c, env.RuleTypeName(rule.Type), "gray")
				env.Highlight(c, rule.Payload, q, t.Text).Font("monospace").Grow(1).Basis(0).MinWidth(0)
			})
			hitText := i18n.T("命中 0")
			if hasHit {
				hitText = i18n.Tn(int(h.HitCount), "命中 %s", env.FmtInt(h.HitCount))
				if h.HitCount > 0 {
					hitText += " · " + env.FmtAgo(h.HitAt)
				}
			}
			ui.Text(c, "→ "+env.TargetName(rule.Target)+"  ·  "+hitText).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
		})
		more := ui.ButtonBase(c).Label(i18n.T("规则操作")).Size(env.TouchSize, env.TouchSize).Radius(10).
			AlignItems(ui.Center).Justify(ui.Center).Shrink(0).TextColor(t.TextMuted)
		if more.Pressed() {
			more.Background(t.SurfacePressed)
		}
		more.Children(func() { e.Kit.Icon(c, "more", 18) })
		more.PopoverMenuButton(func(m *ui.Menu) {
			if m.Item(i18n.T("编辑…")).Chosen() {
				p.form.Open(rule)
			}
			if m.Item(i18n.T("上移")).Disabled(first).Chosen() {
				e.Run(func() error { return e.API.MoveCustomRule(rule.ID, -1) }, "")
			}
			m.Separator()
			if m.Item(i18n.T("删除…")).Danger().Chosen() {
				e.Confirm(i18n.Tf("删除规则「%s」？", ruleform.Describe(rule)), i18n.T("删除后会重新生成配置并让内核重新加载。"), i18n.T("删除"), true, func() {
					e.Run(func() error { return e.API.DeleteCustomRule(rule.ID) }, i18n.T("规则已删除"))
				})
			}
		}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd})
	})
}

// phoneSubRow is a subscription rule in two lines: its content (and the
// size of a rule set), then its number, type, policy and hits; 停用 or
// 恢复 on the right.
func (p *Page) phoneSubRow(c *ui.Context, t *ui.Theme, r model.Rule, q string) {
	e := p.e
	muted := t.TextMuted
	row := ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(env.TouchSize+12).Padding(6, 8, 6, 14)
	row.Children(func() {
		col := ui.Column(c).Gap(4).Grow(1).Basis(0).MinWidth(0)
		if r.Disabled {
			col.Opacity(0.5)
		}
		col.Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				if r.Payload == "" {
					env.Highlight(c, r.Type, q, t.Text).Font("monospace")
				} else {
					env.Highlight(c, r.Payload, q, t.Text).Font("monospace").Shrink(1).MinWidth(0)
				}
				if r.Size > 0 {
					ui.Text(c, i18n.Tn(r.Size, "%s 条", env.FmtInt(int64(r.Size)))).FontSize(11).TextColor(muted).FontFeatures("tnum").SingleLine().Shrink(0)
				}
			})
			line := fmt.Sprintf("#%d", r.Index+1)
			if r.Payload != "" {
				line += " · " + r.Type
			}
			line += " → " + env.TargetName(r.Proxy) + "  ·  " + i18n.Tn(int(r.HitCount), "命中 %s", env.FmtInt(r.HitCount))
			if r.HitCount > 0 {
				line += " · " + env.FmtAgo(r.HitAt)
			}
			env.Highlight(c, line, q, muted).FontSize(12).FontFeatures("tnum")
		})
		if r.Disabled {
			ui.Box(c).Shrink(0).Children(func() { e.Kit.Tag(c, i18n.T("已临时停用"), "orange") })
		}
		label := i18n.T("停用")
		if r.Disabled {
			label = i18n.T("恢复")
		}
		idx, dis := r.Index, !r.Disabled
		ui.Button(c, label).FontSize(13).MinHeight(env.TouchSize).Shrink(0).OnClick(func() {
			e.Run(func() error { return e.API.SetRuleDisabled(idx, dis) }, "")
		})
	})
}
