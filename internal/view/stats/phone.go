package stats

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
)

// phoneRows is the most rows the phone's list draws: it is not virtual,
// and the search finds the rest.
const phoneRows = 200

// phoneChartH is the chart's height on a phone.
const phoneChartH = 150

// phoneView is the page on a phone (env.Env.Phone): one column that
// scrolls, the totals three a line, the chart, the dimensions in a strip
// that scrolls sideways, and the buckets as two-line rows in place of the
// table.
func (p *Page) phoneView(c *ui.Context, t *ui.Theme, st model.TrafficStats, subtitle string) {
	e := p.e
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("统计"), subtitle, func() { p.tools(c) })
		ui.Scroll(c.Key("phone")).Grow(1).Padding(0, 16, 16, 16).Gap(12).Children(func() {
			p.cards(c, t, st)
			if st.Totals.Requests == 0 && st.Totals.Upload+st.Totals.Download == 0 {
				e.Kit.Empty(c, "stats", i18n.T("暂无统计数据"), i18n.T("内核运行时，经过澜流的流量会按设备、域名、进程等在这里汇总。"), nil)
				return
			}
			ui.Column(c).Height(phoneChartH).Shrink(0).Children(func() { p.chart(c, t, st) })
			p.phoneList(c, t, st)
		})
	})
}

// phoneList is the dimension switch, the search and the rows on a phone.
func (p *Page) phoneList(c *ui.Context, t *ui.Theme, st model.TrafficStats) {
	e := p.e
	// The cut-off last dimension shows that the strip scrolls; a bar
	// would cover the segments.
	ui.ScrollHorizontal(c.Key("dims")).Shrink(0).Scrollbars(false).Children(func() {
		ui.Segmented(c.Key("dim"), &p.dim, i18n.Options(dimLabels...)...).Label(i18n.T("统计维度")).Shrink(0)
	})
	ui.Row(c).Gap(10).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索")).Label(i18n.T("搜索统计")).Grow(1).Bind(&p.search)
		if dims[p.dim] == model.StatsDomain {
			ui.Checkbox(c, &p.hosts, i18n.T("完整主机名")).Shrink(0)
		}
	})
	rows := p.rows(st)
	q := strings.TrimSpace(p.query)
	if len(rows) == 0 {
		if q != "" {
			e.Kit.Empty(c, "search", i18n.Tf("没有匹配“%s”的项目", q), "", nil)
		} else {
			e.Kit.Empty(c, "stats", i18n.T("此范围内暂无数据"), "", nil)
		}
		return
	}
	more := len(rows) - phoneRows
	if more > 0 {
		rows = rows[:phoneRows]
	}
	ui.Column(c).Shrink(0).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Dividers(1, t.Border).Label(i18n.T("统计")).Children(func() {
		for _, r := range rows {
			p.phoneRow(c, t, r, q)
		}
	})
	if more > 0 {
		ui.Text(c, i18n.Tn(more, "另有 %d 项未列出，搜索可以找到", more)).FontSize(11).TextColor(t.TextMuted).Padding(0, 4)
	}
}

// phoneRow is one bucket on a phone: the name and its total on the first
// line, the detail and the upload and download under them.
func (p *Page) phoneRow(c *ui.Context, t *ui.Theme, r row, q string) {
	e := p.e
	withIcon := dims[p.dim] == model.StatsProcess && r.Key != "" && r.Key != model.StatsOtherKey
	name := r.name
	row := ui.Row(c.Key(r.Key)).Gap(10).MinHeight(env.TouchSize).Padding(8, 14).AlignItems(ui.Center)
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item(i18n.T("复制名称")).Chosen() {
			c.WriteClipboard(name)
			e.Toast(i18n.T("已复制"), false)
		}
	})
	row.Children(func() {
		if withIcon {
			p.icons.Draw(c, t, r.Detail, procicon.Size)
		}
		ui.Column(c).Gap(2).Grow(1).Basis(0).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
				env.Highlight(c, r.name, q, t.Text).FontSize(13).SingleLine().Shrink(1).MinWidth(0)
				ui.Spacer(c)
				ui.Text(c, env.FmtBytes(r.Total())).FontSize(13).FontWeight(600).FontFeatures("tnum").SingleLine().Shrink(0)
			})
			ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
				if r.detail != "" && r.detail != r.name {
					env.Highlight(c, r.detail, q, t.TextMuted).FontSize(11).SingleLine().Shrink(1).MinWidth(0)
				}
				ui.Spacer(c)
				ui.Text(c, "↑ "+env.FmtBytes(r.Upload)+"  ↓ "+env.FmtBytes(r.Download)).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").SingleLine().Shrink(0)
			})
		})
	})
}
