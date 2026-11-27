package proxies

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

// 链式代理 (docs/chain-proxy.md §3.4): one dialog with the list of chains
// and the editor of one chain. The chains themselves live in the service
// (API.ProxyChains); a chain that is in force shows on the page as a
// select group named after it whose only member is a generated node
// (profile.ChainClonePrefix), drawn here as the path of its hops.

// chainsDialog is the dialog's state.
type chainsDialog struct {
	open    bool
	session int // a new key per opening: fields start afresh
	editing bool
	draft   chainDraft
	err     string
	saving  bool
}

// chainDraft is the chain being edited.
type chainDraft struct {
	id      string // "" = new
	name    string
	enabled bool
	hops    []string // entry, middle…, exit; "" = not chosen yet
}

// openChains opens the dialog on the chain list.
func (p *Page) openChains() {
	d := &p.chains
	d.open, d.editing, d.err, d.saving = true, false, "", false
	d.session++
}

// newChain opens the editor on a new chain with the given entry and
// exit ("" = to choose).
func (p *Page) newChain(entry, exit string) {
	d := &p.chains
	d.open, d.editing, d.err, d.saving = true, true, "", false
	d.session++
	d.draft = chainDraft{enabled: true, hops: []string{entry, exit}}
}

// editChain opens the editor on ch.
func (p *Page) editChain(ch model.ProxyChain) {
	d := &p.chains
	d.open, d.editing, d.err, d.saving = true, true, "", false
	d.session++
	d.draft = chainDraft{id: ch.ID, name: ch.Name, enabled: ch.Enabled, hops: append([]string(nil), ch.Hops...)}
}

