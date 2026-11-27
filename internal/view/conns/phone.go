package conns

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
)

// The 连接 page on a phone (docs/phone-ui.md): a list of two-line rows in
// place of the table, the detail in place of the list rather than beside
// it, and no topology. The desktop's layout is in page.go.

// phoneView draws the content area on a phone; the shell's top bar shows
// the title and the actions (PhoneActions), and its back button closes
// the detail (PhoneSub, PhonePop).
func (p *Page) phoneView(c *ui.Context, t *ui.Theme, running bool, active, closed []model.Connection, nActive int, subtitle string) {
	e := p.e
	if running && p.sel != "" {
		src := active
		if p.tab == 1 {
			src = closed
		}
		if cn, ok := find(src, p.sel); ok {
			p.selHost = cn.Host
			p.phoneDetail(c, t, cn, p.tab == 0)
			return
		}
		p.sel = ""
	}
	p.phoneTools(c, t, subtitle)
	if !running {
		e.Kit.Empty(c, "conns", i18n.T("内核未启动"), i18n.T("启动内核后这里会实时显示经过澜流的连接。"), nil)
		return
	}
	p.phoneList(c, t, active, closed)
}

// PhoneSub is env.PhoneStack: the detail is a page pushed over the list.
func (p *Page) PhoneSub() (string, string, bool) {
	if p.sel == "" {
		return "", "", false
	}
	return p.selHost, i18n.T("连接"), true
}

// PhonePop closes the detail.
func (p *Page) PhonePop() { p.sel = "" }

// PhoneActions are the top bar's: the filter (活动 / 已关闭), 暂停 and ⋯
// with 全部断开.
func (p *Page) PhoneActions(c *ui.Context) {
	e := p.e
	st := e.API.Status()
	running := st.Core == model.CoreRunning
	env.IconButton(c, e.Kit, "filter", i18n.T("筛选连接")).Menu(func(m *ui.Menu) {
		if m.Item(i18n.T("活动")).Radio(p.tab == 0).Chosen() {
			p.tab, p.sel = 0, ""
		}
		if m.Item(i18n.T("已关闭")).Radio(p.tab == 1).Chosen() {
			p.tab, p.sel = 1, ""
		}
	})
	pauseLabel, icon := i18n.T("暂停"), "pause"
	if p.paused {
		pauseLabel, icon = i18n.T("继续"), "play"
	}
	if env.IconButton(c, e.Kit, icon, pauseLabel).Disabled(!running).Clicked() {
		p.paused = !p.paused
	}
	n := st.Connections
	env.IconButton(c, e.Kit, "more", i18n.T("更多")).Menu(func(m *ui.Menu) {
		if m.Item(i18n.T("全部断开…")).Disabled(!running || n == 0).Danger().Chosen() {
			e.Confirm(i18n.Tn(n, "断开全部 %d 个连接？", n), i18n.T("应用会自动重新建立需要的连接。"), i18n.T("断开"), false, func() {
				p.sel = ""
				e.Run(e.API.CloseAllConnections, i18n.T("已断开全部连接"))
			})
		}
	})
}

// phoneTools is the search bar, and under it what the list shows: the
// active connections' count, or 已关闭.
func (p *Page) phoneTools(c *ui.Context, t *ui.Theme, subtitle string) {
	ui.Column(c).Gap(6).Padding(0, 16, 4, 16).Shrink(0).Children(func() {
		env.SearchBar(c, "search", &p.query, i18n.T("搜索"), i18n.T("搜索连接")).Bind(&p.search)
		line := subtitle
		if p.tab == 1 {
			line = i18n.T("已关闭")
		}
		if line != "" {
			ui.Text(c, line).FontSize(13).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().Padding(4, 4, 0, 4)
		}
	})
}

