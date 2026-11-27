package logs

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// The 日志 page on a phone (docs/phone-ui.md): a toolbar in two rows that
// wrap, and each line in two: the time and the level above, the message
// wrapped below, so nothing runs off the narrow screen. The desktop's
// layout is in page.go.

// phoneTools is the toolbar on a phone: the source the screen's width, then
// the search bar; 暂停 and ⋯ are in the top bar (PhoneActions).
func (p *Page) phoneTools(c *ui.Context, all []model.LogEntry) {
	ui.Column(c).Gap(8).Padding(0, 16, 8, 16).Shrink(0).Children(func() {
		src := p.source
		if env.Segmented(c, "log-source", &src, i18n.T("内核"), i18n.T("澜流")).Label(i18n.T("日志来源")).Changed() {
			p.source = src
			p.frozen, p.paused = nil, false
			p.list.ScrollToEnd()
			p.persist()
		}
		env.SearchBar(c, "search", &p.query, i18n.T("搜索"), i18n.T("搜索日志")).Bind(&p.search)
	})
}

// PhoneActions are the top bar's: 暂停 and ⋯ (the level, copy, export,
// 清空). No file manager and no line wrapping to choose on a phone: the
// lines always wrap.
func (p *Page) PhoneActions(c *ui.Context) {
	e := p.e
	all := p.entries()
	pauseLabel, icon := i18n.T("暂停"), "pause"
	if p.paused {
		pauseLabel, icon = i18n.T("继续"), "play"
	}
	if env.IconButton(c, e.Kit, icon, pauseLabel).Clicked() {
		p.paused = !p.paused
	}
	env.IconButton(c, e.Kit, "more", i18n.T("更多")).Menu(func(m *ui.Menu) {
		opts := levelOptions()
		m.Submenu(i18n.T("级别"), func(sub *ui.Menu) {
			for i, o := range opts {
				if sub.Item(o).Radio(p.level == i).Chosen() && p.level != i {
					p.level = i
					p.persist()
				}
			}
		})
		m.Separator()
		if m.Item(i18n.T("复制全部")).Disabled(len(all) == 0).Chosen() {
			c.WriteClipboard(text(all))
			e.Toast(i18n.T("已复制"), false)
		}
		if m.Item(i18n.T("导出诊断信息…")).Chosen() {
			go ExportDiagnostics(e)
		}
		m.Separator()
		if m.Item(i18n.T("清空")).Disabled(len(all) == 0).Danger().Chosen() {
			p.clear()
		}
	})
}

// phoneLine is one entry on a phone: the time and the level on the first
// line, the message wrapped under them.
func (p *Page) phoneLine(c *ui.Context, t *ui.Theme, l model.LogEntry) {
	color, timeColor := lineColors(l.Level, t)
	ui.Column(c).Gap(2).PaddingX(16).PaddingY(6).FillWidth().Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, l.Time.Format("15:04:05")).FontSize(11).TextColor(timeColor).FontFeatures("tnum").Unselectable().Shrink(0)
			ui.Text(c, levelName(l.Level)).FontSize(11).TextColor(color).Unselectable().Shrink(0).SingleLine()
		})
		env.Highlight(c, l.Payload, strings.TrimSpace(p.query), color).FillWidth().MinWidth(0)
	})
}