// chainCandidates lists what a hop may be: nodes are the configuration's
// own nodes (not those of a proxy provider, which dialer-proxy cannot
// name), entries are those and the proxy groups that cannot lead back
// into a chain. Both in the configuration's order.
func chainCandidates(px model.ProxySnapshot, chains []model.ProxyChain) (entries, nodes []string) {
	groups := map[string]model.ProxyGroup{}
	for _, g := range px.Groups {
		groups[g.Name] = g
	}
	chainNames := map[string]bool{profile.ChainGroupName: true, profile.ChainGroupAltName: true}
	for _, ch := range chains {
		chainNames[ch.Name] = true
	}
	// the configuration's order: GLOBAL lists DIRECT, REJECT, the nodes,
	// then the groups; the rest sorted
	var order []string
	seen := map[string]bool{}
	if g, ok := groups["GLOBAL"]; ok {
		for _, n := range g.All {
			if !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}
	var rest []string
	for n := range px.Proxies {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	order = append(order, rest...)

	// the group the chain container goes into (as composition picks it),
	// and every group that may reach it or a chain: not an entry
	first := ""
	for _, g := range px.Groups {
		if g.Type == model.GroupSelect && !g.Hidden && g.Name != "GLOBAL" && !chainNames[g.Name] {
			first = g.Name
			break
		}
	}
	loops := map[string]bool{}
	var reaches func(name string, seen map[string]bool) bool
	reaches = func(name string, seen map[string]bool) bool {
		if chainNames[name] || name == first {
			return true
		}
		g, ok := groups[name]
		if !ok || seen[name] {
			return false
		}
		seen[name] = true
		for _, m := range g.All {
			if reaches(m, seen) {
				return true
			}
		}
		return false
	}
	for name := range groups {
		if reaches(name, map[string]bool{}) {
			loops[name] = true
		}
	}

	for _, n := range order {
		switch n {
		case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL":
			continue
		}
		if profile.IsReservedName(n) || chainNames[n] {
			continue
		}
		if g, isGroup := groups[n]; isGroup {
			if !g.Hidden && !loops[n] {
				entries = append(entries, n)
			}
			continue
		}
		if pr, ok := px.Proxies[n]; ok && pr.Provider == "" {
			entries = append(entries, n)
			nodes = append(nodes, n)
		}
	}
	return entries, nodes
}

// udpTransport reports whether a node of Mihomo type typ carries its own
// traffic over UDP, so the hop before it must pass UDP.
func udpTransport(typ string) bool {
	switch strings.ToLower(typ) {
	case "hysteria", "hysteria2", "tuic", "wireguard":
		return true
	}
	return false
}

// chainHints are the notes on a draft's hops: UDP the previous hop must
// carry, a WireGuard node shared with its original.
func chainHints(hops []string, px model.ProxySnapshot, groups map[string]bool) []string {
	var out []string
	for i := 1; i < len(hops); i++ {
		h, prev := hops[i], hops[i-1]
		if h == "" || prev == "" {
			continue
		}
		typ := px.Proxies[h].Type
		if udpTransport(typ) {
			switch {
			case groups[prev]:
				out = append(out, i18n.Tf("「%s」经 UDP 传输，链路是否可用取决于「%s」当前选择的节点是否支持 UDP。", h, prev))
			case !px.Proxies[prev].UDP:
				out = append(out, i18n.Tf("「%s」经 UDP 传输，但前一跳「%s」不支持 UDP，链路将无法连通。", h, prev))
			}
		}
		if strings.EqualFold(typ, "wireguard") {
			out = append(out, i18n.Tf("「%s」是 WireGuard 节点：链路与原节点共用私钥，同时使用会互相顶掉会话。", h))
		}
	}
	return out
}

// chainPath is how a chain reads: 你 → A → B → 目标网站.
func chainPath(hops []string) string {
	var parts []string
	for _, h := range hops {
		if h == "" {
			h = "…"
		}
		parts = append(parts, h)
	}
	return i18n.T("你") + " → " + strings.Join(parts, " → ") + " → " + i18n.T("目标网站")
}

// chainsDialogView draws the dialog when it is open.
func (p *Page) chainsDialogView(c *ui.Context, px model.ProxySnapshot) {
	d := &p.chains
	if !d.open {
		return
	}
	chains := p.e.API.ProxyChains()
	if d.editing {
		p.chainEditor(c, d, chains, px)
		return
	}
	t := c.Theme()
	k := p.e.Kit
	env.Dialog(c, &d.open, env.DialogOptions{Title: i18n.T("链式代理|对话框"), Width: 520}, func() {
		ui.Column(c.Key(fmt.Sprint("chains", d.session))).Gap(8).Padding(0, 4, 4, 0).Children(func() {
			ui.Text(c, i18n.Tf("让流量依次经过几个节点：入口 → … → 落地。每条链路会成为一个同名的代理组，放进「%s」组，也可以作为规则的目标。", profile.ChainGroupName)).
				FontSize(12).TextColor(t.TextMuted)
			if len(chains) == 0 {
				ui.Box(c).Padding(16).Children(func() {
					ui.Text(c, i18n.T("还没有链路。")).FontSize(13).TextColor(t.TextMuted)
				})
			}
			ui.Scroll(c).MaxHeight(env.DialogMaxHeight(c, 200)).Children(func() {
				ui.Column(c).Gap(6).Children(func() {
					for i, ch := range chains {
						p.chainRow(c, ch, i, chains, px)
					}
				})
			})
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
			}
		})
	}, func() {
		add := ui.Button(c, "").Label(i18n.T("新建链路")).Disabled(d.saving)
		add.Children(func() {
			k.Icon(c, "plus", 13)
			ui.Text(c, i18n.T("新建链路")).SingleLine()
		})
		if add.Clicked() {
			p.newChain("", "")
		}
		ui.Spacer(c)
		if ui.PrimaryButton(c, i18n.T("完成")).Clicked() {
			d.open = false
		}
	})
}

// chainState says whether a chain is in force in the running config.
func chainState(ch model.ProxyChain, px model.ProxySnapshot) (active bool, label, color string) {
	for _, g := range px.Groups {
		if g.Name != ch.Name {
			continue
		}
		if len(g.All) == 1 && profile.IsChainClone(g.All[0]) {
			return true, i18n.T("生效中"), "green"
		}
	}
	if !ch.Enabled {
		return false, i18n.T("已停用"), "gray"
	}
	return false, i18n.T("未生效"), "accent"
}

// chainRow is one chain of the list.
func (p *Page) chainRow(c *ui.Context, ch model.ProxyChain, i int, chains []model.ProxyChain, px model.ProxySnapshot) {
	t := c.Theme()
	k := p.e.Kit
	d := &p.chains
	ui.Row(c.Key("chain:"+ch.ID)).MinHeight(48).Padding(6, 8, 6, 12).Gap(10).AlignItems(ui.Center).Radius(8).
		Background(t.Surface).Border(1, t.Border.Alpha(0.6)).Children(func() {
		ui.Column(c).Gap(3).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, ch.Name).FontSize(13).FontWeight(600).SingleLine().Shrink(1).Tooltip(ch.Name)
				_, label, color := chainState(ch, px)
				tag := ui.Box(c).Children(func() { k.Tag(c, label, color) })
				if label == i18n.T("未生效") {
					tag.Tooltip(i18n.T("当前配置里缺少某一跳，或链路无效；打开编辑查看原因。"))
				}
			})
			path := strings.Join(ch.Hops, " → ")
			ui.Text(c, path).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Tooltip(path)
		})
		on := ch.Enabled
		sw := ui.Switch(c.Key("on"), &on).Label(i18n.Tf("启用 %s", ch.Name)).Disabled(d.saving)
		if sw.Changed() {
			next := model.CloneProxyChains(chains)
			next[i].Enabled = on
			msg := i18n.Tf("已启用「%s」", ch.Name)
			if !on {
				msg = i18n.Tf("已停用「%s」", ch.Name)
			}
			p.applyChains(next, msg, false)
		}
		if ui.Button(c, i18n.T("编辑")).Disabled(d.saving).Clicked() {
			p.editChain(ch)
		}
		ui.Button(c, "").Label(i18n.Tf("更多 %s", ch.Name)).Tooltip(i18n.T("更多")).Children(func() { k.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
			if m.Item(i18n.T("测试延迟")).Disabled(!slices.ContainsFunc(px.Groups, func(g model.ProxyGroup) bool { return g.Name == ch.Name })).Chosen() {
				name := ch.Name
				p.e.Run(func() error { return p.e.API.TestGroupDelay(name) }, "")
			}
			if m.Item(i18n.T("删除…")).Danger().Chosen() {
				p.askDeleteChain(ch, chains)
			}
		})
	})
}