// phoneList is the list of connections: a row each, the host above the
// process, rule, chain and speed; a tap opens the detail.
func (p *Page) phoneList(c *ui.Context, t *ui.Theme, active, closed []model.Connection) {
	src := active
	if p.tab == 1 {
		src = closed
	}
	rows := p.rows(src)
	if len(rows) == 0 {
		switch {
		case strings.TrimSpace(p.query) != "":
			p.e.Kit.Empty(c, "search", i18n.Tf("没有匹配“%s”的连接", strings.TrimSpace(p.query)), "", nil)
		case p.tab == 1:
			p.e.Kit.Empty(c, "conns", i18n.T("暂无已关闭连接"), i18n.T("断开的连接会在这里保留最近 500 条，仅保存在内存中。"), nil)
		default:
			p.e.Kit.Empty(c, "conns", i18n.T("暂无活动连接"), "", nil)
		}
		return
	}
	p.plist.Key = func(i int) any { return rows[i].ID }
	p.plist.Label = func(i int) string { return rows[i].Host }
	p.plist.KeepOffset = true
	ui.List(c.Key("phone-list"), &p.plist, len(rows), func(i int) {
		p.phoneRow(c, t, rows[i])
	}).Grow(1).Basis(0).MinHeight(0).FillWidth().Label(i18n.T("连接"))
}

// phoneRow is one connection of the list.
func (p *Page) phoneRow(c *ui.Context, t *ui.Theme, cn model.Connection) {
	q := strings.TrimSpace(p.query)
	active := p.tab == 0
	row := ui.ButtonBase(c).Row().MinHeight(56).Padding(8, 16).Gap(10).AlignItems(ui.Center).FillWidth().Label(cn.Host)
	if row.Pressed() {
		row.Background(t.Text.Alpha(0.06))
	}
	row.ContextMenu(func(m *ui.Menu) { p.rowMenu(c, m, cn, active) })
	row.Children(func() {
		if cn.Process != "" {
			p.icons.Draw(c, t, cn.ProcessPath, procicon.Size)
		} else {
			ui.Box(c).Width(procicon.Size).Shrink(0)
		}
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			env.Highlight(c, cn.Host, q, t.Text).FontSize(14).SingleLine()
			env.Highlight(c, phoneSubtitle(cn), q, t.TextMuted).FontSize(12).SingleLine().FontFeatures("tnum")
		})
		p.e.Kit.Icon(c, "chevron", 12)
	})
	if row.Clicked() {
		p.sel = cn.ID
	}
}

// phoneSubtitle is a row's second line: process · rule · chain · speed.
func phoneSubtitle(cn model.Connection) string {
	parts := []string{processDisplayName(cn), ruleName(cn), chain(cn)}
	if cn.DownSpeed > 0 || cn.UpSpeed > 0 {
		parts = append(parts, "↓ "+env.FmtSpeed(cn.DownSpeed)+" ↑ "+env.FmtSpeed(cn.UpSpeed))
	}
	return strings.Join(parts, " · ")
}

// phoneDetail shows one connection in place of the list; the shell's back
// button (and Android's back) goes back to it.
func (p *Page) phoneDetail(c *ui.Context, t *ui.Theme, cn model.Connection, active bool) {
	ui.Column(c.Key("phone-detail")).Grow(1).Basis(0).MinHeight(0).FillWidth().Children(func() {
		p.detailBody(c, t, cn, active, true)
	})
}

// phoneActions are the detail's actions on a phone: rows of an inset group,
// in the accent color, 断开此连接 in red, each a finger's height.
func (p *Page) phoneActions(c *ui.Context, cn model.Connection, active bool) {
	t := c.Theme()
	row := func(key, label string, col ui.Color, disabled bool, do func()) {
		r := ui.ButtonBase(c.Key(key)).Row().MinHeight(env.TouchSize).PaddingX(16).AlignItems(ui.Center).FillWidth().
			Label(label).Disabled(disabled).Cursor(ui.CursorPointer)
		if r.Pressed() {
			r.Background(t.SurfaceHover)
		}
		if disabled {
			col = t.TextMuted
		}
		r.Children(func() { ui.Text(c, label).FontSize(16).TextColor(col) })
		if r.Clicked() {
			do()
		}
	}
	ui.Column(c).FillWidth().Radius(env.PhoneRadius).Background(t.Surface).Clip().Dividers(0.5, t.Border).Children(func() {
		row("rule-host", i18n.T("此域名始终走…"), t.Accent, false, func() { p.addRuleForHost(cn) })
		row("rule-proc", i18n.T("此程序始终走…"), t.Accent, cn.Process == "", func() { p.addRuleForProcess(cn) })
		if active && cn.ClosedAt.IsZero() {
			id := cn.ID
			row("close", i18n.T("断开此连接"), t.Danger, false, func() { p.closeConn(id) })
		}
	})
}
