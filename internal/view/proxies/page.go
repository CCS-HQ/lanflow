// Package proxies is the 代理 page (docs/design.md §5.2): collapsible
// proxy groups, node picking for select groups, delay tests, search and
// sort.
package proxies

import (
	"fmt"
	"image"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

// Sort orders.
const (
	sortDefault = iota
	sortDelay
	sortName
)

// Views of a group's nodes.
const (
	viewList = iota
	viewGrid
)

// gridColumns is the compact grid's column count before its width is
// known (docs/design.md §5.2: 3–4 列); gridColumnsFor then fits as many
// tiles of at least gridTileWidth as the width takes, from minGridColumns
// to maxGridColumns, so a wide window shows more columns rather than
// stretched tiles, and a narrow one keeps node names legible.
const (
	gridColumns    = 2
	gridTileWidth  = 260
	minGridColumns = 2
	maxGridColumns = 6
	// gridChrome is what the window's width loses before the grid: the
	// sidebar, the page's and the group card's padding.
	gridChrome = 290
)

// gridColumnsFor is the column count for a grid w DIPs wide (0: unknown).
func gridColumnsFor(w float32) int {
	if w <= 0 {
		return gridColumns
	}
	return min(max(int(w/gridTileWidth), minGridColumns), maxGridColumns)
}

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query       string
	search      ui.Handle
	sort        int
	hideTimeout bool
	view        int                       // viewList or viewGrid, remembered
	open        map[string]bool           // group expanded state, remembered across restarts
	openInit    map[string]bool           // groups whose default state was decided
	loaded      bool                      // open/view restored from API.UIState
	provOpen    bool                      // 代理集合 section expanded
	scrollTo    string                    // group to reveal after Navigate
	locate       locateState               // 定位当前节点 (locate.go)
	manual       manualNodeDialog          // 添加 / 编辑手动节点 (manual_dialog.go)
	manualByName map[string]model.ManualNode
	icons        map[string]ui.ImageSource // decoded group icons by reference (locate.go)
	iconFail    map[string]bool           // references that do not decode
	lists       map[string]*ui.ListState  // the virtual lists of large groups, by group

	// idle is set by View when no node of the snapshot it draws is being
	// tested: no group then walks its members to look for one (group).
	idle bool

	// decode runs a group icon's decoding off the UI thread; tests make
	// it synchronous.
	decode func(fn func())

	mu      sync.Mutex
	pending map[string]string // optimistic selection by group
	testing bool              // 全部测速 running
	// iconBusy are the icon references being decoded; iconDone what the
	// decoding left for the view to take: nil for bytes that are no
	// picture or too large a one.
	iconBusy map[string]bool
	iconDone map[string]image.Image
}

// New returns the page.
func New(e *env.Env) *Page {
	return &Page{e: e, view: viewGrid, open: map[string]bool{}, openInit: map[string]bool{}, pending: map[string]string{},
		icons: map[string]ui.ImageSource{}, iconFail: map[string]bool{}, lists: map[string]*ui.ListState{},
		iconBusy: map[string]bool{}, iconDone: map[string]image.Image{}, decode: func(fn func()) { go fn() }}
}

// restore loads the remembered expanded states and view once.
func (p *Page) restore() {
	if p.loaded {
		return
	}
	p.loaded = true
	u := p.e.API.UIState()
	for name, open := range u.OpenGroups {
		p.open[name] = open
		p.openInit[name] = true
	}
	if u.ProxyGrid {
		p.view = viewGrid
	}
	p.sort = sortFromKey(u.ProxySort)
	p.hideTimeout = u.ProxyHideTimeout
	p.provOpen = u.ProxyProvidersOpen
}

// sortKeys are the stored names of the sort orders, by sort constant.
var sortKeys = [...]string{sortDefault: "", sortDelay: "delay", sortName: "name"}

func sortFromKey(k string) int {
	for i, v := range sortKeys {
		if v == k {
			return i
		}
	}
	return sortDefault
}

