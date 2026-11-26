// Package conns is the 连接 page (docs/design.md §5.4): a sortable table of
// the connections going through the core, a detail panel, and the entry
// points for adding a custom rule from a connection.
package conns

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/colwidth"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
	"lanflow/internal/view/rules/ruleform"
)

const detailWidth = 280

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query  string
	search ui.Handle // the search field, focused by ⌘F
	paused bool
	// pausedFlag mirrors paused for Wants (any goroutine).
	pausedFlag atomic.Bool
	// frozen are the lists held: each is read when first needed after the
	// connections changed (data), and kept as it is while paused. gen
	// counts the reads, for what is derived from them (sorted).
	frozen struct {
		active, closed       []model.Connection
		hasActive, hasClosed bool
	}
	data *env.Stale
	gen  uint64
	// sorted are the rows of the list view, filtered and in order, for
	// the lists, tab, query and order they were made for.
	sorted struct {
		ok    bool
		gen   uint64
		tab   int
		query string
		sort  ui.SortOrder
		rows  []model.Connection
	}
	tab      int // 0 活动, 1 已关闭
	view     int // 0 列表, 1 拓扑 (topology.go)
	topo     topology
	sort     ui.SortOrder
	list     ui.ListState
	selected int    // row index the table chooses
	sel      string // ID of the connection shown in the detail panel
	hoverRow int
	// detailW is the detail panel's width, which the user drags its edge
	// to change (env.DetailEdge), remembered in the UI state; 0 = default.
	detailW float32
	form    ruleform.Form
	icons   *procicon.Cache // application icons of the 进程 column

	// hidden holds the IDs of the columns the user hid through the header's
	// context menu; it mirrors Settings.ConnHiddenColumns.
	hidden map[string]bool
	// cols are the columns shown, in the order of allColumns; rebuilt
	// when the settings or the language change (colsStale, colsLang).
	cols      []ui.TableColumn
	colsStale *env.Stale
	colsLang  string

	// hay caches each connection's search text, lowercased, by ID
	// (haystack); kept only while a query is typed.
	hay map[string]hayText
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, selected: -1, hoverRow: -1, sort: ui.SortOrder{Column: "down", Descending: true}, icons: procicon.New(e.API),
		colsStale: env.Watch(e.API, app.TopicSettings), data: env.Watch(e.API, app.TopicConns, app.TopicStatus)}
	p.topo.icons = p.icons
	p.list.Sort = &p.sort
	p.list.Selected = &p.selected
	p.list.KeepOffset = true
	p.list.HeaderMenu = p.headerMenu
	return p
}

// allColumns are the table's columns (docs/design.md §5.4); the user
// chooses which show by right-clicking the header. 主机 always shows.
var allColumns = []ui.TableColumn{
	{Title: "主机", ID: "host", MinWidth: 150, Sortable: true},
	{Title: "进程", ID: "process", Width: 120, Sortable: true},
	{Title: "规则", ID: "rule", Width: 132, Sortable: true},
	{Title: "链路", ID: "chain", Width: 150, Sortable: true},
	{Title: "↓ 速度", ID: "down", Width: 84, Align: ui.End, Sortable: true},
	{Title: "↑ 速度", ID: "up", Width: 84, Align: ui.End, Sortable: true},
	{Title: "↓ 总计", ID: "total", Width: 92, Align: ui.End, Sortable: true},
	{Title: "时长", ID: "dur", Width: 68, Align: ui.End, Sortable: true},
	{Title: "", ID: "close", Width: 30, Fixed: true},
}

// visibleColumns returns allColumns without the hidden ones, each wide
// enough for its title in the language shown (colwidth.Fit); read again
// only after a change of the settings or of the language.
func (p *Page) visibleColumns(c *ui.Context) []ui.TableColumn {
	stale := p.colsStale.Take()
	if !stale && p.cols != nil && p.colsLang == i18n.Lang() {
		return p.cols
	}
	p.colsLang = i18n.Lang()
	hidden := p.e.API.Settings().ConnHiddenColumns
	p.hidden = make(map[string]bool, len(hidden))
	for _, id := range hidden {
		p.hidden[id] = true
	}
	cols := make([]ui.TableColumn, 0, len(allColumns))
	for _, col := range allColumns {
		col.Title = i18n.T(col.Title)
		if !p.hidden[col.ID] || col.ID == "host" || col.Fixed {
			cols = append(cols, col)
		}
	}
	return colwidth.Fit(c, cols)
}