func (p *Page) askDeleteChain(ch model.ProxyChain, chains []model.ProxyChain) {
	p.e.Confirm(i18n.Tf("删除链路「%s」？", ch.Name), i18n.T("正在使用它的代理组会改回之前的选择。被自定义规则使用的链路需先修改规则，或改为停用。"), i18n.T("删除"), true, func() {
		next := slices.DeleteFunc(model.CloneProxyChains(chains), func(x model.ProxyChain) bool { return x.ID == ch.ID })
		p.applyChains(next, i18n.T("链路已删除"), false)
	})
}

// applyChains stores the whole list off the UI thread; close leaves the
// editor (back to the list) when it succeeds.
func (p *Page) applyChains(next []model.ProxyChain, okMsg string, back bool) {
	d := &p.chains
	d.saving, d.err = true, ""
	var err error
	p.e.Then(func() error {
		_, err = p.e.API.SetProxyChains(next)
		return nil
	}, func(error) {
		d.saving = false
		if err != nil {
			d.err = err.Error()
			if !d.open {
				p.e.Toast(err.Error(), true)
			}
			return
		}
		if back {
			d.editing = false
		}
		if okMsg != "" {
			p.e.Toast(okMsg, false)
		}
	})
}

// chainEditor draws the editor of d.draft.
func (p *Page) chainEditor(c *ui.Context, d *chainsDialog, chains []model.ProxyChain, px model.ProxySnapshot) {
	t := c.Theme()
	k := p.e.Kit
	dr := &d.draft
	entries, nodes := chainCandidates(px, chains)
	groups := groupNames(px)
	title := i18n.T("新建链路")
	if dr.id != "" {
		title = i18n.T("编辑链路")
	}
	// a hop chosen before but no longer offered (gone with a subscription
	// update) stays selectable so the user sees it
	withCur := func(opts []string, cur string) []string {
		if cur != "" && !slices.Contains(opts, cur) {
			return append([]string{cur}, opts...)
		}
		return opts
	}
	env.Dialog(c, &d.open, env.DialogOptions{Title: title, Width: 520}, func() {
		ui.Scroll(c.Key(fmt.Sprint("chain-edit", d.session))).MaxHeight(env.DialogMaxHeight(c, 150)).Children(func() {
			ui.Column(c).Gap(10).Padding(0, 4, 4, 0).Children(func() {
				manualField(c, i18n.T("名称"), func() {
					in := ui.TextInput(c.Key("name"), &dr.name).Placeholder(i18n.T("例如：家宽中转 · 东京")).
						Label(i18n.T("链路名称")).FillWidth().ReadOnly(d.saving)
					if dr.id == "" {
						in.AutoFocus()
					}
					if in.Changed() {
						d.err = ""
					}
				})
				last := len(dr.hops) - 1
				for i := range dr.hops {
					i := i
					label := i18n.Tf("中间 %d", i)
					opts := nodes
					switch i {
					case 0:
						label, opts = i18n.T("入口"), entries
					case last:
						label = i18n.T("落地")
					}
					opts = withCur(opts, dr.hops[i])
					ui.Row(c.Key(fmt.Sprint("hop", i))).Gap(6).AlignItems(ui.End).FillWidth().Children(func() {
						ui.Column(c).Grow(1).Shrink(1).Children(func() {
							manualField(c, label, func() {
								sel := ui.Select(c.Key("sel"), &dr.hops[i], opts).Label(label).Disabled(d.saving || len(opts) == 0).FillWidth()
								if sel.Changed() {
									d.err = ""
								}
							})
						})
						up := ui.Button(c, "").Label(i18n.Tf("上移 %s", label)).Tooltip(i18n.T("上移")).Disabled(d.saving || i == 0).Children(func() { k.Icon(c, "up", 13) })
						if up.Clicked() {
							dr.hops[i-1], dr.hops[i] = dr.hops[i], dr.hops[i-1]
						}
						down := ui.Button(c, "").Label(i18n.Tf("下移 %s", label)).Tooltip(i18n.T("下移")).Disabled(d.saving || i == last).Children(func() { k.Icon(c, "down", 13) })
						if down.Clicked() {
							dr.hops[i], dr.hops[i+1] = dr.hops[i+1], dr.hops[i]
						}
						rm := ui.Button(c, "").Label(i18n.Tf("移除 %s", label)).Tooltip(i18n.T("移除这一跳")).
							Disabled(d.saving || len(dr.hops) <= model.ProxyChainMinHops).Children(func() { k.Icon(c, "close", 12) })
						if rm.Clicked() {
							dr.hops = slices.Delete(dr.hops, i, i+1)
						}
					})
				}
				if len(dr.hops) < model.ProxyChainMaxHops {
					add := ui.Button(c, "").Label(i18n.T("添加一跳")).Disabled(d.saving)
					add.Children(func() {
						k.Icon(c, "plus", 13)
						ui.Text(c, i18n.T("添加一跳")).SingleLine()
					})
					if add.Clicked() {
						// before the exit: the new hop is a middle one
						dr.hops = slices.Insert(dr.hops, len(dr.hops)-1, "")
					}
				}
				ui.Text(c, chainPath(dr.hops)).FontSize(12).TextColor(t.Accent).Tooltip(chainPath(dr.hops))
				ui.Text(c, i18n.T("入口可以是节点或代理组；中间与落地只能是配置里的节点（订阅提供方的节点需先放进一个代理组，作为入口使用）。会绕回链路自身的代理组不能做入口。")).
					FontSize(11).TextColor(t.TextMuted)
				for _, h := range chainHints(dr.hops, px, groups) {
					ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
						ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 13) })
						ui.Text(c, h).FontSize(11.5).TextColor(t.Warning).Shrink(1)
					})
				}
				ui.Checkbox(c, &dr.enabled, i18n.T("启用这条链路")).Disabled(d.saving)
				if d.err != "" {
					ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
				}
			})
		})
	}, func() {
		saved := dr.id != "" && slices.ContainsFunc(chains, func(x model.ProxyChain) bool {
			return x.ID == dr.id && x.Name == strings.TrimSpace(dr.name) && slices.Equal(x.Hops, dr.hops) && x.Enabled == dr.enabled
		})
		inForce := saved && slices.ContainsFunc(px.Groups, func(g model.ProxyGroup) bool { return g.Name == dr.name })
		test := ui.Button(c, i18n.T("测试延迟")).Disabled(d.saving || !inForce)
		if !inForce {
			test.Tooltip(i18n.T("保存并生效后才能测试延迟"))
		}
		if test.Clicked() {
			name := dr.name
			p.e.Run(func() error { return p.e.API.TestGroupDelay(name) }, "")
		}
		if inForce {
			for _, g := range px.Groups {
				if g.Name == dr.name && len(g.All) == 1 {
					pr := px.Proxies[g.All[0]]
					k.Delay(c, pr.Delay, pr.Testing)
				}
			}
		}
		ui.Spacer(c)
		if ui.Button(c, i18n.T("返回")).Disabled(d.saving).Clicked() {
			d.editing, d.err = false, ""
		}
		label := i18n.T("保存")
		if d.saving {
			label = i18n.T("保存中…")
		}
		if ui.PrimaryButton(c, label).Disabled(d.saving).Clicked() {
			p.saveChain(chains)
		}
	})
}

