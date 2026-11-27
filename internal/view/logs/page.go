// Package logs is the 日志 page (docs/design.md §5.5): the core's log, or
// Lanflow's own run log, in a virtualized monospace list with level filter,
// search, pause, clear and follow-bottom; its ⋯ menu also exports the
// diagnostics bundle.
package logs

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/colwidth"
	"lanflow/internal/view/env"
)

var (
	levels     = []string{"debug", "info", "warning", "error"}
	levelNames = map[string]string{"debug": "调试", "info": "信息", "warning": "警告", "error": "错误"}
	levelRank  = map[string]int{"debug": 0, "info": 1, "warning": 2, "error": 3}
)

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query    string
	search   ui.Handle // the search field, focused by ⌘F
	level    int       // index into levels of the lowest level shown
	paused   bool
	frozen   []model.LogEntry
	list     ui.ListState
	menuOpen bool
	// track numbers the entries of each source (0 core, 1 app), so a line
	// keeps its identity as older ones leave the full buffer; endSeq is
	// the number after the last entry there was when the list last
	// showed its end.
	// showed its end, per source.
	track  [2]tracker
	endSeq [2]uint64
	// vis caches which entries the level filter and the search show.
	vis    visCache
	source int  // 0 = the core's log, 1 = Lanflow's own
	loaded bool // level/source restored from API.UIState
	// wrap breaks long lines over several rows instead of cutting them
	// at the window's edge (⋯ 自动换行).
	wrap bool
}

// isApp reports whether Lanflow's own log is shown.
func (p *Page) isApp() bool { return p.source == 1 }

// entries returns the log of the chosen source.
func (p *Page) entries() []model.LogEntry {
	if p.isApp() {
		return p.e.API.AppLogs()
	}
	return p.e.API.Logs()
}

// tracker numbers the entries of a log that only gives its newest ones
// (API.Logs: at most 1000, oldest first, no sequence numbers): base is the
// number of all[0], so all[i] is entry base+i for as long as it is held.
// It finds how many entries left the front by lining the previous
// snapshot up with the new one.
type tracker struct {
	prev []model.LogEntry
	base uint64
}

func sameEntry(a, b model.LogEntry) bool {
	return a.Time.Equal(b.Time) && a.Level == b.Level && a.Payload == b.Payload
}

// follow takes the log as it is now and returns the number of all[0].
// When no part of the previous snapshot is found at the start of all (the
// log was cleared, or everything held was replaced), the numbers go on
// after the previous ones, so that none is used twice.
func (k *tracker) follow(all []model.LogEntry) uint64 {
	prev := k.prev
	k.prev = all
	if len(prev) == 0 {
		return k.base
	}
	// Most builds find the log as it was (a scroll, the pointer): the same
	// length between the same two ends is taken as that, without going
	// through every entry.
	if n := len(all); n == len(prev) && sameEntry(prev[0], all[0]) && sameEntry(prev[n-1], all[n-1]) {
		return k.base
	}
	// The smallest shift d for which prev[d:] is the start of all.
	for d := 0; d < len(prev); d++ {
		rest := prev[d:]
		if len(rest) > len(all) || !sameEntry(rest[0], all[0]) || !sameEntry(rest[len(rest)-1], all[len(rest)-1]) {
			continue
		}
		same := true
		for i := range rest {
			if !sameEntry(rest[i], all[i]) {
				same = false
				break
			}
		}
		if same {
			k.base += uint64(d)
			return k.base
		}
	}
	k.base += uint64(len(prev))
	return k.base
}

// visCache is the entries shown, as indexes into the log, for the log,
// level and search it was made for.
type visCache struct {
	ok          bool
	source      int
	base        uint64
	n           int
	level       int
	query       string
	first, last model.LogEntry
	idx         []int
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, level: levelRank["info"]} // 级别：信息 by default (docs/design.md §5.5)
	p.list.FollowEnd = true
	return p
}

// minLevel returns the lowest level shown.
func (p *Page) minLevel() int { return p.level }

// restore loads the remembered level filter, source and line wrapping
// once (model.UIState.LogLevel / LogSource / LogWrap).
func (p *Page) restore() {
	if p.loaded {
		return
	}
	p.loaded = true
	u := p.e.API.UIState()
	if r, ok := levelRank[u.LogLevel]; ok {
		p.level = r
	}
	if u.LogSource == "app" {
		p.source = 1
	}
	p.wrap = u.LogWrap
}