// persist saves the expanded states and view off the UI thread, keeping
// what other pages remember in the UI state.
func (p *Page) persist() {
	open := make(map[string]bool, len(p.open))
	for name, o := range p.open {
		open[name] = o
	}
	grid := p.view == viewGrid
	order, hide, prov := sortKeys[p.sort], p.hideTimeout, p.provOpen
	p.e.SaveUIState("proxies", func(u *model.UIState) {
		u.OpenGroups, u.ProxyGrid = open, grid
		u.ProxySort, u.ProxyHideTimeout, u.ProxyProvidersOpen = order, hide, prov
	})
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	k := p.e.Kit
	st := p.e.API.Status()
	px := p.e.API.Proxies()
	run := st.Core == model.CoreRunning
	p.restore()
	if arg := p.e.Arg(env.Proxies); arg != "" {
		p.scrollTo = arg
		p.open[arg] = true
		p.openInit[arg] = true
	}
	p.mu.Lock()
	testingAll := p.testing
	p.mu.Unlock()

	k.Header(c, i18n.T("代理"), "", func() {
		if st.HasSubscriptions {
			ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索节点")).Label(i18n.T("搜索节点")).Width(160).Bind(&p.search)
			ui.MenuButton(c, i18n.T("排序"), func(m *ui.Menu) {
				if m.Item(i18n.T("网格")).Radio(p.view == viewGrid).Chosen() && p.view != viewGrid {
					p.view = viewGrid
					p.persist()
				}
				if m.Item(i18n.T("列表")).Radio(p.view == viewList).Chosen() && p.view != viewList {
					p.view = viewList
					p.persist()
				}
				m.Separator()
				for i, name := range []string{i18n.T("默认顺序"), i18n.T("按延迟"), i18n.T("按名称")} {
					if m.Item(name).Radio(p.sort == i).Chosen() && p.sort != i {
						p.sort = i
						p.persist()
					}
				}
				m.Separator()
				if m.Item(i18n.T("隐藏超时节点")).Checked(p.hideTimeout).Chosen() {
					p.hideTimeout = !p.hideTimeout
					p.persist()
				}
			}).Label(i18n.T("排序"))
			tip := i18n.T("依次测试所有组的延迟（⌘R）")
			if !p.e.Mac {
				tip = i18n.T("依次测试所有组的延迟（Ctrl+R）")
			}
			b := ui.Button(c, "").Label(i18n.T("全部测速")).Tooltip(tip).Disabled(!run || testingAll)
			b.Children(func() {
				if testingAll {
					ui.Spinner(c).Size(12, 12)
				} else {
					k.Icon(c, "bolt", 14)
				}
			})
			if b.Clicked() {
				p.testAll()
			}
		}
		ui.MenuButton(c, i18n.T("添加节点"), func(m *ui.Menu) {
			p.addNodeMenu(c, m)
		}).Label(i18n.T("添加节点"))
	})
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		p.search.Focus(c)
	}
	if run && c.Shortcut(ui.Cmd, ui.KeyR) {
		p.testAll()
	}

	manualNodes := p.e.API.ManualNodes()
	p.manualByName = make(map[string]model.ManualNode, len(manualNodes))
	for _, mn := range manualNodes {
		p.manualByName[mn.Name] = mn
	}

	switch {
	case !st.HasSubscriptions:
		k.Empty(c, "proxies", i18n.T("还没有订阅"), i18n.T("导入订阅或手动添加代理节点后，这里会列出代理组和节点。"), func() {
			if ui.PrimaryButton(c, i18n.T("导入订阅")).Clicked() {
				p.e.Navigate(env.Subs, "import-url")
			}
			ui.MenuButton(c, i18n.T("手动添加节点"), func(m *ui.Menu) {
				p.addNodeMenu(c, m)
			}).Label(i18n.T("手动添加节点"))
		})
		p.manualDialogView(c)
		return
	case !run:
		k.Empty(c, "power", i18n.T("内核未启动"), i18n.T("启动内核后才能查看和切换节点。"), func() {
			if st.Core == model.CoreStarting {
				ui.Spinner(c).Label(i18n.T("启动中"))
				return
			}
			if ui.PrimaryButton(c, i18n.T("启动内核")).Clicked() {
				p.e.Run(p.e.API.StartCore, i18n.T("内核已启动"))
			}
		})
		if len(manualNodes) > 0 {
			p.stoppedManualSection(c, manualNodes)
		}
		p.manualDialogView(c)
		return
	}

	ui.Scroll(c).Grow(1).Children(func() {
		body := ui.Column(c).Padding(0, 16, 16, 16).Gap(10)
		body.Children(func() {
			if st.Mode == model.ModeDirect {
				k.Banner(c, "warn", i18n.T("当前为直连模式，节点选择不生效。"), func() {
					if ui.Button(c, i18n.T("切换到规则模式")).Clicked() {
						p.e.Run(func() error { return p.e.API.SetMode(model.ModeRule) }, i18n.T("已切换到规则模式"))
					}
				})
			}
			groups := p.visibleGroups(px, st)
			names := groupNames(px)
			p.idle = !anyTesting(px)
			shown := 0
			list := ui.Column(c).Gap(10)
			if st.Mode == model.ModeDirect {
				list.Opacity(0.6)
			}
			list.Children(func() {
				for _, g := range groups {
					// Only an expanded group's members are filtered and
					// sorted; a collapsed one only counts its matches while
					// searching, to hide a group with none.
					var nodes []string
					matched := len(g.All)
					if p.isOpen(g, px) {
						nodes = p.filterNodes(g, px)
						matched = len(nodes)
					} else if p.query != "" {
						matched = p.countMatches(g)
					}
					if p.query != "" && matched == 0 {
						continue
					}
					shown++
					p.group(c, g, nodes, matched, px, names)
				}
			})
			if shown == 0 && p.query != "" {
				k.Empty(c, "search", i18n.Tf("没有匹配“%s”的节点", p.query), i18n.T("换个关键词试试。"), nil)
			}
			if provs := p.e.API.ProxyProviders(); len(provs) > 0 && p.query == "" {
				p.providers(c, provs, px)
			}
		})
	})
	p.manualDialogView(c)
}