// headerMenu is the header's context menu: a check item per column.
func (p *Page) headerMenu(m *ui.Menu, _ int) {
	for _, col := range allColumns {
		if col.Fixed {
			continue
		}
		item := m.Item(i18n.T(col.Title)).Checked(!p.hidden[col.ID]).Disabled(col.ID == "host")
		if item.Chosen() {
			p.toggleColumn(col.ID)
		}
	}
	m.Separator()
	if m.Item(i18n.T("显示全部列")).Disabled(len(p.hidden) == 0).Chosen() {
		p.saveHidden(nil)
	}
}

// toggleColumn hides a shown column or shows a hidden one.
func (p *Page) toggleColumn(id string) {
	var hidden []string
	for _, col := range allColumns {
		if (p.hidden[col.ID]) != (col.ID == id) {
			hidden = append(hidden, col.ID)
		}
	}
	p.saveHidden(hidden)
}

// saveHidden persists the hidden columns in the settings.
func (p *Page) saveHidden(hidden []string) {
	e := p.e
	// Only this option is saved (app.EditSettings): the settings page, the
	// tray or an agent may be saving others at the same moment.
	e.Run(func() error {
		return app.EditSettings(e.API, func(set *model.Settings) { set.ConnHiddenColumns = hidden })
	}, "")
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	if a := e.Arg(env.Conns); a != "" {
		if q, ok := env.TakeQuery(a); ok { // the global search
			a = q
		}
		// The exit map opens the page filtered to one route (docs/exit-map.md §4).
		p.query, p.tab, p.paused, p.view = a, 0, false, 0
	}
	st := e.API.Status()
	running := st.Core == model.CoreRunning
	if !running {
		p.paused = false
	}
	active, closed, nActive := p.lists(st)

	subtitle := ""
	if running {
		subtitle = i18n.Tn(nActive, "%d 个活动连接", nActive)
	}
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("连接"), subtitle, func() { p.tools(c, running, nActive) })
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			p.search.Focus(c)
		}
		switch {
		case !running:
			e.Kit.Empty(c, "conns", i18n.T("内核未启动"), i18n.T("启动内核后这里会实时显示经过澜流的连接。"), nil)
		case p.view == 1:
			p.topology(c, t, active, closed)
		default:
			p.body(c, t, active, closed)
		}
	})
	p.form.View(c, e)
}

// lists returns the connection lists this view shows, reading only those
// (the list view shows one tab, the topology the active ones and, if
// asked, the closed), frozen while paused; and how many are active.
func (p *Page) lists(st model.Status) (active, closed []model.Connection, nActive int) {
	e := p.e
	needActive := p.view == 1 || p.tab == 0
	needClosed := (p.view == 1 && p.topo.withClosed) || (p.view != 1 && p.tab == 1)
	p.pausedFlag.Store(p.paused)
	// The lists change with TopicConns only: a build for anything else
	// (typing, the pointer, a scroll) shows the ones held.
	if !p.paused && p.data.Take() {
		p.frozen.active, p.frozen.closed = nil, nil
		p.frozen.hasActive, p.frozen.hasClosed = false, false
	}
	if needActive {
		if !p.frozen.hasActive {
			p.frozen.active, p.frozen.hasActive = displayChains(foldCaptureConns(e.API.Connections()), e.API.ProxyChains()), true
			p.gen++
		}
		active = p.frozen.active
	}
	if needClosed {
		if !p.frozen.hasClosed {
			p.frozen.closed, p.frozen.hasClosed = displayChains(foldCaptureConns(e.API.ClosedConnections()), e.API.ProxyChains()), true
			p.gen++
		}
		closed = p.frozen.closed
	}
	nActive = st.Connections
	if p.frozen.hasActive {
		nActive = len(p.frozen.active)
	}
	return active, closed, nActive
}