// persist remembers the level filter, source and line wrapping off the
// UI thread, keeping what other pages store in the UI state.
func (p *Page) persist() {
	level, source := levels[min(p.level, len(levels)-1)], "core"
	if p.isApp() {
		source = "app"
	}
	wrap := p.wrap
	p.e.SaveUIState("logs", func(u *model.UIState) {
		u.LogLevel, u.LogSource, u.LogWrap = level, source, wrap
	})
}

// levelOptions are the level select's texts, 「级别：信息」, in the language
// of the frame.
func levelOptions() []string {
	opts := make([]string, len(levels))
	for i, l := range levels {
		opts[i] = i18n.Tf("级别：%s", i18n.T(levelNames[l]))
	}
	return opts
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	p.restore()
	if q, ok := env.TakeQuery(e.Arg(env.Logs)); ok { // the global search
		p.query = q
	}
	// Paused, the log is neither read nor numbered again: the frozen copy
	// and its numbers stand.
	var all []model.LogEntry
	if p.paused && p.frozen != nil {
		all = p.frozen
	} else {
		all = p.entries()
		p.track[p.source].follow(all)
		p.frozen = nil
		if p.paused {
			p.frozen = all
			if p.frozen == nil {
				p.frozen = []model.LogEntry{}
			}
		}
	}
	base := p.track[p.source].base
	vis := p.visible(all, base)

	ui.Column(c).Fill().Children(func() {
		if e.Phone {
			p.phoneTools(c, all)
		} else {
			e.Kit.Header(c, i18n.T("日志"), "", func() { p.tools(c, all) })
		}
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			p.search.Focus(c)
		}
		ui.Box(c).Grow(1).Children(func() {
			if len(vis) == 0 {
				switch {
				case strings.TrimSpace(p.query) != "":
					e.Kit.Empty(c, "search", i18n.Tf("没有匹配“%s”的日志", strings.TrimSpace(p.query)), "", nil)
				case p.paused:
					e.Kit.Empty(c, "logs", i18n.T("暂无日志"), i18n.T("已暂停。按「继续」恢复接收新日志。"), nil)
				case p.isApp():
					e.Kit.Empty(c, "logs", i18n.T("暂无日志"), i18n.T("澜流自身的操作和错误会记录在这里。"), nil)
				default:
					e.Kit.Empty(c, "logs", i18n.T("暂无日志"), i18n.T("内核运行时，连接、DNS 和错误记录会实时显示在这里；可用「级别」缩小范围。"), nil)
				}
				return
			}
			// A line is its entry, not its row: when the buffer is full
			// every new entry drops the oldest, and the list stays on the
			// lines being read instead of sliding under them.
			p.list.Key = func(i int) any { return base + uint64(vis[i]) }
			p.list.Label = func(i int) string { return all[vis[i]].Payload }
			levelW := float32(0)
			if !e.Phone {
				levelW = levelWidth(c)
			}
			if e.Phone {
				// Two lines an entry, wrapped: no sideways scrolling.
				ui.List(c.Key("list"), &p.list, len(vis), func(i int) {
					p.phoneLine(c, t, all[vis[i]])
				}).Fill().Font("monospace").FontSize(12).Selectable().Label(i18n.T("日志"))
			} else if p.wrap {
				ui.List(c.Key("list"), &p.list, len(vis), func(i int) {
					p.line(c, t, all[vis[i]], levelW)
				}).Fill().Font("monospace").FontSize(12).Selectable().KeepScrollbars(true).Label(i18n.T("日志"))
			} else {
				hs := ui.ScrollHorizontal(c.Key("hscroll")).Fill().KeepScrollbars(true)
				listW := max(hs.Bounds().W, p.contentWidth(c, all, vis, levelW))
				hs.Children(func() {
					ui.List(c.Key("list"), &p.list, len(vis), func(i int) {
						p.line(c, t, all[vis[i]], levelW)
					}).Width(listW).FillHeight().Font("monospace").FontSize(12).Selectable().KeepScrollbars(true).Label(i18n.T("日志"))
				})
			}
			atEnd := p.list.AtEnd() || len(vis) == 0
			end := base + uint64(len(all))
			if atEnd {
				p.endSeq[p.source] = end
			}
			if !atEnd {
				label := i18n.T("回到底部")
				if end > p.endSeq[p.source] {
					label = i18n.T("回到底部（有新日志）")
				}
				ui.Button(c, "").Children(func() {
					e.Kit.Icon(c, "down", 13)
					ui.Text(c, label)
				}).Radius(999).Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.18)).
					Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(14).
					OnClick(func() { p.list.ScrollToEnd() })
			}
		})
	})
}