// visibleGroups returns the groups in config order, GLOBAL first in
// global mode and hidden otherwise.
func (p *Page) visibleGroups(px model.ProxySnapshot, st model.Status) []model.ProxyGroup {
	var out []model.ProxyGroup
	for _, g := range px.Groups {
		if g.Name == "GLOBAL" {
			if st.Mode == model.ModeGlobal {
				out = append([]model.ProxyGroup{g}, out...)
			}
			continue
		}
		if g.Hidden {
			continue
		}
		out = append(out, g)
	}
	return out
}

// groupNames is the set of the group names, which members may be.
func groupNames(px model.ProxySnapshot) map[string]bool {
	set := make(map[string]bool, len(px.Groups))
	for _, g := range px.Groups {
		set[g.Name] = true
	}
	return set
}

// countMatches counts the members of g whose name has the search in it.
func (p *Page) countMatches(g model.ProxyGroup) int {
	q := strings.ToLower(strings.TrimSpace(p.query))
	n := 0
	for _, m := range g.All {
		if strings.Contains(strings.ToLower(m), q) {
			n++
		}
	}
	return n
}

// filterNodes applies deduplication, search, timeout filter, and sort.
// Deduplication keeps the first occurrence so groups combining explicit
// proxies: with include-all-proxies: true never render duplicate widget keys.
func (p *Page) filterNodes(g model.ProxyGroup, px model.ProxySnapshot) []string {
	q := strings.ToLower(strings.TrimSpace(p.query))
	seen := make(map[string]struct{}, len(g.All))
	var nodes []string
	for _, n := range g.All {
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		if q != "" && !strings.Contains(strings.ToLower(n), q) {
			continue
		}
		// The member in use stays listed even when it timed out: it is
		// what 定位当前节点 reveals and what the user must see to change.
		if p.hideTimeout && px.Proxies[n].Delay < 0 && n != g.Now {
			continue
		}
		nodes = append(nodes, n)
	}
	switch p.sort {
	case sortDelay:
		dv := func(n string) int {
			pr := px.Proxies[n]
			switch {
			case pr.Testing || pr.Delay == 0:
				return 1e7
			case pr.Delay < 0:
				return 1e6
			default:
				return pr.Delay
			}
		}
		sort.SliceStable(nodes, func(i, j int) bool { return dv(nodes[i]) < dv(nodes[j]) })
	case sortName:
		sort.SliceStable(nodes, func(i, j int) bool { return nodes[i] < nodes[j] })
	}
	return nodes
}

func groupTypeName(t model.GroupType) string {
	switch t {
	case model.GroupSelect:
		return i18n.T("选择")
	case model.GroupURLTest:
		return i18n.T("自动测速")
	case model.GroupFallback:
		return i18n.T("故障转移")
	case model.GroupLoadBalance:
		return i18n.T("负载均衡")
	case model.GroupRelay:
		return i18n.T("链式代理")
	default:
		return string(t)
	}
}

// isOpen returns whether a group is expanded: by default all groups start
// collapsed (matching Clash Party) except GLOBAL in global mode.
func (p *Page) isOpen(g model.ProxyGroup, px model.ProxySnapshot) bool {
	if !p.openInit[g.Name] {
		p.openInit[g.Name] = true
		p.open[g.Name] = g.Name == "GLOBAL"
	}
	return p.open[g.Name]
}

// bigGroup is the member count from which a group switches to a virtualized
// inner list. Kept high (400) so normal 60–200 node groups scroll smoothly
// inside the single outer page scroll without nested scrollbar fighting.
const (
	bigGroup        = 400
	bigGroupRows    = 12
	maxBigGroupRows = 30
	nodeRowH        = 32
)

