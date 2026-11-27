package subs

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// The 订阅 and 聚合订阅 pages on a phone (env.Env.Phone, docs/phone-ui.md):
// iOS's large title, a + in the top bar for the imports, borderless cards
// a tap uses, a swipe left edits or deletes and a long press opens the
// rest; a pull from the top updates them all. The desktop's table is
// untouched.

// PhoneLargeTitle: 订阅 starts with iOS's large title; 聚合订阅, pushed
// from 设置, does not.
func (p *Page) PhoneLargeTitle() bool { return !p.aggOnly }

// PhoneActions puts the page's + in the phone's top bar: the import menu
// on 订阅, a new aggregate subscription on 聚合订阅 (nothing while its
// editor shows, which has its own).
func (p *Page) PhoneActions(c *ui.Context) {
	k := p.e.Kit
	if p.aggOnly {
		if p.agg.open {
			return
		}
		if env.IconButton(c, k, "plus", i18n.T("新建聚合订阅")).Clicked() {
			p.openAggregate(nil)
		}
		return
	}
	env.IconButton(c, k, "plus", i18n.T("导入")).Menu(func(m *ui.Menu) {
		p.importMenu(c, m)
	})
}

// phoneView draws 订阅 on a phone: the cards under the large title, or
// the empty state.
func (p *Page) phoneView(c *ui.Context, subs []model.Subscription, switching string) {
	if len(subs) == 0 {
		p.phoneEmpty(c)
		return
	}
	top := float32(0)
	if env.Material() {
		top = env.PhoneGap / 2 // no large title under the top bar
	}
	sc := p.e.Scroll(c, env.Subs).Fill()
	if sc.PullToRefresh(&p.refresh) {
		p.updateAll()
	}
	p.settleRefresh()
	sc.Children(func() {
		ui.Column(c).Padding(top, env.PhoneGap, 24, env.PhoneGap).Gap(12).Children(func() {
			env.LargeTitle(c, i18n.T("订阅"))
			for _, s := range subs {
				p.phoneCard(c, s, switching)
			}
		})
	})
}

// settleRefresh sends the pull to refresh's indicator away once 全部更新
// is done (at once when it was already running and ended, or failed to
// start).
func (p *Page) settleRefresh() {
	p.mu.Lock()
	updating := p.updating
	p.mu.Unlock()
	if p.refresh.Refreshing && !updating {
		p.refresh.Refreshing = false
	}
}

// phoneEmpty is 订阅 without subscriptions: the large title and the
// empty state with its imports.
func (p *Page) phoneEmpty(c *ui.Context) {
	if p.e.PhoneScroll != nil {
		// No list to scroll: the large title shows.
		p.e.PhoneScroll(env.Subs).Y = 0
	}
	k := p.e.Kit
	ui.Column(c).Fill().Padding(0, env.PhoneGap, env.PhoneGap, env.PhoneGap).Children(func() {
		env.LargeTitle(c, i18n.T("订阅"))
		k.Empty(c, "subs", i18n.T("还没有订阅"), i18n.T("从订阅链接导入，或选择一个 .yaml / .yml 配置文件。"), func() {
			if ui.PrimaryButton(c, i18n.T("从链接导入")).MinHeight(env.TouchSize).Clicked() {
				p.openImport(c)
			}
			if ui.Button(c, i18n.T("从文件导入")).MinHeight(env.TouchSize).Clicked() {
				p.importFile()
			}
		})
	})
}

// swipeState is the swipe state of a subscription's card.
func (p *Page) swipeState(id string) *mobile.SwipeState {
	if p.swipes == nil {
		p.swipes = map[string]*mobile.SwipeState{}
	}
	st := p.swipes[id]
	if st == nil {
		st = &mobile.SwipeState{}
		p.swipes[id] = st
	}
	return st
}

