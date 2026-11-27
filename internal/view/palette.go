package view

import (
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/hotkey"
	"lanflow/internal/view/settings"
)

// The global search (⌘K / Ctrl+K, docs/design.md §9): one field that
// finds every page, the actions of the tray and the shortcuts, every
// section and row of the settings, the proxy groups and nodes, the
// subscriptions and custom rules, and opens the pages with a search
// field filtered by what was typed. Enter (or a click) runs the item
// highlighted; ↑ ↓ move.

// Result groups, in the order shown.
const (
	palPages = iota
	palActions
	palSettings
	palGroups
	palNodes
	palSubs
	palRules
	palSearchIn
)

var palGroupNames = []string{"页面", "操作|搜索", "设置", "代理组", "节点|搜索", "订阅", "自定义规则", "在页面中搜索"}

// palPerGroup caps each group's results while a query is typed.
const palPerGroup = 8

type palItem struct {
	group int
	icon  string // icons.go name, "" = none
	title string
	sub   string // muted, after the title
	match string // lower-case: what a query is matched against
	run   func()
	score int
}

type palette struct {
	open  bool
	query string
	sel   int
	moved bool      // sel changed by the keys: bring it into view
	shown []palItem // the results of the last frame, for Enter
	enter bool
}

// OpenSearch opens the global search (the ⌘K shortcut, the sidebar's
// search button, the tray).
func (s *Shell) OpenSearch() {
	s.pal = palette{open: true}
	s.Redraw()
}