// group draws one collapsible group with its header controls; nodes are
// its members shown when it is open, matched how many match the search.
func (p *Page) group(c *ui.Context, g model.ProxyGroup, nodes []string, matched int, px model.ProxySnapshot, names map[string]bool) {
	t := c.Theme()
	k := p.e.Kit
	open := p.isOpen(g, px)
	o := open
	p.mu.Lock()
	pendingNow := p.pending[g.Name]
	p.mu.Unlock()
	now := g.Now
	if pendingNow != "" {
		now = pendingNow
	}
	// Only while some node is being tested (View looks once a build)
	// is a group's member list walked for its own.
	groupTesting := false
	if !p.idle {
		for _, n := range g.All {
			if px.Proxies[n].Testing {
				groupTesting = true
				break
			}
		}
	}
	selectable := g.Type == model.GroupSelect

	box := ui.Column(c.Key("group:"+g.Name)).Radius(12).Background(t.Background).Border(1, t.Border.Alpha(0.75)).Clip()
	if p.scrollTo == g.Name {
		box.ScrollIntoView()
		p.scrollTo = ""
	}
	box.Children(func() {
		var parts ui.CollapsibleParts
		ui.Row(c).AlignItems(ui.Center).Gap(4).Padding(0, 8, 0, 0).Children(func() {
			parts = ui.CollapsibleBase(c.Key("coll"), &o)
			parts.Trigger.Grow(1).MinHeight(44).Padding(8, 12).Gap(8).AlignItems(ui.Center).Label(g.Name).Children(func() {
				ui.Box(c).TextColor(t.TextMuted).Children(func() { k.Icon(c, "chevron", 12) }).Rotate(90 * parts.Progress())
				p.groupIcon(c, g.Icon)
				ui.Text(c, g.Name).FontSize(14).FontWeight(600).SingleLine().Shrink(0).Tooltip(g.Name)
				if g.Name == px.MainGroup {
					k.Tag(c, i18n.T("主代理组"), "gray")
				}
				if now != "" {
					ui.Box(c).Padding(2, 8).Radius(6).Background(t.Accent.Alpha(0.10)).Shrink(1).Children(func() {
						ui.Text(c, now).FontSize(12).FontWeight(500).TextColor(t.Accent).SingleLine().Tooltip(now)
					})
				}
				meta := groupTypeName(g.Type)
				if p.query != "" {
					meta = i18n.Tn(matched, "%s · %d 个匹配", meta, matched)
				} else if len(g.All) > 0 {
					meta = fmt.Sprintf("%s · %d", meta, len(g.All))
				}
				ui.Text(c, meta).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(meta)
			})
			if now != "" {
				loc := ui.Button(c, "").Label(i18n.Tf("定位当前节点 %s", g.Name)).Tooltip(i18n.T("定位当前节点")).Children(func() {
					k.Icon(c, "locate", 14)
				})
				if loc.Clicked() {
					p.locateNow(c, g.Name)
				}
			}
			test := ui.Button(c, "").Label(i18n.Tf("测速 %s", g.Name)).Tooltip(i18n.T("组内测速")).Disabled(groupTesting).Children(func() {
				if groupTesting {
					ui.Spinner(c).Size(12, 12)
				} else {
					k.Icon(c, "bolt", 14)
				}
			})
			if test.Clicked() {
				name := g.Name
				p.e.Run(func() error { return p.e.API.TestGroupDelay(name) }, "")
			}
			ui.Button(c, "").Label(i18n.Tf("更多 %s", g.Name)).Tooltip(i18n.T("更多")).Children(func() { k.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
				if g.Name == profile.ManualGroupName {
					m.Submenu(i18n.T("添加节点"), func(sub *ui.Menu) {
						p.addNodeMenu(c, sub)
					})
					m.Separator()
				}
				if m.Item(i18n.T("设为主代理组")).Checked(g.Name == px.MainGroup).Disabled(!selectable).Chosen() {
					name := g.Name
					p.e.Run(func() error { return p.e.API.SetMainGroup(name) }, i18n.Tf("已将「%s」设为主代理组", name))
				}
				if m.Item(i18n.T("复制组名")).Chosen() {
					c.WriteClipboard(g.Name)
					p.e.Toast(i18n.T("已复制"), false)
				}
			})
		})
		if o != open {
			p.open[g.Name] = o
			p.persist()
		}
		parts.Panel(func() {
			if nodes == nil {
				nodes = p.filterNodes(g, px)
			}
			ui.Column(c).Padding(0, 6, 6, 6).Children(func() {
				ui.Box(c).Height(1).Background(t.Border).Margin(0, 6, 4, 6)
				if p.view == viewGrid {
					// From the window's width, not the grid's last
					// frame, so a resize refits the columns at once.
					winW, _ := c.Size()
					cols := gridColumnsFor(winW - gridChrome)
					if len(nodes) > bigGroup {
						p.bigGrid(c, g, nodes, cols, now, selectable, px, names)
						return
					}
					ui.Grid(c).Columns(cols).Gap(6).Padding(4, 6, 2, 6).Children(func() {
						for _, n := range nodes {
							p.cell(c, g, n, now, selectable, px, names)
						}
					})
					return
				}
				if len(nodes) > bigGroup {
					p.bigList(c, g, nodes, now, selectable, px, names)
					return
				}
				for _, n := range nodes {
					p.node(c, g, n, now, selectable, px, names)
				}
			})
		})
	})
}