func (p *Page) tools(c *ui.Context, all []model.LogEntry) {
	e := p.e
	if ui.Segmented(c.Key("source"), &p.source, i18n.T("内核"), i18n.T("澜流")).Label(i18n.T("日志来源")).
		Tooltip(i18n.T("内核：Mihomo 的运行日志；澜流：澜流自己的操作记录、提示和错误")).Changed() {
		p.frozen, p.paused = nil, false
		p.list.ScrollToEnd()
		p.persist()
	}
	ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索")).Label(i18n.T("搜索日志")).Width(180).Bind(&p.search)
	opts := levelOptions()
	cur := opts[min(p.level, len(opts)-1)]
	if ui.Select(c.Key("level"), &cur, opts).Label(i18n.T("级别")).Changed() {
		for i, o := range opts {
			if o == cur {
				p.level = i
			}
		}
		p.persist()
	}
	pauseLabel, icon := i18n.T("暂停"), "pause"
	if p.paused {
		pauseLabel, icon = i18n.T("继续"), "play"
	}
	ui.Button(c, "").Label(pauseLabel).Children(func() {
		e.Kit.Icon(c, icon, 13)
		ui.Text(c, pauseLabel)
	}).OnClick(func() { p.paused = !p.paused })
	ui.Button(c, i18n.T("清空")).Disabled(len(all) == 0).OnClick(func() { p.clear() })
	reveal := i18n.T("在资源管理器中显示日志文件")
	if e.Mac {
		reveal = i18n.T("在访达中显示日志文件")
	}
	ui.Button(c, "⋯").Label(i18n.T("更多")).Tooltip(i18n.T("更多")).PopoverMenuButton(func(m *ui.Menu) {
		// 复制全部 copies every line held, whatever the level filter and
		// the search show (docs/design.md §5.5 ⋯ 菜单).
		if m.Item(i18n.T("复制全部")).Disabled(len(all) == 0).Chosen() {
			c.WriteClipboard(text(all))
			e.Toast(i18n.T("已复制"), false)
		}
		if m.Item(i18n.T("自动换行")).Checked(p.wrap).Chosen() {
			p.wrap = !p.wrap
			p.persist()
		}
		if m.Item(reveal).Chosen() {
			name := logFileName
			if p.isApp() {
				name = appLogFileName
			}
			revealLogFile(e.API.LogDir(), name)
		}
		m.Separator()
		if m.Item(i18n.T("导出诊断信息…")).Chosen() {
			go ExportDiagnostics(e)
		}
	}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd, Open: &p.menuOpen})
}

// clear empties the log shown.
func (p *Page) clear() {
	e := p.e
	p.frozen = nil
	app := p.isApp()
	e.Run(func() error {
		if app {
			e.API.ClearAppLogs()
		} else {
			e.API.ClearLogs()
		}
		return nil
	}, "")
}

// logFileName is the core's log file under LogDir (internal/app/service.go),
// appLogFileName Lanflow's own (internal/applog).
const (
	logFileName    = "mihomo.log"
	appLogFileName = "lanflow.log"
)

// ExportDiagnostics asks where to save the diagnostics zip and writes it.
// The system dialog blocks, so call it on its own goroutine.
func ExportDiagnostics(e *env.Env) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       i18n.T("导出诊断信息"),
		Message:     i18n.T("包含日志、运行时配置和网络状态；节点地址、密码和订阅令牌已去除。"),
		DefaultPath: "lanflow-diagnostics-" + time.Now().Format("20060102-1504") + ".zip",
		Filters:     []mygo.FileFilter{{Name: i18n.T("ZIP 压缩包"), Extensions: []string{"zip"}}},
	})
	if err != nil || path == "" {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".zip"
	}
	e.Run(func() error {
		if err := e.API.ExportDiagnostics(path); err != nil {
			return err
		}
		env.RevealInFolder(path)
		return nil
	}, i18n.Tf("已导出到 %s", filepath.Base(path)))
}

