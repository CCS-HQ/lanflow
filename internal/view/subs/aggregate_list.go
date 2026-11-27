package subs

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

// The 聚合订阅 page (env.Aggregate, the sidebar's last card): the
// aggregate subscriptions built and maintained here, each opening the
// editor, with 新建 for another. It is a Page of its own (NewAggregates)
// sharing the 订阅 page's code.

// NewAggregates returns the 聚合订阅 page.
func NewAggregates(e *env.Env) *Page {
	p := New(e)
	p.aggOnly = true
	return p
}

// aggSummary is what a card shows of a subscription's spec.
type aggSummary struct {
	sources int    // enabled sources
	mode    string // the template's label, or 高级配置
}

// aggregatesView draws the 聚合订阅 page: the editor while it is open,
// else the list.
func (p *Page) aggregatesView(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	all := p.e.API.Subscriptions()
	switch arg := p.e.Arg(env.Aggregate); arg {
	case "":
	case "new":
		p.openAggregate(nil)
	default:
		for _, s := range all {
			if s.ID == arg && s.Source == model.SourceAggregate {
				p.openAggregate(&s)
			}
		}
	}
	if p.agg.open {
		p.aggregateView(c)
		p.dialogs(c)
		return
	}
	var subs []model.Subscription
	for _, s := range all {
		if s.Source == model.SourceAggregate {
			subs = append(subs, s)
		}
	}
	p.loadSummaries(subs)
	if p.e.Phone {
		p.phoneAggregates(c, subs)
		p.dialogs(c)
		return
	}

	k.Header(c, i18n.T("聚合订阅"), i18n.T("多个来源合成一个订阅，在本机编辑维护"), func() {
		if len(subs) > 1 {
			if ui.Button(c, i18n.T("全部更新")).Disabled(p.updatingAny(subs)).Clicked() {
				for _, s := range subs {
					p.update(s)
				}
			}
		}
		b := ui.PrimaryButton(c, "").Label(i18n.T("新建聚合订阅"))
		b.Children(func() {
			k.Icon(c, "plus", 13)
			ui.Text(c, i18n.T("新建聚合订阅")).SingleLine()
		})
		if b.Clicked() {
			p.openAggregate(nil)
		}
	})
	if len(subs) == 0 {
		k.Empty(c, "layers", i18n.T("还没有聚合订阅"), i18n.T("从订阅链接、节点链接或 YAML 生成自己的订阅：节点筛选、代理组和分流规则都可以在这里编辑，之后随时回来修改、更新。"), func() {
			if ui.PrimaryButton(c, i18n.T("新建聚合订阅")).Clicked() {
				p.openAggregate(nil)
			}
		})
		p.dialogs(c)
		return
	}
	p.mu.Lock()
	switching := p.switching
	p.mu.Unlock()
	ui.Scroll(c.Key("agg-list")).Grow(1).Shrink(1).Basis(0).Children(func() {
		ui.Column(c).Gap(10).Padding(0, 24, 16, 24).Children(func() {
			for _, s := range subs {
				p.aggCard(c, s, switching)
			}
			ui.Text(c, i18n.T("聚合订阅也会出现在「订阅」页，可以和其他订阅一样切换使用。")).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center).Margin(4, 0, 0, 0)
		})
	})
	p.dialogs(c)
}