// anyTesting reports whether a latency test runs on any node of px.
func anyTesting(px model.ProxySnapshot) bool {
	for _, pr := range px.Proxies {
		if pr.Testing {
			return true
		}
	}
	return false
}

// bigList shows a large group's members in a virtual list, scrolled to
// the current member when 定位当前节点 asks for it.
func (p *Page) bigList(c *ui.Context, g model.ProxyGroup, nodes []string, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	ls := p.lists[g.Name]
	if ls == nil {
		ls = &ui.ListState{}
		p.lists[g.Name] = ls
	}
	ls.Key = func(i int) any { return nodes[i] }
	// Sorted by delay, the members move as results arrive: the list stays
	// where it is scrolled rather than follow the node at its top.
	ls.KeepOffset = true
	if l := p.locate; l.group == g.Name && !l.shown && !l.until.IsZero() {
		if i := slices.Index(nodes, now); i >= 0 {
			ls.ScrollTo(i, ui.Center)
		}
	}
	ui.List(c.Key("nodes"), ls, len(nodes), func(i int) {
		p.node(c, g, nodes[i], now, selectable, px, names)
	}).Height(float32(bigListRows(c, len(nodes))) * nodeRowH).FillWidth().Label(g.Name)
}

// gridRowH is the height a row of tiles takes in a large group's virtual
// grid: a tile's minimum height and the gap under it.
const gridRowH = 52 + 6

// bigGrid is bigList for the grid view: a large group's tiles in a
// virtual list of rows of cols tiles, which builds only the rows in view
// (a group of hundreds of nodes is otherwise hundreds of tiles each build,
// several times a second while a latency test runs).
func (p *Page) bigGrid(c *ui.Context, g model.ProxyGroup, nodes []string, cols int, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	key := "grid:" + g.Name
	ls := p.lists[key]
	if ls == nil {
		ls = &ui.ListState{}
		p.lists[key] = ls
	}
	rows := (len(nodes) + cols - 1) / cols
	ls.Key = func(i int) any { return nodes[i*cols] }
	ls.KeepOffset = true // as bigList: sorted by delay, the tiles move
	if l := p.locate; l.group == g.Name && !l.shown && !l.until.IsZero() {
		if i := slices.Index(nodes, now); i >= 0 {
			ls.ScrollTo(i/cols, ui.Center)
		}
	}
	_, h := c.Size()
	shown := min(max(int(h*0.55/gridRowH), bigGroupRows/2), maxBigGroupRows/2, rows)
	ui.List(c.Key("tiles"), ls, rows, func(i int) {
		ui.Grid(c).Columns(cols).Gap(6).Padding(3, 6).Children(func() {
			for _, n := range nodes[i*cols : min((i+1)*cols, len(nodes))] {
				p.cell(c, g, n, now, selectable, px, names)
			}
		})
	}).Height(float32(shown) * gridRowH).FillWidth().Label(g.Name)
}

// bigListRows is how many rows a large group's virtual list shows: about
// half the window's height, from bigGroupRows up to maxBigGroupRows, so a
// tall window shows more of a big group rather than a fixed strip.
func bigListRows(c *ui.Context, n int) int {
	_, h := c.Size()
	rows := int(h * 0.55 / nodeRowH)
	return min(max(rows, bigGroupRows), maxBigGroupRows, n)
}

// memberKind classifies a group member for display: builtin (DIRECT/REJECT),
// nested group (one of names), or node with its protocol.
func memberKind(n string, px model.ProxySnapshot, names map[string]bool) (proto string, isGroup, builtin bool) {
	if names[n] {
		return i18n.T("代理组"), true, false
	}
	if n == "DIRECT" || n == "REJECT" {
		return i18n.T("内置"), false, true
	}
	if pr, known := px.Proxies[n]; known {
		return pr.Type, false, false
	}
	return "", false, false
}

