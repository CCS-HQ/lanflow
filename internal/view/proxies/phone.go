package proxies

// The 代理 page on a phone (Env.Phone): the large title, a strip of the
// groups as pills that scrolls sideways, a line of what the chosen group
// uses, and its members as a grid of cards two to a row, tapped to choose.
// Testing, sorting, locating, searching and adding nodes are in the top
// bar (PhoneActions). The whole page is one virtual list, so a group of
// hundreds of nodes builds only the rows of cards in view.

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

// phoneCols is how many cards a row of the phone's grid holds.
const phoneCols = 2

// phoneCardH is a node card's least height.
const phoneCardH = 64

// phoneState is what the phone's layout keeps between builds.
type phoneState struct {
	group      string // the chosen group's name; "" is the main group
	revealPill bool   // scroll the chosen group's pill into view once
	searching  bool   // the search field shows
	list       ui.ListState
	pills      ui.ScrollState
}

// PhoneLargeTitle: the page starts with iOS's large title (env.LargeTitle).
func (p *Page) PhoneLargeTitle() bool { return true }

// takeArg takes what another page asked of this one: a group to choose
// (首页's current node card), or the global search.
func (p *Page) takeArg() {
	arg := p.e.Arg(env.Proxies)
	if arg == "" {
		return
	}
	if q, ok := env.TakeQuery(arg); ok {
		p.query, p.phone.searching = q, true
		return
	}
	p.phone.group, p.phone.revealPill = arg, true
}

// phoneGroup returns the group the phone shows: the one chosen, else the
// main group, else the first; ok is false when there is none.
func (p *Page) phoneGroup(groups []model.ProxyGroup, px model.ProxySnapshot) (model.ProxyGroup, bool) {
	for _, want := range []string{p.phone.group, px.MainGroup} {
		if want == "" {
			continue
		}
		for _, g := range groups {
			if g.Name == want {
				return g, true
			}
		}
	}
	if len(groups) > 0 {
		return groups[0], true
	}
	return model.ProxyGroup{}, false
}

// groupNow is the member group g uses, the pending choice first.
func (p *Page) groupNow(g model.ProxyGroup) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := p.pending[g.Name]; n != "" {
		return n
	}
	return g.Now
}

// groupTesting reports whether a member of g is being tested.
func groupTesting(g model.ProxyGroup, px model.ProxySnapshot) bool {
	for _, n := range g.All {
		if px.Proxies[n].Testing {
			return true
		}
	}
	return false
}

// PhoneActions draws the top bar's actions: testing the chosen group and
// the ⋯ menu.
func (p *Page) PhoneActions(c *ui.Context) {
	k := p.e.Kit
	p.takeArg()
	st := p.e.API.Status()
	ready := st.HasSubscriptions && st.Core == model.CoreRunning
	var (
		g       model.ProxyGroup
		have    bool
		px      model.ProxySnapshot
		isChain bool
		chain   model.ProxyChain
	)
	if ready {
		px = p.e.API.Proxies()
		g, have = p.phoneGroup(p.visibleGroups(px, st), px)
		if have {
			chain, isChain = p.chainByName[g.Name]
			busy := groupTesting(g, px)
			b := env.IconButton(c, k, "bolt", i18n.T("测速当前组")).Disabled(busy)
			if busy {
				b.Opacity(0.5)
			}
			if b.Clicked() && !busy {
				name := g.Name
				p.e.Run(func() error { return p.e.API.TestGroupDelay(name) }, "")
			}
		}
	}
	env.IconButton(c, k, "more", i18n.T("更多")).Menu(func(m *ui.Menu) {
		if ready && have {
			if m.Item(i18n.T("搜索节点")).Checked(p.phone.searching).Chosen() {
				p.phone.searching = !p.phone.searching
				if !p.phone.searching {
					p.query = ""
				}
			}
			m.Submenu(i18n.T("排序"), p.sortMenu)
			if m.Item(i18n.T("定位当前节点")).Disabled(p.groupNow(g) == "").Chosen() {
				p.phoneLocate(c, g.Name)
			}
			main := g.Name == px.MainGroup
			if m.Item(i18n.T("设为主代理组")).Checked(main).Disabled(main || g.Type != model.GroupSelect).Chosen() {
				name := g.Name
				p.e.Run(func() error { return p.e.API.SetMainGroup(name) }, i18n.Tf("已将「%s」设为主代理组", name))
			}
			m.Separator()
		}
		m.Submenu(i18n.T("添加节点"), func(sub *ui.Menu) { p.addNodeMenu(c, sub) })
		if isChain {
			if m.Item(i18n.T("编辑链路…")).Chosen() {
				p.editChain(chain)
			}
		}
		if m.Item(i18n.T("链式代理…")).Disabled(!st.HasSubscriptions).Chosen() {
			p.openChains()
		}
	})
}