func (p *Page) tools(c *ui.Context, running bool, n int) {
	e := p.e
	// The toolbar must fit beside the title at the minimum window width
	// (docs/design.md §4), with the topology's extra controls too.
	searchWidth := float32(130)
	if env.TwoColumns(c) {
		searchWidth = 180
	}
	ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索")).Label(i18n.T("搜索连接")).Width(searchWidth).Bind(&p.search)
	pauseLabel, icon := i18n.T("暂停"), "pause"
	if p.paused {
		pauseLabel, icon = i18n.T("继续"), "play"
	}
	ui.Button(c, "").Label(pauseLabel).Disabled(!running).Children(func() {
		e.Kit.Icon(c, icon, 13)
		ui.Text(c, pauseLabel)
	}).OnClick(func() { p.paused = !p.paused })
	if p.view == 1 {
		// The topology weighs its bands by speed or by total bytes, and
		// may take the recently closed connections in (topology.go).
		ui.Segmented(c.Key("weight"), &p.topo.byTotal, i18n.T("速度"), i18n.T("流量")).Label(i18n.T("线宽依据"))
		ui.Button(c, "").Label(i18n.T("拓扑选项")).Tooltip(i18n.T("拓扑选项")).Children(func() {
			e.Kit.Icon(c, "more", 14)
		}).Menu(func(m *ui.Menu) {
			if m.Item(i18n.T("含已关闭")).Checked(p.topo.withClosed).Chosen() {
				p.topo.withClosed = !p.topo.withClosed
			}
			if m.Item(i18n.T("分端口")).Checked(p.topo.byPort).Chosen() {
				p.topo.byPort = !p.topo.byPort
			}
		})
	} else {
		ui.Segmented(c.Key("tab"), &p.tab, i18n.T("活动"), i18n.T("已关闭")).Label(i18n.T("连接列表"))
	}
	ui.Segmented(c.Key("view"), &p.view, i18n.T("列表"), i18n.T("拓扑")).Label(i18n.T("连接视图"))
	ui.Button(c, i18n.T("全部断开")).Disabled(!running || n == 0).OnClick(func() {
		e.Confirm(i18n.Tn(n, "断开全部 %d 个连接？", n), i18n.T("应用会自动重新建立需要的连接。"), i18n.T("断开"), false, func() {
			p.sel = ""
			e.Run(e.API.CloseAllConnections, i18n.T("已断开全部连接"))
		})
	})
}

func (p *Page) body(c *ui.Context, t *ui.Theme, active, closed []model.Connection) {
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
	// The detail panel follows the connection's ID, not its row.
	p.selected = -1
	for i, r := range rows {
		if r.ID == p.sel {
			p.selected = i
		}
	}
	p.hoverRow = -1
	p.list.Key = func(i int) any { return rows[i].ID }
	p.list.Label = func(i int) string { return rows[i].Host }
	p.list.CellMenu = func(m *ui.Menu, row, col int) { p.rowMenu(c, m, rows[row], p.tab == 0) }
	p.cols = p.visibleColumns(c)
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Table(c.Key("table"), &p.list, p.cols, len(rows), func(row, col int) {
			p.cell(c, t, rows[row], row, p.cols[col])
		}).Grow(1).FillHeight().Label(i18n.T("连接"))
		if p.selected >= 0 && p.selected < len(rows) {
			p.sel = rows[p.selected].ID
		} else if p.selected < 0 {
			p.sel = ""
		}
		if p.sel != "" {
			if conn, ok := find(rows, p.sel); ok {
				p.detail(c, t, conn, p.tab == 0)
			}
		}
	})
}

// cell builds one cell for column col.
func (p *Page) cell(c *ui.Context, t *ui.Theme, cn model.Connection, row int, col ui.TableColumn) {
	q := strings.TrimSpace(p.query)
	muted := t.TextMuted
	if row == p.selected {
		// Muted grey is unreadable on the chosen row's accent.
		muted = t.AccentText
	}
	box := ui.Row(c).FillWidth()
	if col.Align == ui.End {
		box.Justify(ui.End)
	}
	if box.Hovered() {
		p.hoverRow = row
	}
	box.Children(func() {
		switch col.ID {
		case "host":
			env.Highlight(c, cn.Host, q, t.Text).SingleLine().Tooltip(cn.Host)
		case "process":
			name := processDisplayName(cn)
			ui.Row(c).Gap(5).AlignItems(ui.Center).FillWidth().Children(func() {
				if cn.Process != "" {
					p.icons.Draw(c, t, cn.ProcessPath, procicon.Size)
				}
				env.Highlight(c, name, q, muted).SingleLine().Shrink(1).Tooltip(processTip(cn))
			})
		case "rule":
			env.Highlight(c, ruleName(cn), q, muted).SingleLine().Tooltip(ruleTip(cn))
		case "chain":
			env.Highlight(c, chain(cn), q, t.Text).SingleLine().Tooltip(chain(cn))
		case "down":
			speed(c, muted, cn.DownSpeed)
		case "up":
			speed(c, muted, cn.UpSpeed)
		case "total":
			ui.Text(c, env.FmtBytes(cn.Download)).FontFeatures("tnum").SingleLine()
		case "dur":
			ui.Text(c, env.FmtDuration(duration(cn))).FontFeatures("tnum").SingleLine()
		case "close":
			if p.tab != 0 {
				return
			}
			show := p.hoverRow == row || p.selected == row
			id := cn.ID
			b := ui.Button(c, "").Label(i18n.T("断开")).Tooltip(i18n.T("断开")).Padding(0, 4).Children(func() {
				ui.Text(c, "✕").FontSize(11)
			}).OnClick(func() { p.closeConn(id) })
			if !show {
				b.Invisible()
			}
		}
	})
}