// revealLogFile selects the log file in the file manager; before it has
// ever been written, it opens the log folder instead.
func revealLogFile(dir, name string) {
	env.RevealInFolder(filepath.Join(dir, name))
}

// visible returns the indexes in all of the entries the level filter and
// the search show. base numbers all[0] (tracker): while the log, the level
// and the search are the same, the answer of the last build is returned.
func (p *Page) visible(all []model.LogEntry, base uint64) []int {
	min := p.minLevel()
	q := strings.ToLower(strings.TrimSpace(p.query))
	vc := &p.vis
	if vc.ok && vc.source == p.source && vc.base == base && vc.n == len(all) && vc.level == min && vc.query == q &&
		(len(all) == 0 || (sameEntry(vc.first, all[0]) && sameEntry(vc.last, all[len(all)-1]))) {
		return vc.idx
	}
	out := make([]int, 0, len(all))
	for i, l := range all {
		if levelRank[l.Level] < min {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l.Payload), q) {
			continue
		}
		out = append(out, i)
	}
	*vc = visCache{ok: true, source: p.source, base: base, n: len(all), level: min, query: q, idx: out}
	if len(all) > 0 {
		vc.first, vc.last = all[0], all[len(all)-1]
	}
	return out
}

// text renders lines as the list shows them, one per line.
func text(lines []model.LogEntry) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s %s %s\n", l.Time.Format("15:04:05"), levelName(l.Level), l.Payload)
	}
	return b.String()
}

func levelName(l string) string {
	if n, ok := levelNames[l]; ok {
		return i18n.T(n)
	}
	return l
}

// lineColors returns the text colour of a line and of its time column:
// a warning is orange and an error red as a whole line, time included;
// other lines keep the default colour with a muted time (docs/design.md
// §5.5 级别样式).
func lineColors(level string, t *ui.Theme) (text, time ui.Color) {
	switch level {
	case "warning":
		return t.Warning, t.Warning
	case "error":
		return t.Danger, t.Danger
	}
	return t.Text, t.TextMuted
}

func (p *Page) line(c *ui.Context, t *ui.Theme, l model.LogEntry, levelW float32) {
	color, timeColor := lineColors(l.Level, t)
	row := ui.Row(c).MinHeight(22).Gap(10).PaddingX(16)
	if p.wrap {
		row.AlignItems(ui.Start).PaddingY(3)
	} else {
		row.AlignItems(ui.Center)
	}
	row.Children(func() {
		ui.Text(c, l.Time.Format("15:04:05")).TextColor(timeColor).FontFeatures("tnum").Unselectable().Shrink(0)
		ui.Text(c, levelName(l.Level)).Width(levelW).TextColor(color).Unselectable().Shrink(0).SingleLine()
		msg := env.Highlight(c, l.Payload, strings.TrimSpace(p.query), color)
		if p.wrap {
			msg.Grow(1).Shrink(1).MinWidth(0)
		} else {
			msg.SingleLine().Shrink(0)
		}
	})
}

// contentWidth returns the DIP width needed so the widest visible log line
// renders without truncation inside the horizontal scroll container.
func (p *Page) contentWidth(c *ui.Context, all []model.LogEntry, vis []int, levelW float32) float32 {
	var longest string
	longestBytes := 0
	for _, idx := range vis {
		s := all[idx].Payload
		if len(s) > longestBytes {
			longestBytes = len(s)
			longest = s
		}
	}
	timeW := colwidth.Text(c, "monospace", 12, 0, "00:00:00")
	msgW := colwidth.Text(c, "monospace", 12, 0, longest)
	// paddingX(16*2) + 2*gap(10) + scrollbar room(16)
	return timeW + levelW + msgW + 68
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicLogs}
}

// levelWidth is the width of the level column: that of the widest level
// name in the language shown, measured in the list's font (monospace
// resolves to another face on each platform, so no fixed width fits
// "Warning" everywhere).
func levelWidth(c *ui.Context) float32 {
	names := make([]string, len(levels))
	for i, l := range levels {
		names[i] = levelName(l)
	}
	return colwidth.Text(c, "monospace", 12, 2, names...)
}