// phoneCard is one subscription: a card that a tap uses, a swipe left
// shows 编辑 and 删除 (not for the current one) behind, and a long press
// opens the row's menu.
func (p *Page) phoneCard(c *ui.Context, s model.Subscription, switching string) {
	t := c.Theme()
	sub := s
	st := p.swipeState(s.ID)
	actions := []mobile.SwipeAction{{Label: i18n.T("编辑"), OnTap: func() { p.openEdit(sub) }}}
	if !s.Current {
		actions = append(actions, mobile.SwipeAction{Label: i18n.T("删除"), Destructive: true, OnTap: func() { p.askDelete(sub) }})
	}
	if *st == (mobile.SwipeState{}) {
		// At rest the actions take the page's color: the card's rounded
		// edge, smoothed, would show theirs around it.
		bg := t.Background
		for i := range actions {
			actions[i].Color = &bg
		}
	}
	mobile.SwipeRow(c.Key("sub-swipe-"+s.ID), st, mobile.SwipeOptions{Trailing: actions}, func() {
		card := env.PhoneCard(c.Key("sub-card-" + s.ID)).Role(ui.RoleButton).Label(s.Name)
		if card.Pressed() {
			card.Background(t.SurfacePressed)
		}
		card.ContextMenu(func(m *ui.Menu) { p.rowMenu(c, m, sub) })
		card.Children(func() { p.phoneCardBody(c, s, switching) })
		if card.Clicked() {
			switch {
			case *st != (mobile.SwipeState{}):
				st.Close() // a tap on a swiped card puts it back
			case !s.Current && switching == "":
				p.use(sub)
			}
		}
	}).Radius(env.PhoneRadius)
}

// phoneCardBody is what a card shows: the name with a check on the
// current one, when it was updated and when it expires, and the traffic.
func (p *Page) phoneCardBody(c *ui.Context, s model.Subscription, switching string) {
	t := c.Theme()
	k := p.e.Kit
	ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(24).Children(func() {
		ui.Text(c, s.Name).FontSize(16).FontWeight(600).SingleLine().Shrink(1)
		ui.Spacer(c)
		switch {
		case s.Updating:
			ui.Spinner(c).Size(16, 16).Label(i18n.T("更新中"))
		case switching == s.ID:
			ui.Spinner(c).Size(16, 16).Label(i18n.T("切换中"))
		}
		if s.Current {
			ui.Box(c).TextColor(t.Accent).Label(i18n.T("当前")).Shrink(0).Children(func() { k.Icon(c, "check", 20) })
		}
	})
	// 更新于 … · 到期 …, the troubles in the warning colors.
	type part struct {
		text string
		col  ui.Color
	}
	var parts []part
	switch {
	case s.Updating:
		parts = append(parts, part{i18n.T("更新中…"), t.TextMuted})
	case switching == s.ID:
		parts = append(parts, part{i18n.T("切换中…"), t.TextMuted})
	case s.NeedsURL:
		parts = append(parts, part{i18n.T("需要重新输入链接"), t.Warning})
	case s.LastError != "":
		parts = append(parts, part{i18n.T("更新失败"), t.Warning})
	case !s.UpdatedAt.IsZero():
		parts = append(parts, part{i18n.Tf("更新于 %s", env.FmtAgo(s.UpdatedAt)), t.TextMuted})
	}
	if !s.Expire.IsZero() {
		switch {
		case s.Expire.Before(time.Now()):
			parts = append(parts, part{i18n.T("已过期"), t.Danger})
		case time.Until(s.Expire) < 7*24*time.Hour:
			parts = append(parts, part{i18n.Tf("到期 %s", env.FmtDate(s.Expire)), t.Warning})
		default:
			parts = append(parts, part{i18n.Tf("到期 %s", env.FmtDate(s.Expire)), t.TextMuted})
		}
	}
	if len(parts) > 0 {
		ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
			for i, pt := range parts {
				if i > 0 {
					ui.Text(c, "·").FontSize(13).TextColor(t.TextMuted)
				}
				ui.Text(c, pt.text).FontSize(13).TextColor(pt.col).FontFeatures("tnum").SingleLine()
			}
		})
	}
	if s.Total > 0 {
		used := s.Upload + s.Download
		frac := min(1, float64(used)/float64(s.Total))
		ui.Column(c).Gap(6).Margin(4, 0, 0, 0).Children(func() {
			bar := ui.Progress(c, frac).FillWidth().Label(i18n.T("已用流量"))
			if frac > 0.85 {
				bar.TextColor(t.Warning)
			}
			ui.Row(c).Children(func() {
				ui.Text(c, i18n.Tf("已用 %s / %s GB", env.FmtGB(used), env.FmtGB(s.Total))).FontSize(12).
					TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().Shrink(1)
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%.0f%%", frac*100)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine()
			})
		})
	}
}