// phoneLocate reveals and tints the current member of group.
func (p *Page) phoneLocate(c *ui.Context, group string) {
	p.query = "" // the node may not match
	p.locate = locateState{group: group, until: c.Now().Add(highlightFor)}
}

// phoneView draws the page on a phone.
func (p *Page) phoneView(c *ui.Context, st model.Status, px model.ProxySnapshot) {
	p.takeArg()
	manualNodes := p.loadLists()
	run := st.Core == model.CoreRunning
	if !st.HasSubscriptions || !run {
		p.e.Scroll(c, env.Proxies).Grow(1).FillWidth().Children(func() {
			ui.Column(c).Padding(0, env.PhoneGap, env.PhoneGap, env.PhoneGap).Gap(12).Children(func() {
				env.LargeTitle(c, i18n.T("代理"))
				ui.Box(c).Height(40)
				if !st.HasSubscriptions {
					p.noSubscriptions(c)
					return
				}
				p.coreStopped(c, st)
				if len(manualNodes) > 0 {
					p.stoppedManualSection(c, manualNodes)
				}
			})
		})
		p.manualDialogView(c)
		if st.HasSubscriptions {
			p.chainsDialogView(c, px)
		}
		return
	}

	groups := p.visibleGroups(px, st)
	g, have := p.phoneGroup(groups, px)
	names := groupNames(px)
	now := ""
	var nodes []string
	chain, isChain := p.chainByName[g.Name]
	inForce := false
	if have {
		now = p.groupNow(g)
		inForce = isChain && len(g.All) == 1 && profile.IsChainClone(g.All[0])
		if !inForce {
			nodes = p.filterNodes(g, px)
		}
	}
	selectable := g.Type == model.GroupSelect
	rows := (len(nodes) + phoneCols - 1) / phoneCols
	// The rows: the head (title to the summary line), the rows of cards,
	// and the foot (an empty search, the proxy providers).
	n := 1 + rows + 1
	ls := &p.phone.list
	ls.Key = func(i int) any {
		switch {
		case i == 0:
			return "head"
		case i <= rows:
			return "row:" + nodes[(i-1)*phoneCols]
		}
		return "foot"
	}
	ls.KeepOffset = true // sorted by delay, the cards move as results come
	if l := p.locate; l.group == g.Name && !l.shown && !l.until.IsZero() {
		if i := slices.Index(nodes, now); i >= 0 {
			ls.ScrollTo(1+i/phoneCols, ui.Center)
		}
	}
	list := ui.List(c.Key("phone-list"), ls, n, func(i int) {
		switch {
		case i == 0:
			p.phoneHead(c, st, px, groups, g, have, now, len(nodes), inForce, chain, isChain)
		case i <= rows:
			ui.Row(c).Padding(0, env.PhoneGap, 10, env.PhoneGap).Gap(10).AlignItems(ui.Stretch).Children(func() {
				row := nodes[(i-1)*phoneCols : min(i*phoneCols, len(nodes))]
				for _, name := range row {
					p.phoneCard(c, g, name, now, selectable, px, names)
				}
				for range phoneCols - len(row) {
					ui.Box(c).Grow(1).Basis(0)
				}
			})
		default:
			p.phoneFoot(c, px, have && !inForce && len(nodes) == 0)
		}
	}).Grow(1).Basis(0).MinHeight(0).FillWidth().Label(i18n.T("代理"))
	if p.e.PhoneScroll != nil {
		list.TrackScroll(p.e.PhoneScroll(env.Proxies))
	}
	p.manualDialogView(c)
	p.chainsDialogView(c, px)
}