// cell draws one member as a Clash Party–style 2-row card with independent
// sibling buttons: left area selects the node, right delay badge triggers
// single-node latency test without conflicting with node selection.
func (p *Page) cell(c *ui.Context, g model.ProxyGroup, n, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	t := c.Theme()
	k := p.e.Kit
	pr := px.Proxies[n]
	proto, _, builtin := memberKind(n, px, names)
	cur := n == now

	tip := n
	if proto != "" {
		tip = n + " · " + proto
	}

	tile := ui.Row(c.Key("cell:" + n)).Radius(10).AlignItems(ui.Stretch).MinHeight(52).Clip()
	selHovered := false
	selClicked := false

	tile.Children(func() {
		if cur {
			ui.Box(c).Width(3).Background(t.Accent).Shrink(0)
		}
		var sel ui.Element
		if selectable {
			sel = ui.ButtonBase(c.Key("sel")).Label(n).Cursor(ui.CursorPointer)
			selHovered = sel.Hovered()
		} else {
			sel = ui.Column(c.Key("sel"))
		}
		sel.Column().Grow(1).Shrink(1).Padding(7, 4, 7, 10).Gap(3).Justify(ui.Center).AlignItems(ui.Start).Children(func() {
			name := ui.Text(c, n).FontSize(13).FontWeight(500).SingleLine().Tooltip(tip)
			if cur {
				name.FontWeight(600)
			}
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				if !selectable && cur {
					k.Tag(c, i18n.T("当前"), "accent")
				}
				if proto != "" {
					ui.Box(c).Padding(1, 5).Radius(4).Background(t.Background.Alpha(0.75)).Border(1, t.Border.Alpha(0.45)).Children(func() {
						ui.Text(c, proto).FontSize(10.5).TextColor(t.TextMuted).SingleLine()
					})
				}
			})
		})
		if selectable && sel.Clicked() {
			selClicked = true
		}

		if builtin {
			ui.Box(c).Padding(0, 10, 0, 6).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, "—").FontSize(12.5).TextColor(t.TextMuted)
			})
		} else {
			ui.Box(c).Padding(0, 8, 0, 2).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
				d := ui.ButtonBase(c.Key("delay")).MinWidth(44).Padding(4, 8).Radius(6).
					AlignItems(ui.Center).Justify(ui.Center).
					Label(i18n.Tf("重新测速 %s", n)).Tooltip(i18n.T("单击重新测速")).
					Cursor(ui.CursorPointer)
				if d.Hovered() {
					d.Background(t.Accent.Alpha(0.14))
				}
				d.Children(func() { pillDelay(c, t, pr.Delay, pr.Testing) })
				if d.Clicked() {
					name := n
					p.e.Run(func() error { return p.e.API.TestProxyDelay(name) }, "")
				}
			})
		}

		if cur {
			ui.Box(c).Width(3).Background(t.Accent).Shrink(0)
		}
	})

	switch {
	case cur:
		tile.Background(t.Accent.Alpha(0.13)).Border(1, t.Accent.Alpha(0.65))
	case selHovered:
		tile.Background(t.SurfaceHover).Border(1, t.Accent.Alpha(0.45))
	default:
		tile.Background(t.Surface).Border(1, t.Border.Alpha(0.45))
	}
	if reveal, tint := p.locating(c, g.Name, cur); tint {
		tile.Background(t.Accent.Alpha(0.28)).Border(1.5, t.Accent)
		if reveal {
			tile.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollCenter, Animation: &ui.Smooth})
		}
	}
	if selectable && selClicked && !cur {
		p.pick(g.Name, n)
	}
}

// pillDelay draws a compact Clash Party–style delay label: pure number without
// "ms", accent "测速" when untested, danger "超时" on timeout.
func pillDelay(c *ui.Context, t *ui.Theme, delay int, testing bool) {
	if testing {
		ui.Spinner(c).Size(12, 12).Label(i18n.T("测速中"))
		return
	}
	switch {
	case delay == 0:
		ui.Text(c, i18n.T("测速")).FontSize(12.5).FontWeight(500).TextColor(t.Accent).SingleLine()
	case delay < 0:
		ui.Text(c, i18n.T("超时")).FontSize(12.5).TextColor(t.Danger).SingleLine()
	default:
		col := t.Success
		if delay >= 600 {
			col = t.Danger
		} else if delay >= 300 {
			col = t.Warning
		}
		ui.Text(c, fmt.Sprint(delay)).FontSize(12.5).FontWeight(500).TextColor(col).FontFeatures("tnum").SingleLine()
	}
}