func speed(c *ui.Context, muted ui.Color, v int64) {
	if v <= 0 {
		// Idle connections read as a dash, not a unit-less zero.
		ui.Text(c, "—").TextColor(muted).FontFeatures("tnum")
		return
	}
	ui.Text(c, env.FmtSpeed(v)).FontFeatures("tnum").SingleLine()
}

// rowMenu is the context menu of a row; it runs while the view builds, so
// c is the window's context.
func (p *Page) rowMenu(c *ui.Context, m *ui.Menu, cn model.Connection, active bool) {
	if active {
		if m.Item(i18n.T("断开")).Chosen() {
			p.closeConn(cn.ID)
		}
		m.Separator()
	}
	if m.Item(i18n.T("此域名始终走…")).Chosen() {
		p.addRuleForHost(cn)
	}
	if m.Item(i18n.T("此程序始终走…")).Disabled(cn.Process == "").Chosen() {
		p.addRuleForProcess(cn)
	}
	// HTTPS 选择性解密 (docs/capture.md §7.2): the 抓包 page confirms;
	// only offered while 抓包 is enabled in the settings.
	if p.e.API.Settings().CaptureEnabled {
		if h := hostOnly(cn.Host); m.Item(i18n.T("解密此主机…")).Disabled(isIP(h)).Chosen() {
			p.e.Navigate(env.Capture, "add-host:"+h)
		}
		if m.Item(i18n.T("解密此程序…")).Disabled(cn.Process == "").Chosen() {
			p.e.Navigate(env.Capture, "add-app:"+cn.ProcessPath+"\n"+cn.Process)
		}
	}
	m.Separator()
	if m.Item(i18n.T("复制主机")).Chosen() {
		c.WriteClipboard(cn.Host)
		p.e.Toast(i18n.T("已复制"), false)
	}
}