// phoneHead is the list's first row: the large title, the search field,
// the direct mode's banner, the group pills and the summary line.
func (p *Page) phoneHead(c *ui.Context, st model.Status, px model.ProxySnapshot, groups []model.ProxyGroup, g model.ProxyGroup, have bool, now string, count int, inForce bool, chain model.ProxyChain, isChain bool) {
	t := c.Theme()
	k := p.e.Kit
	pad := func(body func()) { ui.Column(c).PaddingX(env.PhoneGap).Gap(8).Children(body) }
	ui.Column(c).Gap(12).Padding(0, 0, 12, 0).Children(func() {
		if !env.Material() {
			pad(func() { env.LargeTitle(c, i18n.T("代理")) })
		} else {
			ui.Box(c).Height(4)
		}
		if p.phone.searching {
			ui.Row(c).PaddingX(env.PhoneGap).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索节点")).Label(i18n.T("搜索节点")).
					Grow(1).Height(36).Bind(&p.search)
				cancel := ui.ButtonBase(c.Key("search-cancel")).Height(env.TouchSize).PaddingX(4).Center().
					Label(i18n.T("取消")).TextColor(t.Accent).Cursor(ui.CursorPointer)
				cancel.Children(func() { ui.Text(c, i18n.T("取消")).FontSize(15).TextColor(t.Accent).SingleLine() })
				if cancel.Clicked() {
					p.phone.searching, p.query = false, ""
				}
			})
		}
		if st.Mode == model.ModeDirect {
			pad(func() {
				k.Banner(c, "warn", i18n.T("当前为直连模式，节点选择不生效。"), func() {
					if ui.Button(c, i18n.T("切换到规则模式")).Clicked() {
						p.e.Run(func() error { return p.e.API.SetMode(model.ModeRule) }, i18n.T("已切换到规则模式"))
					}
				})
			})
		}
		if !have {
			k.Empty(c, "proxies", i18n.T("没有代理组"), "", nil)
			return
		}
		p.phonePills(c, groups, g.Name)
		pad(func() {
			info := ""
			switch {
			case inForce:
				info = i18n.Tn(len(chain.Hops), "链式代理 · %d 跳", len(chain.Hops))
			case isChain:
				info = i18n.T("链路已停用，命中它的流量被拒绝")
			case p.query != "":
				info = i18n.Tn(count, "%s · %d 个匹配", groupTypeName(g.Type), count)
			case now != "":
				info = i18n.Tn(count, "当前 %s · %d 个节点", now, count)
			default:
				info = i18n.Tn(count, "%d 个节点", count)
			}
			if g.Type != model.GroupSelect && !isChain && p.query == "" {
				info = groupTypeName(g.Type) + " · " + info
			}
			ui.Text(c, info).FontSize(13).TextColor(t.TextMuted).MaxLines(2)
			if inForce {
				ui.Column(c).Radius(env.PhoneRadius).Background(t.Surface).Padding(4, 0).Children(func() {
					p.chainGroupPanel(c, g, chain, px)
				})
			}
		})
	})
}

// phonePills draws the groups as pills in a strip that scrolls sideways;
// a tap chooses one.
func (p *Page) phonePills(c *ui.Context, groups []model.ProxyGroup, chosen string) {
	t := c.Theme()
	ui.ScrollHorizontal(c.Key("pills")).TrackScroll(&p.phone.pills).Scrollbars(false).FillWidth().Children(func() {
		ui.Row(c).Padding(0, env.PhoneGap).Gap(8).AlignItems(ui.Center).Children(func() {
			for _, g := range groups {
				cur := g.Name == chosen
				b := ui.ButtonBase(c.Key("pill:" + g.Name)).Height(env.TouchSize).Shrink(0).AlignItems(ui.Center).
					Label(g.Name).Cursor(ui.CursorPointer)
				pressed := b.Pressed()
				b.Children(func() {
					pill := ui.Row(c).Height(34).PaddingX(14).Radius(17).Gap(6).AlignItems(ui.Center)
					switch {
					case cur:
						pill.Background(t.Accent).TextColor(ui.RGB(255, 255, 255))
					case pressed:
						pill.Background(t.SurfaceHover)
					default:
						pill.Background(t.Surface)
					}
					pill.Children(func() {
						p.groupIcon(c, g.Icon)
						txt := ui.Text(c, g.Name).FontSize(14).SingleLine()
						if cur {
							txt.FontWeight(600).TextColor(ui.RGB(255, 255, 255))
						}
					})
				})
				if cur && p.phone.revealPill {
					b.ScrollIntoView(ui.ScrollIntoViewOptions{Inline: ui.ScrollCenter})
					p.phone.revealPill = false
				}
				if b.Clicked() && !cur {
					p.phone.group = g.Name
				}
			}
		})
	})
}