// providers draws the collapsed 代理集合 section at the bottom of the page:
// one row per proxy-provider with its source, node count, update time,
// subscription usage, and buttons to re-download and health-check it.
func (p *Page) providers(c *ui.Context, provs []model.ProxyProvider, px model.ProxySnapshot) {
	t := c.Theme()
	k := p.e.Kit
	box := ui.Column(c.Key("providers")).Radius(10).Background(t.Surface).Border(1, t.Border).Clip()
	box.Children(func() {
		wasOpen := p.provOpen
		parts := ui.CollapsibleBase(c.Key("coll"), &p.provOpen)
		if p.provOpen != wasOpen {
			p.persist()
		}
		parts.Trigger.MinHeight(40).Padding(6, 12).Gap(8).AlignItems(ui.Center).Label(i18n.T("代理集合")).Children(func() {
			ui.Box(c).TextColor(t.TextMuted).Children(func() { k.Icon(c, "chevron", 12) }).Rotate(90 * parts.Progress())
			ui.Text(c, i18n.T("代理集合")).FontSize(13).FontWeight(600).SingleLine()
			ui.Text(c, i18n.Tf("%d 个代理集合", len(provs))).FontSize(12).TextColor(t.TextMuted).SingleLine().
				Tooltip(i18n.Tn(len(provs), "%d 个 proxy-provider", len(provs)))
		})
		parts.Panel(func() {
			ui.Column(c).Padding(0, 6, 6, 6).Children(func() {
				ui.Box(c).Height(1).Background(t.Border).Margin(0, 6, 4, 6)
				for _, pv := range provs {
					p.provider(c, pv, px)
				}
			})
		})
	})
}

// providerSource names a provider's vehicle type.
func providerSource(vehicle string) string {
	switch vehicle {
	case "HTTP":
		return i18n.T("链接")
	case "File":
		return i18n.T("文件")
	case "Inline":
		return i18n.T("内联")
	case "Compatible":
		return i18n.T("配置内")
	default:
		return vehicle
	}
}

// providerMeta is the muted summary line under a provider's name.
func providerMeta(pv model.ProxyProvider) string {
	meta := i18n.Tn(len(pv.Proxies), "%s · %d 个节点", providerSource(pv.VehicleType), len(pv.Proxies))
	if !pv.UpdatedAt.IsZero() {
		meta += i18n.Tf(" · 更新于 %s", env.FmtAgo(pv.UpdatedAt))
	}
	if pv.Total > 0 {
		meta += i18n.Tf(" · 已用 %s / %s GB", env.FmtGB(pv.Upload+pv.Download), env.FmtGB(pv.Total))
	}
	if !pv.Expire.IsZero() {
		meta += i18n.Tf(" · 到期 %s", env.FmtDate(pv.Expire))
	}
	return meta
}

// provider draws one proxy-provider row.
func (p *Page) provider(c *ui.Context, pv model.ProxyProvider, px model.ProxySnapshot) {
	t := c.Theme()
	k := p.e.Kit
	checking := pv.Checking
	for _, n := range pv.Proxies {
		if px.Proxies[n].Testing {
			checking = true
			break
		}
	}
	ui.Row(c.Key("prov:"+pv.Name)).MinHeight(44).Padding(4, 4, 4, 12).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Text(c, pv.Name).FontSize(13).FontWeight(500).SingleLine().Tooltip(pv.Name)
			meta := providerMeta(pv)
			ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted).SingleLine().Tooltip(meta)
		})
		canUpdate := pv.VehicleType == "HTTP" || pv.VehicleType == "File"
		up := ui.Button(c, "").Label(i18n.Tf("更新 %s", pv.Name)).Tooltip(i18n.T("重新下载该代理集合")).Disabled(!canUpdate || pv.Updating).Children(func() {
			if pv.Updating {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "refresh", 14)
			}
		})
		if up.Clicked() {
			name := pv.Name
			p.e.Run(func() error { return p.e.API.UpdateProxyProvider(name) }, i18n.Tf("已更新「%s」", name))
		}
		hc := ui.Button(c, "").Label(i18n.Tf("健康检查 %s", pv.Name)).Tooltip(i18n.T("测试该集合中所有节点的延迟")).Disabled(checking || pv.TestURL == "").Children(func() {
			if checking {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "bolt", 14)
			}
		})
		if hc.Clicked() {
			name := pv.Name
			p.e.Run(func() error { return p.e.API.HealthCheckProvider(name) }, "")
		}
	})
}