// saveChain checks the draft and stores the list with it.
func (p *Page) saveChain(chains []model.ProxyChain) {
	d := &p.chains
	dr := d.draft
	ch := model.ProxyChain{ID: dr.id, Name: strings.TrimSpace(dr.name), Hops: append([]string(nil), dr.hops...), Enabled: dr.enabled}
	if _, err := profile.NormalizeProxyChain(ch); err != nil {
		d.err = chainDraftError(ch)
		return
	}
	next := model.CloneProxyChains(chains)
	if i := slices.IndexFunc(next, func(x model.ProxyChain) bool { return x.ID == ch.ID && ch.ID != "" }); i >= 0 {
		next[i] = ch
	} else {
		next = append(next, ch)
	}
	p.applyChains(next, i18n.Tf("链路「%s」已保存", ch.Name), true)
}

// chainDraftError says in the interface's language what is wrong with a
// draft profile.NormalizeProxyChain refuses.
func chainDraftError(ch model.ProxyChain) string {
	switch {
	case ch.Name == "":
		return i18n.T("请填写链路名称。")
	case profile.IsReservedName(ch.Name):
		return i18n.T("链路名称不能以 LANFLOW- 开头。")
	}
	seen := map[string]bool{}
	for i, h := range ch.Hops {
		h = strings.TrimSpace(h)
		switch {
		case h == "":
			return i18n.Tf("第 %d 跳还没有选择。", i+1)
		case seen[h]:
			return i18n.Tf("「%s」在链路里出现了两次。", h)
		case h == ch.Name:
			return i18n.T("链路不能把自己作为一跳。")
		}
		seen[h] = true
	}
	return i18n.Tf("链路需要 %d–%d 跳。", model.ProxyChainMinHops, model.ProxyChainMaxHops)
}