func (p *Page) detail(c *ui.Context, t *ui.Theme, cn model.Connection, active bool) {
	e := p.e
	panel := ui.Column(c.Key("detail")).FillHeight().Shrink(0).BorderWidth(0, 0, 0, 1).BorderColor(t.Border)
	panel.Children(func() {
		p.e.PaneSize(env.SizeConnsDetail, &p.detailW)
		panel.Width(env.DetailEdge(c, &p.detailW, detailWidth))
		ui.Scroll(c).Grow(1).Padding(14, 16).Gap(12).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
				ui.Text(c, cn.Host).Bold().Grow(1).Selectable()
				ui.Button(c, "").Label(i18n.T("关闭详情")).Tooltip(i18n.T("关闭")).Padding(0, 4).Children(func() {
					ui.Text(c, "✕").FontSize(11)
				}).OnClick(func() { p.sel = "" })
			})
			meta := strings.ToUpper(cn.Network)
			if cn.Type != "" {
				meta += " · " + cn.Type
			}
			if !cn.ClosedAt.IsZero() {
				meta += i18n.Tf(" · 已关闭 %s", env.FmtAgo(cn.ClosedAt))
			}
			ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted)
			ui.Column(c).Gap(6).Selectable().Children(func() {
				kv := func(k, v string) { p.kv(c, t, k, v, "") }
				src := cn.SourceIP
				if cn.SourcePort != "" {
					src += ":" + cn.SourcePort
				}
				kv(i18n.T("源地址"), src)
				kv(i18n.T("目标地址"), cn.Host)
				kv(i18n.T("目标 IP"), env.Dash(cn.DstIP))
				if cn.RemoteDestination != "" && cn.RemoteDestination != cn.DstIP {
					kv(i18n.T("实际连接"), cn.RemoteDestination)
				}
				p.kv(c, t, i18n.T("进程"), env.Dash(cn.Process), cn.ProcessPath)
				kv(i18n.T("规则"), ruleName(cn))
				kv(i18n.T("规则载荷"), env.Dash(cn.RulePayload))
				kv(i18n.T("链路"), chain(cn))
				kv(i18n.T("下行"), i18n.Tf("%s · 共 %s", env.FmtSpeed(cn.DownSpeed), env.FmtBytes(cn.Download)))
				kv(i18n.T("上行"), i18n.Tf("%s · 共 %s", env.FmtSpeed(cn.UpSpeed), env.FmtBytes(cn.Upload)))
				kv(i18n.T("时长"), env.FmtDuration(duration(cn)))
			})
			ui.Column(c).Gap(6).AlignItems(ui.Start).Children(func() {
				ui.Button(c, "").Children(func() {
					e.Kit.Icon(c, "rules", 13)
					ui.Text(c, i18n.T("此域名始终走…"))
				}).OnClick(func() { p.addRuleForHost(cn) })
				ui.Button(c, "").Disabled(cn.Process == "").Children(func() {
					e.Kit.Icon(c, "rules", 13)
					ui.Text(c, i18n.T("此程序始终走…"))
				}).OnClick(func() { p.addRuleForProcess(cn) })
				if active && cn.ClosedAt.IsZero() {
					id := cn.ID
					ui.Button(c, i18n.T("断开此连接")).OnClick(func() { p.closeConn(id) })
				}
			})
		})
	})
}

func (p *Page) kv(c *ui.Context, t *ui.Theme, k, v, sub string) {
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c, k).Width(64).FontSize(12).TextColor(t.TextMuted).Unselectable()
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, v).FontSize(12).FontFeatures("tnum")
			if sub != "" {
				ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted)
			}
		})
	})
}

func (p *Page) closeConn(id string) {
	e := p.e
	if p.sel == id {
		p.sel = ""
	}
	e.Run(func() error { return e.API.CloseConnection(id) }, "")
}

func (p *Page) addRuleForHost(cn model.Connection) {
	host := hostOnly(cn.Host)
	r := model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: ruleform.SuffixOf(host), Target: p.defaultTarget(cn)}
	if isIP(host) {
		r.Type, r.Payload = "IP-CIDR", ruleform.CIDROf(host)
	}
	p.form.Open(r)
}

func (p *Page) addRuleForProcess(cn model.Connection) {
	p.form.Open(model.CustomRule{Type: "PROCESS-NAME", Payload: cn.Process, Target: p.defaultTarget(cn)})
}

// defaultTarget prefills the rule's target with the connection's first
// hop when it is a group, else the main group.
func (p *Page) defaultTarget(cn model.Connection) string {
	chains := cn.Chains
	if cn.CaptureLeg && len(cn.CaptureReturnChains) > 0 {
		chains = cn.CaptureReturnChains
	}
	if len(chains) > 0 && chains[0] != "DIRECT" && chains[0] != "REJECT" {
		return chains[0]
	}
	return p.e.API.Proxies().MainGroup
}

// ---- helpers ----

// rows returns src filtered by the search and in the table's order. The
// result is kept until the lists, the tab, the search or the order
// change, so a build for anything else does not filter and sort again.
func (p *Page) rows(src []model.Connection) []model.Connection {
	sc := &p.sorted
	q := strings.ToLower(strings.TrimSpace(p.query))
	if sc.ok && sc.gen == p.gen && sc.tab == p.tab && sc.query == q && sc.sort == p.sort {
		return sc.rows
	}
	rows := sortRows(p.filter(src), p.sort)
	sc.ok, sc.gen, sc.tab, sc.query, sc.sort, sc.rows = true, p.gen, p.tab, q, p.sort, rows
	return rows
}