// palItems builds every item for the current state.
func (s *Shell) palItems() []palItem {
	api := s.api
	st := api.Status()
	var out []palItem
	add := func(group int, icon, title, sub, match string, run func()) {
		out = append(out, palItem{group: group, icon: icon, title: title, sub: sub,
			match: strings.ToLower(title + " " + sub + " " + match), run: run})
	}

	// Pages: 抓包 only while it is enabled, as in the sidebar.
	captureShown := captureOn(api)
	for _, p := range env.Order {
		if p == env.Capture && !captureShown {
			continue
		}
		page := p
		add(palPages, pageIcons[string(p)], env.Title(p), "", string(p)+" "+i18n.Both(pageTitleKeys[p]), func() { s.navigate(page, "") })
	}

	// Actions: the shortcuts' (keys.go), then the rest.
	for _, a := range model.ShortcutActions {
		if a == model.ShortcutWindow {
			continue // the window is open
		}
		action := a
		add(palActions, "bolt", hotkey.ActionName(a), hotkey.ActionDesc(a), a, func() { s.keys.run(action) })
	}
	job := func(icon, key, okMsg, match string, fn func() error) {
		add(palActions, icon, i18n.T(key), "", i18n.Both(key)+" "+match, func() { s.run(fn, okMsg) })
	}
	running := st.Core == model.CoreRunning
	if running {
		job("refresh", "重启内核", i18n.T("内核已重启"), "restart core mihomo", api.RestartCore)
		job("power", "停止内核", i18n.T("内核已停止"), "stop core mihomo", api.StopCore)
		job("bolt", "测试全部节点延迟", i18n.T("测速完成"), "delay latency test all", api.TestAllDelays)
	} else {
		job("play", "启动内核", i18n.T("内核已启动"), "start core mihomo", api.StartCore)
	}
	if st.HasSubscriptions {
		job("refresh", "更新全部订阅", i18n.T("订阅已更新"), "update subscriptions", api.UpdateAllSubscriptions)
	}
	add(palActions, "plus", i18n.T("导入订阅链接"), "", i18n.Both("导入订阅链接")+" import subscription url", func() { s.navigate(env.Subs, "import-url") })
	add(palActions, "doc", i18n.T("从文件导入订阅"), "", i18n.Both("从文件导入订阅")+" import subscription file yaml", func() { s.navigate(env.Subs, "import-file") })
	if running {
		add(palActions, "close", i18n.T("关闭全部连接"), "", i18n.Both("关闭全部连接")+" close connections", func() {
			s.ask(i18n.T("关闭全部连接？"), i18n.T("正在进行的下载、通话等会中断，应用会重新建立连接。"), i18n.T("关闭全部"), true, func() {
				s.run(api.CloseAllConnections, i18n.T("已关闭全部连接"))
			})
		})
		job("refresh", "清空 DNS 缓存", i18n.T("DNS 缓存已清空"), "flush dns cache", api.FlushDNSCache)
		job("refresh", "清空 Fake-IP 缓存", i18n.T("Fake-IP 缓存已清空"), "flush fake-ip cache", api.FlushFakeIPCache)
		add(palActions, "probe", i18n.T("运行泄漏检查"), "", i18n.Both("运行泄漏检查")+" dns ipv6 webrtc leak", func() {
			s.navigate(env.Probe, "")
			s.run(api.RunLeakCheck, "")
		})
		add(palActions, "net", i18n.T("检测网站连通性"), "", i18n.Both("检测网站连通性")+" sites connectivity", func() {
			s.navigate(env.Net, "")
			s.run(api.TestSites, "")
		})
	}
	add(palActions, "doc", i18n.T("清空日志"), "", i18n.Both("清空日志")+" clear logs", func() { api.ClearLogs(); s.toast(env.Note{Title: i18n.T("日志已清空")}) })
	job("down", "更新地理数据库", i18n.T("地理数据库已更新"), "geoip geosite update", api.UpdateGeoData)
	add(palActions, "doc", i18n.T("查看运行时配置"), "", i18n.Both("查看运行时配置")+" runtime config yaml", func() { s.navigate(env.Settings, "override#"+i18n.T("运行时配置")) })
	add(palActions, "info", i18n.T("反馈问题"), "", i18n.Both("反馈问题")+" feedback bug report", func() { s.OpenFeedback("") })

	// Settings.
	for _, e := range settings.SearchIndex() {
		entry := e
		sub := e.TabName
		if e.Section != "" && e.Section != e.TabName {
			sub += " › " + e.Section
		}
		icon := "settings"
		add(palSettings, icon, e.Label, sub, e.Match, func() { s.navigate(env.Settings, entry.Arg()) })
	}

	// Proxy groups and nodes.
	px := api.Proxies()
	if len(px.Groups) > 0 {
		main, _ := mainGroup(px, st)
		inMain := map[string]bool{}
		if main.Type == model.GroupSelect {
			for _, n := range main.All {
				inMain[n] = true
			}
		}
		groups := map[string]bool{}
		for _, g := range px.Groups {
			groups[g.Name] = true
			if g.Hidden || (g.Name == "GLOBAL" && st.Mode != model.ModeGlobal) {
				continue
			}
			name := g.Name
			sub := string(g.Type)
			if g.Now != "" {
				sub += " · " + g.Now
			}
			add(palGroups, "proxies", g.Name, sub, "group", func() { s.navigate(env.Proxies, name) })
		}
		names := make([]string, 0, len(px.Proxies))
		for n := range px.Proxies {
			if !groups[n] && n != "DIRECT" && n != "REJECT" && n != "REJECT-DROP" && n != "PASS" && n != "COMPATIBLE" {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			p := px.Proxies[n]
			name := n
			sub := p.Type
			if p.Delay > 0 {
				sub += " · " + strconv.Itoa(p.Delay) + " ms"
			}
			if inMain[n] && main.Now != n {
				group := main.Name
				sub += " · " + i18n.Tf("切换「%s」到此节点", group)
				add(palNodes, "proxies", n, sub, p.Provider, func() {
					s.run(func() error { return api.SelectProxy(group, name) }, i18n.Tf("已切换到 %s", name))
				})
				continue
			}
			if main.Now == n {
				sub += " · " + i18n.T("当前节点")
			}
			add(palNodes, "proxies", n, sub, p.Provider, func() { s.navigate(env.Proxies, env.QueryArg(name)) })
		}
	}

	// Subscriptions.
	for _, sub := range api.Subscriptions() {
		id, name := sub.ID, sub.Name
		if sub.Current {
			add(palSubs, "subs", sub.Name, i18n.T("当前订阅"), "subscription", func() { s.navigate(env.Subs, "") })
			continue
		}
		add(palSubs, "subs", sub.Name, i18n.T("切换到此订阅"), "subscription", func() {
			s.run(func() error { return api.UseSubscription(id) }, i18n.Tf("已切换到订阅「%s」", name))
		})
	}

	// Custom rules.
	for _, r := range api.CustomRules() {
		payload := r.Payload
		add(palRules, "rules", r.Type+","+r.Payload, "→ "+r.Target, "rule", func() { s.navigate(env.Rules, env.QueryArg(payload)) })
	}
	return out
}

// captureOn reports whether the 抓包 page is shown (the sidebar's rule).
func captureOn(api app.API) bool { return api.Settings().CaptureEnabled || api.CaptureStatus().Enabled }

// pageTitleKeys are the i18n keys of env.Title, for matching the page
// names in both languages.
var pageTitleKeys = map[env.Page]string{env.Overview: "概览", env.Proxies: "代理", env.Subs: "订阅", env.Conns: "连接",
	env.Logs: "日志", env.Rules: "规则|页", env.Net: "网络", env.Stats: "统计", env.Probe: "检测", env.Capture: "抓包", env.Aggregate: "聚合订阅", env.Settings: "设置"}

// searchInItems are the "在…中搜索" items for query: the pages that have
// a search field, opened filtered by it.
func (s *Shell) searchInItems(query string) []palItem {
	pages := []env.Page{env.Proxies, env.Conns, env.Logs, env.Rules, env.Stats}
	if captureOn(s.api) {
		pages = append(pages, env.Capture)
	}
	out := make([]palItem, 0, len(pages))
	for _, p := range pages {
		page := p
		out = append(out, palItem{group: palSearchIn, icon: "search", title: i18n.Tf("在「%s」中搜索「%s」", env.Title(p), query),
			run: func() { s.navigate(page, env.QueryArg(query)) }})
	}
	return out
}

// palFilter returns the items matching query, best first within each
// group and at most palPerGroup of each; an empty query keeps the pages
// and actions.
func palFilter(items []palItem, query string) []palItem {
	q := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(q)
	var out []palItem
	for _, it := range items {
		if len(words) == 0 {
			if it.group == palPages || it.group == palActions {
				out = append(out, it)
			}
			continue
		}
		ok := true
		for _, w := range words {
			if !strings.Contains(it.match, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		title := strings.ToLower(it.title)
		switch {
		case title == q:
			it.score = 4
		case strings.HasPrefix(title, q):
			it.score = 3
		case strings.Contains(title, q):
			it.score = 2
		default:
			it.score = 1
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].group != out[j].group {
			return out[i].group < out[j].group
		}
		return out[i].score > out[j].score
	})
	if len(words) == 0 {
		return out
	}
	// Groups whose best match is the title itself come first, so that a
	// node or setting typed in full is the default (Enter) item.
	capped := out[:0:0]
	count := map[int]int{}
	for _, it := range out {
		if count[it.group] < palPerGroup {
			count[it.group]++
			capped = append(capped, it)
		}
	}
	best := map[int]int{}
	for _, it := range capped {
		best[it.group] = max(best[it.group], it.score)
	}
	sort.SliceStable(capped, func(i, j int) bool {
		bi, bj := best[capped[i].group], best[capped[j].group]
		if bi != bj {
			return bi > bj
		}
		return false
	})
	return capped
}

// paletteDialog draws the global search while it is open.
func (s *Shell) paletteDialog(c *ui.Context) {
	if !s.pal.open {
		return
	}
	t := c.Theme()
	k := s.env.Kit
	shown := palFilter(s.palItems(), s.pal.query)
	if q := strings.TrimSpace(s.pal.query); q != "" {
		shown = append(shown, s.searchInItems(q)...)
	}
	if s.pal.sel >= len(shown) {
		s.pal.sel = max(len(shown)-1, 0)
	}
	s.pal.shown = shown

	ui.Modal(c, &s.pal.open, func() {
		ui.Column(c).Width(env.DialogWidth(c, 560)).Gap(8).MinHeight(0).Shrink(1).Children(func() {
			field := ui.SearchField(c.Key("pal-q"), &s.pal.query).
				Placeholder(i18n.T("搜索页面、操作、设置、节点、订阅…")).Label(i18n.T("全局搜索")).AutoFocus()
			// ↑ ↓ move the highlight; the field leaves them (one line).
			if field.Shortcut(0, ui.KeyDown) {
				s.pal.sel = min(s.pal.sel+1, max(len(shown)-1, 0))
				s.pal.moved = true
			}
			if field.Shortcut(0, ui.KeyUp) {
				s.pal.sel = max(s.pal.sel-1, 0)
				s.pal.moved = true
			}
			if field.Changed() {
				s.pal.sel = 0
			}
			if field.Submitted() {
				s.pal.enter = true
			}
			list := ui.Scroll(c.Key("pal-list")).MaxHeight(env.DialogMaxHeight(c, 90)).MinHeight(0).Shrink(1)
			list.Children(func() {
				if len(shown) == 0 {
					k.Empty(c, "search", i18n.T("没有找到匹配的结果"), "", nil)
					return
				}
				last := -1
				for i, it := range shown {
					if it.group != last {
						last = it.group
						ui.Text(c, i18n.T(palGroupNames[it.group])).FontSize(11).FontWeight(600).TextColor(t.TextMuted).
							Padding(8, 10, 2, 10)
					}
					s.palRow(c, t, i, it)
				}
			})
			ui.Text(c, i18n.T("↑↓ 选择 · ↩ 打开 · Esc 关闭")).FontSize(11).TextColor(t.TextMuted).Padding(0, 4)
		})
	})
	s.pal.moved = false
	if s.pal.enter {
		s.pal.enter = false
		if s.pal.sel < len(shown) {
			s.palRun(shown[s.pal.sel])
		}
	}
}

func (s *Shell) palRow(c *ui.Context, t *ui.Theme, i int, it palItem) {
	k := s.env.Kit
	row := env.ClickableRow(c.Key("pal:"+strconv.Itoa(it.group)+":"+it.title+"|"+it.sub), it.title).
		Gap(10).Padding(7, 10).Radius(8).AlignItems(ui.Center)
	selected := i == s.pal.sel
	if selected {
		row.Background(t.Accent.Alpha(0.16))
		if s.pal.moved {
			row.ScrollIntoView()
		}
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		if it.icon != "" {
			ui.Box(c).Shrink(0).TextColor(t.TextMuted).Children(func() { k.Icon(c, it.icon, 15) })
		}
		ui.Text(c, it.title).FontSize(13).SingleLine().Shrink(1).MinWidth(0)
		if it.sub != "" {
			ui.Text(c, it.sub).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
		}
	})
	if row.Clicked() {
		s.palRun(it)
	}
}

// palRun closes the search and runs it.
func (s *Shell) palRun(it palItem) {
	s.pal.open = false
	s.pal.query = ""
	if it.run != nil {
		it.run()
	}
	s.Redraw()
}