// aggCard is one aggregate subscription: its name and state, and the
// actions to edit, update, use and manage it.
func (p *Page) aggCard(c *ui.Context, s model.Subscription, switching string) {
	t := c.Theme()
	k := p.e.Kit
	sub := s
	card := ui.Row(c.Key("agg-card-"+s.ID)).Gap(14).AlignItems(ui.Center).Padding(14, 16).Radius(14).
		Background(t.Surface).Border(1, t.Border.Alpha(0.65))
	if s.Current {
		card.Border(1.5, t.Accent.Alpha(0.8))
	}
	card.Children(func() {
		ui.Box(c).Size(40, 40).Radius(10).Background(t.Accent.Alpha(0.12)).TextColor(t.Accent).
			AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() { k.Icon(c, "layers", 20) })
		ui.Column(c).Gap(5).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, s.Name).FontSize(14.5).FontWeight(600).SingleLine().Shrink(1).Tooltip(s.Name)
				if s.Current {
					k.Tag(c, i18n.T("当前"), "accent")
				}
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				var parts []string
				if sm, ok := p.aggSum[s.ID]; ok {
					parts = append(parts, i18n.Tf("%d 个来源", sm.sources), sm.mode)
				}
				switch {
				case s.Updating:
					ui.Spinner(c).Size(11, 11).Label(i18n.T("更新中"))
					parts = append(parts, i18n.T("更新中…"))
				case switching == s.ID:
					ui.Spinner(c).Size(11, 11).Label(i18n.T("切换中"))
					parts = append(parts, i18n.T("切换中…"))
				case !s.UpdatedAt.IsZero():
					parts = append(parts, i18n.Tf("更新于 %s", env.FmtAgo(s.UpdatedAt)))
				}
				if s.Total > 0 {
					parts = append(parts, i18n.Tf("已用 %s / %s GB", env.FmtGB(s.Upload+s.Download), env.FmtGB(s.Total)))
				}
				ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1).FontFeatures("tnum")
				if s.LastError != "" {
					ui.Box(c).TextColor(t.Warning).Cursor(ui.CursorHelp).Tooltip(s.LastError).Label(i18n.T("更新失败")).Shrink(0).Children(func() {
						k.Icon(c, "warn", 13)
					})
				}
			})
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			if !s.Current {
				if ui.Button(c, i18n.T("使用")).Disabled(switching != "").Tooltip(i18n.T("使用此订阅")).Clicked() {
					p.use(sub)
				}
			}
			up := ui.Button(c, "").Label(i18n.Tf("更新 %s", s.Name)).Tooltip(i18n.T("重新下载来源并生成")).Disabled(s.Updating)
			up.Children(func() { k.Icon(c, "refresh", 13) })
			if up.Clicked() {
				p.update(sub)
			}
			ed := ui.PrimaryButton(c, "").Label(i18n.Tf("编辑 %s", s.Name))
			ed.Children(func() {
				k.Icon(c, "edit", 13)
				ui.Text(c, i18n.T("编辑")).SingleLine()
			})
			if ed.Clicked() {
				p.openAggregate(&sub)
			}
			ui.Button(c, "").Label(i18n.Tf("更多 %s", s.Name)).Tooltip(i18n.T("更多")).Children(func() { k.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
				p.aggMenu(m, sub)
			})
		})
	})
}

// aggMenu is the ⋯ menu of an aggregate subscription's card.
func (p *Page) aggMenu(m *ui.Menu, sub model.Subscription) {
	if m.Item(i18n.T("编辑覆写…")).Chosen() {
		p.e.Navigate(env.Settings, "override:"+sub.ID)
	}
	if m.Item(i18n.T("查看配置")).Chosen() {
		p.openRaw(sub)
	}
	m.Separator()
	del := m.Item(i18n.T("删除…")).Danger().Disabled(sub.Current)
	if sub.Current {
		del.Tooltip(i18n.T("请先切换到其他订阅"))
	}
	if del.Chosen() {
		p.askDelete(sub)
	}
}

// updatingAny reports whether one of subs is updating.
func (p *Page) updatingAny(subs []model.Subscription) bool {
	for _, s := range subs {
		if s.Updating {
			return true
		}
	}
	return false
}

// loadSummaries reads the specs of subs in the background when the list
// or an update time changed since the last read.
func (p *Page) loadSummaries(subs []model.Subscription) {
	var sig strings.Builder
	ids := make([]string, len(subs))
	for i, s := range subs {
		ids[i] = s.ID
		sig.WriteString(s.ID + "@" + strconv.FormatInt(s.UpdatedAt.UnixNano(), 10) + ";")
	}
	if sig.String() == p.aggSumSig || p.aggSumBusy {
		return
	}
	p.aggSumBusy = true
	want := sig.String()
	out := map[string]aggSummary{}
	p.e.Then(func() error {
		for _, id := range ids {
			spec, err := p.e.API.AggregateSpec(id)
			if err != nil {
				continue
			}
			sm := aggSummary{mode: i18n.T("高级配置")}
			for _, src := range spec.Sources {
				if !src.Disabled {
					sm.sources++
				}
			}
			if tp := profile.AggTemplate(spec.Template); tp != nil {
				sm.mode = i18n.T(tp.Label)
			} else if strings.HasPrefix(spec.Template, "my:") {
				sm.mode = i18n.T("我的模板")
			}
			out[id] = sm
		}
		return nil
	}, func(error) {
		p.aggSumBusy = false
		p.aggSum, p.aggSumSig = out, want
	})
}