func (p *Page) filter(src []model.Connection) []model.Connection {
	q := strings.ToLower(strings.TrimSpace(p.query))
	out := make([]model.Connection, 0, len(src))
	if q == "" {
		p.hay = nil
		return append(out, src...)
	}
	// The search texts of the connections still listed carry over; those
	// of closed-and-gone connections are dropped.
	old := p.hay
	p.hay = make(map[string]hayText, len(src))
	for _, cn := range src {
		h, ok := p.hay[cn.ID]
		if !ok || !h.of(cn) {
			if h, ok = old[cn.ID]; !ok || !h.of(cn) {
				h = hayText{host: cn.Host, process: cn.Process, processPath: cn.ProcessPath, chains: len(cn.Chains), retChains: len(cn.CaptureReturnChains), text: strings.ToLower(haystack(cn))}
			}
			p.hay[cn.ID] = h
		}
		if strings.Contains(h.text, q) {
			out = append(out, cn)
		}
	}
	return out
}

// hayText is a connection's cached search text, with the fields that may
// still change during its life (the process found late, the chain
// resolved), to tell when it is out of date.
type hayText struct {
	host, process, processPath string
	chains                     int
	retChains                  int
	text                       string
}

func (h hayText) of(cn model.Connection) bool {
	return h.host == cn.Host && h.process == cn.Process && h.processPath == cn.ProcessPath && h.chains == len(cn.Chains) && h.retChains == len(cn.CaptureReturnChains)
}

// appBundleName returns the outermost macOS .app bundle name from a process
// path (e.g. "/Applications/Antigravity.app/Contents/.../language_server" ->
// "Antigravity"), or "" when path is not inside an .app bundle.
func appBundleName(path string) string {
	for _, seg := range strings.Split(path, "/") {
		if strings.HasSuffix(seg, ".app") && len(seg) > 4 {
			return strings.TrimSuffix(seg, ".app")
		}
	}
	return ""
}

// processDisplayName formats the process column: for helper binaries inside a
// macOS .app bundle, lead with the owning app name so it is immediately
// recognizable and highlights when searching by app name.
func processDisplayName(cn model.Connection) string {
	if cn.IsInner() {
		return i18n.T("链路前置")
	}
	if cn.Process == "" {
		return "—"
	}
	if app := appBundleName(cn.ProcessPath); app != "" && !strings.EqualFold(app, cn.Process) {
		return app + " · " + cn.Process
	}
	return cn.Process
}

// haystack is the text the search matches: host, process (and its path /
// owning .app bundle), rule with its payload, chain, destination IP and
// source, so a topology node's label (topology_graph.go) finds its connections.
func haystack(cn model.Connection) string {
	src := cn.SourceIP
	if cn.SourcePort != "" {
		src += ":" + cn.SourcePort
	}
	return cn.Host + " " + cn.Process + " " + cn.ProcessPath + " " + ruleName(cn) + " " + cn.RulePayload + " " + chain(cn) + " " + cn.DstIP + " " + src
}

// sortRows returns rows in the order s asks for, newest first among
// equals. Each row's key is taken once, not per comparison (the rule's
// name and the chain are built strings), and the rows are moved once.
func sortRows(rows []model.Connection, s ui.SortOrder) []model.Connection {
	type key struct {
		i     int
		text  string
		num   int64
		start time.Time
	}
	now := time.Now()
	keys := make([]key, len(rows))
	for i := range rows {
		cn := &rows[i]
		k := key{i: i, start: cn.Start}
		switch s.Column {
		case "host":
			k.text = cn.Host
		case "process":
			k.text = processDisplayName(*cn)
		case "rule":
			k.text = ruleName(*cn)
		case "chain":
			k.text = chain(*cn)
		case "up":
			k.num = cn.UpSpeed
		case "total":
			k.num = cn.Download
		case "dur":
			// One clock reading for all rows, so that two connections
			// started together compare equal.
			k.num = int64(durationAt(*cn, now))
		default: // "down"
			k.num = cn.DownSpeed
		}
		keys[i] = k
	}
	slices.SortStableFunc(keys, func(a, b key) int {
		d := cmp.Or(cmp.Compare(a.text, b.text), cmp.Compare(a.num, b.num))
		if s.Descending {
			d = -d
		}
		return cmp.Or(d, b.start.Compare(a.start)) // newest first on ties
	})
	out := make([]model.Connection, len(rows))
	for i, k := range keys {
		out[i] = rows[k.i]
	}
	return out
}

func find(rows []model.Connection, id string) (model.Connection, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return model.Connection{}, false
}

func duration(cn model.Connection) time.Duration { return durationAt(cn, time.Now()) }