// phoneAggregates draws 聚合订阅's list on a phone: cards a tap edits and
// a long press uses, updates or deletes; the + of the top bar adds one.
func (p *Page) phoneAggregates(c *ui.Context, subs []model.Subscription) {
	t := c.Theme()
	k := p.e.Kit
	if len(subs) == 0 {
		k.Empty(c, "layers", i18n.T("还没有聚合订阅"), i18n.T("从订阅链接、节点链接或 YAML 生成自己的订阅：节点筛选、代理组和分流规则都可以在这里编辑，之后随时回来修改、更新。"), func() {
			if ui.PrimaryButton(c, i18n.T("新建聚合订阅")).MinHeight(env.TouchSize).Clicked() {
				p.openAggregate(nil)
			}
		})
		return
	}
	p.mu.Lock()
	switching := p.switching
	p.mu.Unlock()
	ui.Scroll(c.Key("agg-list")).Grow(1).Shrink(1).Basis(0).Children(func() {
		ui.Column(c).Gap(12).Padding(env.PhoneGap/2, env.PhoneGap, 24, env.PhoneGap).Children(func() {
			for _, s := range subs {
				p.phoneAggCard(c, s, switching)
			}
			ui.Text(c, i18n.T("聚合订阅也会出现在「订阅」页，可以和其他订阅一样切换使用。")).FontSize(12).TextColor(t.TextMuted).
				TextAlign(ui.Center).Margin(4, 0, 0, 0)
		})
	})
}

// phoneAggCard is one aggregate subscription: its name, a check on the
// current one and a summary; a tap opens the editor, a long press the
// menu.
func (p *Page) phoneAggCard(c *ui.Context, s model.Subscription, switching string) {
	t := c.Theme()
	k := p.e.Kit
	sub := s
	card := env.PhoneCard(c.Key("agg-card-" + s.ID)).Role(ui.RoleButton).Label(i18n.Tf("编辑 %s", s.Name))
	if card.Pressed() {
		card.Background(t.SurfacePressed)
	}
	card.ContextMenu(func(m *ui.Menu) {
		if m.Item(i18n.T("使用此订阅")).Disabled(sub.Current || switching != "").Chosen() {
			p.use(sub)
		}
		if m.Item(i18n.T("更新")).Disabled(sub.Updating).Chosen() {
			p.update(sub)
		}
		m.Separator()
		if m.Item(i18n.T("删除…")).Danger().Disabled(sub.Current).Chosen() {
			p.askDelete(sub)
		}
	})
	card.Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(36, 36).Radius(9).Background(t.Accent.Alpha(0.12)).TextColor(t.Accent).
				AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() { k.Icon(c, "layers", 18) })
			ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, s.Name).FontSize(16).FontWeight(600).SingleLine()
				var parts []string
				if sm, ok := p.aggSum[s.ID]; ok {
					parts = append(parts, i18n.Tf("%d 个来源", sm.sources), sm.mode)
				}
				switch {
				case s.Updating:
					parts = append(parts, i18n.T("更新中…"))
				case switching == s.ID:
					parts = append(parts, i18n.T("切换中…"))
				case !s.UpdatedAt.IsZero():
					parts = append(parts, i18n.Tf("更新于 %s", env.FmtAgo(s.UpdatedAt)))
				}
				if s.Total > 0 {
					parts = append(parts, i18n.Tf("已用 %s / %s GB", env.FmtGB(s.Upload+s.Download), env.FmtGB(s.Total)))
				}
				if len(parts) > 0 {
					ui.Text(c, strings.Join(parts, " · ")).FontSize(13).TextColor(t.TextMuted).FontFeatures("tnum")
				}
				if s.LastError != "" {
					ui.Text(c, i18n.T("更新失败")).FontSize(13).TextColor(t.Warning)
				}
			})
			if s.Updating || switching == s.ID {
				ui.Spinner(c).Size(16, 16).Label(i18n.T("更新中"))
			}
			if s.Current {
				ui.Box(c).TextColor(t.Accent).Label(i18n.T("当前")).Shrink(0).Children(func() { k.Icon(c, "check", 20) })
			}
		})
	})
	if card.Clicked() {
		p.openAggregate(&sub)
	}
}