// phoneFoot is the list's last row: the empty search, the proxy providers.
func (p *Page) phoneFoot(c *ui.Context, px model.ProxySnapshot, empty bool) {
	ui.Column(c).Padding(0, env.PhoneGap, env.PhoneGap, env.PhoneGap).Gap(12).Children(func() {
		if empty && p.query != "" {
			p.e.Kit.Empty(c, "search", i18n.Tf("没有匹配“%s”的节点", p.query), i18n.T("换个关键词试试。"), nil)
		}
		if provs := p.e.API.ProxyProviders(); len(provs) > 0 && p.query == "" {
			p.providers(c, provs, px)
		}
	})
}

// phoneCard draws one member as a card: its name over its protocol and
// delay. A tap on a select group's card chooses it; a long press offers
// testing it, chains and, for a manual node, editing.
func (p *Page) phoneCard(c *ui.Context, g model.ProxyGroup, n, now string, selectable bool, px model.ProxySnapshot, names map[string]bool) {
	t := c.Theme()
	k := p.e.Kit
	pr := px.Proxies[n]
	proto, _, builtin := memberKind(n, px, names)
	cur := n == now

	var card ui.Element
	if selectable {
		card = ui.ButtonBase(c.Key("card:" + n)).Label(n).Cursor(ui.CursorPointer)
	} else {
		card = ui.Column(c.Key("card:" + n)).Label(n)
	}
	card.Column().Grow(1).Basis(0).Shrink(1).MinWidth(0).MinHeight(phoneCardH).Padding(10, 12).Gap(6).
		Radius(env.PhoneRadius).Background(t.Surface).AlignItems(ui.Stretch).
		ContextMenu(func(m *ui.Menu) { p.phoneCardMenu(m, g, n, builtin, px, names) })
	pressed := selectable && card.Pressed()
	card.Children(func() {
		ui.Text(c, n).FontSize(14).FontWeight(500).MaxLines(2)
		ui.Spacer(c)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, proto).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1)
			ui.Spacer(c)
			if builtin {
				ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
				return
			}
			ui.Box(c).Shrink(0).Children(func() { k.Delay(c, pr.Delay, pr.Testing) })
		})
	})
	switch {
	case cur:
		card.Background(t.Accent.Alpha(0.12)).Border(1.5, t.Accent)
	case pressed:
		card.Background(t.SurfaceHover)
	}
	if reveal, tint := p.locating(c, g.Name, cur); tint {
		card.Background(t.Accent.Alpha(0.28))
		if reveal {
			card.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollCenter, Animation: &ui.Smooth})
		}
	}
	if selectable && card.Clicked() && !cur {
		p.pick(g.Name, n)
	}
}

// phoneCardMenu is a card's long press menu.
func (p *Page) phoneCardMenu(m *ui.Menu, g model.ProxyGroup, n string, builtin bool, px model.ProxySnapshot, names map[string]bool) {
	if !builtin {
		if m.Item(i18n.T("测速")).Chosen() {
			p.e.Run(func() error { return p.e.API.TestProxyDelay(n) }, "")
		}
	}
	if mn, ok := p.manualByName[n]; ok {
		if m.Item(i18n.T("编辑…")).Chosen() {
			p.openManualNode(mn)
		}
		if m.Item(i18n.T("删除…")).Danger().Chosen() {
			p.askDeleteManualNode(mn)
		}
	}
	p.nodeChainMenu(m, g, n, px, names)
}