// durationAt is how long cn has lasted at now, or lasted if it is closed.
func durationAt(cn model.Connection, now time.Time) time.Duration {
	end := now
	if !cn.ClosedAt.IsZero() {
		end = cn.ClosedAt
	}
	if cn.Start.IsZero() {
		return 0
	}
	return end.Sub(cn.Start)
}

// ruleName shows the rule as Mihomo's config spells it (DomainSuffix → DOMAIN-SUFFIX).
func ruleName(cn model.Connection) string {
	switch cn.Rule {
	case "":
		return "—"
	case "Match":
		return "MATCH"
	}
	var b strings.Builder
	for i, r := range cn.Rule {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(cn.Rule[i-1])
			if prev < 'A' || prev > 'Z' {
				b.WriteByte('-')
			}
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// foldCaptureConns drops the return legs of HTTPS decryption
// (lanflow-mitm-up): their outbound chains are folded onto the
// client-to-agent connections (docs/capture.md §9).
func foldCaptureConns(conns []model.Connection) []model.Connection {
	hasRet := false
	for _, cn := range conns {
		if cn.CaptureReturn {
			hasRet = true
			break
		}
	}
	if !hasRet {
		return conns
	}
	out := make([]model.Connection, 0, len(conns))
	for _, cn := range conns {
		if !cn.CaptureReturn {
			out = append(out, cn)
		}
	}
	return out
}

// displayChains names the nodes a chain generates (profile.ChainClonePrefix)
// by the nodes they clone, in copies; conns itself is left as it is (the
// service's client may hold it).
func displayChains(conns []model.Connection, chains []model.ProxyChain) []model.Connection {
	if len(chains) == 0 {
		return conns
	}
	var out []model.Connection
	mapNames := func(src []string) []string {
		names := make([]string, len(src))
		for j, n := range src {
			names[j] = profile.DisplayName(n, chains)
		}
		return names
	}
	for i, cn := range conns {
		c1 := slices.ContainsFunc(cn.Chains, profile.IsChainClone)
		c2 := slices.ContainsFunc(cn.CaptureReturnChains, profile.IsChainClone)
		if !c1 && !c2 {
			continue
		}
		if out == nil {
			out = slices.Clone(conns)
		}
		if c1 {
			out[i].Chains = mapNames(cn.Chains)
		}
		if c2 {
			out[i].CaptureReturnChains = mapNames(cn.CaptureReturnChains)
		}
	}
	if out == nil {
		return conns
	}
	return out
}

func chain(cn model.Connection) string {
	if cn.CaptureLeg && !cn.CaptureReturn {
		if len(cn.CaptureReturnChains) > 0 {
			return i18n.Tf("已解密 · 回流经 %s", strings.Join(cn.CaptureReturnChains, " › "))
		}
		return i18n.T("已解密")
	}
	if len(cn.Chains) == 0 {
		return "—"
	}
	return strings.Join(cn.Chains, " › ")
}

func hostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i > 0 && strings.Count(h, ":") == 1 {
		return h[:i]
	}
	if strings.HasPrefix(h, "[") {
		if j := strings.Index(h, "]"); j > 0 {
			return h[1:j]
		}
	}
	return h
}

// isIP reports whether h, a host without its port, is an IP address
// (bracketed or not) rather than a name: "db.de" and "cafe.be" are names.
func isIP(h string) bool {
	_, err := netip.ParseAddr(strings.Trim(h, "[]"))
	return err == nil
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicConns, app.TopicProxies, app.TopicSettings, app.TopicProcIcons}
}

// Wants drops the connection stream while the page is paused: it shows
// the lists it holds, so there is nothing to redraw for (the shell's
// topic gate).
func (p *Page) Wants(t app.Topic) bool {
	return t != app.TopicConns || !p.pausedFlag.Load()
}

// processTip is the 进程 cell's tooltip: the name with the full path.
func processTip(cn model.Connection) string {
	if cn.IsInner() {
		return i18n.T("链式代理的前置跳：澜流与内核为下一跳建立的连接，流量已计入它所服务的连接")
	}
	if cn.ProcessPath == "" {
		return cn.Process
	}
	return cn.Process + "\n" + cn.ProcessPath
}

// ruleTip is the 规则 cell's tooltip: the rule with its payload.
func ruleTip(cn model.Connection) string {
	if cn.RulePayload == "" {
		return ruleName(cn)
	}
	return ruleName(cn) + " " + cn.RulePayload
}