// chainGroupPanel draws an in-force chain's group: the path of its hops
// and the delay of the whole chain instead of the generated node.
func (p *Page) chainGroupPanel(c *ui.Context, g model.ProxyGroup, ch model.ProxyChain, px model.ProxySnapshot) {
	t := c.Theme()
	k := p.e.Kit
	ui.Row(c.Key("chain-path")).MinHeight(nodeRowH+4).Padding(2, 8, 2, 12).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
			for i, h := range ch.Hops {
				if i > 0 {
					ui.Text(c, "→").FontSize(12).TextColor(t.TextMuted)
				}
				role := i18n.T("中间")
				switch i {
				case 0:
					role = i18n.T("入口")
				case len(ch.Hops) - 1:
					role = i18n.T("落地")
				}
				ui.Box(c).Padding(2, 8).Radius(6).Background(t.Surface).Border(1, t.Border.Alpha(0.6)).Shrink(1).Children(func() {
					ui.Text(c, h).FontSize(12.5).SingleLine().Tooltip(role + " · " + h)
				})
			}
		})
		if len(g.All) == 1 {
			clone := g.All[0]
			pr := px.Proxies[clone]
			dl := ui.ButtonBase(c.Key("delay")).Label(i18n.Tf("重新测速 %s", g.Name)).Tooltip(i18n.T("测试整条链路的延迟")).
				Padding(2, 8).Radius(6).MinWidth(48).AlignItems(ui.Center).Justify(ui.End).Cursor(ui.CursorPointer)
			if dl.Hovered() {
				dl.Background(t.Accent.Alpha(0.14))
			}
			dl.Children(func() { k.Delay(c, pr.Delay, pr.Testing) })
			if dl.Clicked() {
				name := g.Name
				p.e.Run(func() error { return p.e.API.TestGroupDelay(name) }, "")
			}
		}
		if ui.Button(c, i18n.T("编辑")).Clicked() {
			p.editChain(ch)
		}
	})
}

// nodeChainMenu adds the 链式代理 items to a member's context menu.
func (p *Page) nodeChainMenu(m *ui.Menu, g model.ProxyGroup, n string, px model.ProxySnapshot, names map[string]bool) {
	if profile.IsChainClone(n) || n == "DIRECT" || n == "REJECT" || profile.IsReservedName(n) {
		return
	}
	chainNames := map[string]bool{profile.ChainGroupName: true, profile.ChainGroupAltName: true}
	for _, ch := range p.chainList {
		chainNames[ch.Name] = true
	}
	if chainNames[n] {
		return
	}
	switch pr := px.Proxies[n]; {
	case names[n]:
		if m.Item(i18n.T("以此为入口新建链路…")).Chosen() {
			p.newChain(n, "")
		}
	case pr.Provider != "":
		// dialer-proxy cannot name a provider's node: through its group
		ok := g.Name != "GLOBAL" && !chainNames[g.Name]
		if m.Item(i18n.T("以所在代理组为入口新建链路…")).Disabled(!ok).Chosen() {
			p.newChain(g.Name, "")
			p.e.Note(env.Note{Level: env.News, Title: i18n.Tf("订阅提供方的节点不能直接做入口，已改用它所在的代理组「%s」", g.Name)})
		}
	default:
		if m.Item(i18n.T("以此为落地新建链路…")).Chosen() {
			p.newChain("", n)
		}
		if m.Item(i18n.T("以此为入口新建链路…")).Chosen() {
			p.newChain(n, "")
		}
	}
}
