package probe

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// The result tables on a phone (env.Env.Phone) are lists of two-line rows
// in a rounded box that scrolls: what the desktop's columns say, read
// down rather than across.

// phoneBox is the rounded group of rows a phone's result list draws in,
// scrolling when it is longer than the screen.
func phoneBox(c *ui.Context, t *ui.Theme, key, label string, rows func()) {
	ui.Scroll(c.Key(key)).Grow(1).Basis(0).Children(func() {
		ui.Column(c).Shrink(0).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Dividers(1, t.Border).Label(label).Children(rows)
	})
}

// phoneUnlock lists the 解锁检测 results: the service and its verdict, the
// region, time and note under them.
func (p *Page) phoneUnlock(c *ui.Context, t *ui.Theme, rows []model.UnlockResult) {
	phoneBox(c, t, "unlock", i18n.T("解锁检测"), func() {
		for _, r := range rows {
			ui.Column(c.Key(r.Service)).Gap(3).MinHeight(env.TouchSize).Padding(8, 14).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, i18n.T(r.Service)).FontSize(13).SingleLine().Shrink(1)
					ui.Spacer(c)
					p.statusCell(c, t, r)
				})
				sub := countryText(r.Region)
				if r.Millis > 0 {
					sub += " · " + fmt.Sprintf("%d ms", r.Millis)
				}
				if r.Note != "" {
					sub += " · " + r.Note
				}
				ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted).Selectable()
			})
		}
	})
}

// phoneRegion lists the 地区探查 nodes: the node and its delay, then its
// exit, place and unlocks, and the action that makes it the main group's
// node (or the 当前 tag).
func (p *Page) phoneRegion(c *ui.Context, t *ui.Theme, rows []regionRow, withUnlock bool, cur, mainGroup string) {
	e := p.e
	phoneBox(c, t, "region", i18n.T("地区探查"), func() {
		for _, r := range rows {
			ui.Row(c.Key(r.Node)).Gap(8).MinHeight(env.TouchSize).Padding(6, 6, 6, 14).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(3).Grow(1).Basis(0).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, r.Node).FontSize(13).SingleLine().Shrink(1)
						ui.Spacer(c)
						e.Kit.Delay(c, r.Delay, r.Testing)
					})
					ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
						switch {
						case r.Error != "":
							ui.Text(c, i18n.T("失败")).FontSize(11).TextColor(t.Danger)
							ui.Text(c, r.Error).FontSize(11).TextColor(t.TextMuted).MaxLines(2).Shrink(1)
						case !r.Probed:
							ui.Text(c, "—").FontSize(11).TextColor(t.TextMuted)
						default:
							ui.Text(c, r.Exit.IP+" · "+placeText(e.Mac, r.Exit.Place)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").Selectable().Shrink(1)
						}
						if withUnlock && r.total > 0 {
							e.Kit.Tag(c, i18n.Tf("%d / %d 可用", r.unlocked, r.total), unlockKind(r))
						}
					})
				})
				if r.Node == cur {
					ui.Box(c).Padding(0, 8).Shrink(0).Children(func() { e.Kit.Tag(c, i18n.T("当前"), "accent") })
					return
				}
				node := r.Node
				b := env.LinkText(c, i18n.T("设为当前"), i18n.Tf("设为当前 %s", node)).Padding(15, 8).Shrink(0)
				if b.Clicked() && mainGroup != "" {
					e.Run(func() error { return e.API.SelectProxy(mainGroup, node) }, i18n.Tf("已切换到「%s」", node))
				}
			})
		}
	})
}

// unlockKind colors a node's unlock count: all, some or none available.
func unlockKind(r regionRow) string {
	switch {
	case r.unlocked == 0:
		return "red"
	case r.unlocked < r.total:
		return "orange"
	}
	return "green"
}