// node draws one member row with independent sibling buttons for node
// selection (left) and single-node latency testing (right).
func (p *Page) node(c *ui.Context, g model.ProxyGroup, n, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	t := c.Theme()
	k := p.e.Kit
	pr := px.Proxies[n]
	proto, _, builtin := memberKind(n, px, names)
	cur := n == now

	row := ui.Row(c.Key("node:" + n)).Height(nodeRowH).AlignItems(ui.Stretch).Radius(6).Clip()
	selHovered := false
	selClicked := false

	row.Children(func() {
		var sel ui.Element
		if selectable {
			sel = ui.ButtonBase(c.Key("sel")).Label(n).Cursor(ui.CursorPointer)
			selHovered = sel.Hovered()
		} else {
			sel = ui.Row(c.Key("sel"))
		}
		sel.Row().Grow(1).Shrink(1).Padding(0, 6, 0, 12).Gap(10).AlignItems(ui.Center).Children(func() {
			if selectable {
				radio := ui.Box(c).Size(14, 14).Radius(7).Shrink(0)
				if cur {
					radio.Border(4, t.Accent).Background(t.Background)
				} else {
					radio.Border(1.5, t.TextMuted.Alpha(0.6))
				}
			} else {
				ui.Box(c).Size(14, 14).Shrink(0)
			}
			ui.Text(c, n).FontSize(13).SingleLine().Shrink(1).Tooltip(n)
			if !selectable && cur {
				k.Tag(c, i18n.T("当前"), "accent")
			}
			if proto != "" {
				ui.Text(c, proto).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
			}
			ui.Spacer(c)
		})
		if selectable && sel.Clicked() {
			selClicked = true
		}

		if builtin {
			ui.Box(c).Padding(0, 10, 0, 6).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
			})
			return
		}
		ui.Box(c).Padding(0, 8, 0, 2).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
			d := ui.ButtonBase(c.Key("delay")).Label(i18n.Tf("重新测速 %s", n)).Tooltip(i18n.T("单击重新测速")).
				Padding(2, 8).Radius(6).MinWidth(48).AlignItems(ui.Center).Justify(ui.End).Cursor(ui.CursorPointer)
			if d.Hovered() {
				d.Background(t.Accent.Alpha(0.14))
			}
			d.Children(func() { k.Delay(c, pr.Delay, pr.Testing) })
			if d.Clicked() {
				name := n
				p.e.Run(func() error { return p.e.API.TestProxyDelay(name) }, "")
			}
		})
		if mn, isManual := p.manualByName[n]; isManual {
			ui.Box(c).Padding(0, 4, 0, 0).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
				ui.Button(c, "").Label(i18n.Tf("更多 %s", n)).Tooltip(i18n.T("更多")).Children(func() { k.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
					if m.Item(i18n.T("编辑…")).Chosen() {
						p.openManualNode(mn)
					}
					if m.Item(i18n.T("删除…")).Danger().Chosen() {
						p.askDeleteManualNode(mn)
					}
				})
			})
		}
	})

	switch {
	case cur:
		row.Background(t.Accent.Alpha(0.10))
	case selHovered:
		row.Background(t.SurfaceHover)
	}
	if reveal, tint := p.locating(c, g.Name, cur); tint {
		row.Background(t.Accent.Alpha(0.28))
		if reveal {
			row.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollCenter, Animation: &ui.Smooth})
		}
	}
	if selectable && selClicked && !cur {
		p.pick(g.Name, n)
	}
}

func (p *Page) askDeleteManualNode(mn model.ManualNode) {
	p.e.Confirm(i18n.Tf("删除节点「%s」？", mn.Name), i18n.T("删除后会重新生成配置。"), i18n.T("删除"), true, func() {
		p.e.Run(func() error { return p.e.API.DeleteManualNode(mn.ID) }, i18n.T("节点已删除"))
	})
}

func (p *Page) stoppedManualSection(c *ui.Context, nodes []model.ManualNode) {
	t := c.Theme()
	k := p.e.Kit
	ui.Column(c).Padding(0, 24, 20, 24).Gap(8).Children(func() {
		k.Section(c, i18n.T("手动节点"), "", func() {
			for _, mn := range nodes {
				node := mn
				ui.Row(c.Key("manual:"+node.ID)).MinHeight(40).Padding(4, 12).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, node.Name).FontSize(13).FontWeight(500).SingleLine().Shrink(1)
					ui.Text(c, protoOptionForType(node.Type).label).FontSize(11).TextColor(t.TextMuted).SingleLine()
					ui.Text(c, node.Server+":"+strconv.Itoa(node.Port)).FontSize(11).Font("monospace").TextColor(t.TextMuted).SingleLine()
					ui.Spacer(c)
					if ui.Button(c, i18n.T("编辑")).Clicked() {
						p.openManualNode(node)
					}
					if ui.Button(c, i18n.T("删除")).TextColor(t.Danger).Clicked() {
						p.askDeleteManualNode(node)
					}
				})
			}
		})
	})
}

// pick selects a node: optimistic until the core confirms.
func (p *Page) pick(group, name string) {
	p.mu.Lock()
	p.pending[group] = name
	p.mu.Unlock()
	p.e.Run(func() error {
		err := p.e.API.SelectProxy(group, name)
		p.mu.Lock()
		delete(p.pending, group)
		p.mu.Unlock()
		return err
	}, i18n.Tf("已切换到 %s", name))
}

func (p *Page) testAll() {
	p.mu.Lock()
	if p.testing {
		p.mu.Unlock()
		return
	}
	p.testing = true
	p.mu.Unlock()
	p.e.Run(func() error {
		err := p.e.API.TestAllDelays()
		p.mu.Lock()
		p.testing = false
		p.mu.Unlock()
		return err
	}, i18n.T("测速完成"))
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicProxies}
}
