// 首页 on a phone (docs/phone-ui.md): a large round connect button with the
// VPN's state, time and speeds under it, the mode, the node in use and
// the subscription. The exit map is a page of 活动 of its own (PhoneMap).

package overview

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

// connectSize is the diameter of 首页's connect button.
const connectSize = 124

// PhoneLargeTitle: 首页 starts with iOS's large title.
func (p *Page) PhoneLargeTitle() bool { return true }

// phoneView is the page on a phone.
func (p *Page) phoneView(c *ui.Context, st model.Status) {
	p.e.Scroll(c, env.Overview).Fill().Children(func() {
		ui.Column(c).Padding(0, env.PhoneGap, 24, env.PhoneGap).Gap(env.PhoneGap).Children(func() {
			env.LargeTitle(c, i18n.T("首页"))
			p.connect(c, st)
			p.phoneBanners(c, st)
			p.phoneMode(c, st)
			p.nodeCard(c, st)
			p.phoneSubCard(c)
		})
	})
}

// vpnOn is the connect button's state: the core runs or starts, or what
// the user just asked for while that is under way.
func (p *Page) vpnOn(st model.Status) bool {
	p.mu.Lock()
	pc := p.pendingCore
	p.mu.Unlock()
	if pc != nil {
		return *pc
	}
	return st.Core == model.CoreRunning || st.Core == model.CoreStarting
}

// connect is the round button with the state, the time connected and the
// speeds under it.
func (p *Page) connect(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := p.e.Kit
	on := p.vpnOn(st)
	starting := st.Core == model.CoreStarting || (on && st.Core != model.CoreRunning && st.Core != model.CoreError)
	col := t.TextMuted.Alpha(0.55)
	switch {
	case st.Core == model.CoreError || st.Core == model.CoreMissing:
		col = t.Danger
	case starting:
		col = t.Accent
	case st.Core == model.CoreRunning:
		col = t.Success
	}
	label := i18n.T("连接 VPN")
	if on {
		label = i18n.T("断开 VPN")
	}
	ui.Column(c).AlignItems(ui.Center).Gap(10).Padding(12, 0, 4, 0).Children(func() {
		// The ring breathes around the button while the VPN comes up.
		ring := ui.Box(c).Size(connectSize+40, connectSize+40).Center()
		if starting && !c.Preferences().ReduceMotion {
			ph := float32(time.Now().UnixMilli()%1400) / 1400
			ring.Draw(func(pt *ui.Painter, r ui.Rect) {
				var path ui.Path
				path.Circle(r.X+r.W/2, r.Y+r.H/2, connectSize/2+4+14*ph)
				pt.StrokePath(&path, 3, t.Accent.Alpha(0.5*(1-ph)))
			})
			c.AnimationFrame()
		}
		ring.Children(func() {
			btn := ui.ButtonBase(c.Key("connect")).Size(connectSize, connectSize).Radius(connectSize/2).Center().
				Background(col).TextColor(ui.RGB(255, 255, 255)).Label(label).Role(ui.RoleSwitch).Checked(on).
				Disabled(st.Core == model.CoreMissing).Cursor(ui.CursorPointer).
				Shadow(0, 6, 18, 0, col.Alpha(0.35))
			if btn.Pressed() {
				btn.Scale(0.96, 0.96)
			}
			btn.Children(func() { k.Icon(c, "power", 46) })
			if btn.Clicked() {
				p.setVPN(!on)
			}
		})
		ui.Text(c, vpnTitle(st, on)).FontSize(20).FontWeight(600).TextAlign(ui.Center)
		p.vpnDetail(c, st)
	})
}

// phoneBanners are what needs the user under the button: the core file
// missing, and the config sync. A crash shows under the button, its log a
// tap away (vpnDetail).
func (p *Page) phoneBanners(c *ui.Context, st model.Status) {
	if st.Core == model.CoreMissing {
		p.missingBanner(c, st)
	}
	if cs := p.e.API.ConfigSyncStatus(); cs.Error != "" || len(cs.Conflicts) > 0 || cs.Locked {
		ui.Column(c).Margin(0, -12).Children(func() {
			st := st
			st.Core = model.CoreRunning // only the sync's banner
			p.coreBanners(c, st)
		})
	}
}

// vpnTitle is the line under the button.
func vpnTitle(st model.Status, on bool) string {
	switch {
	case st.Core == model.CoreError:
		return i18n.T("连接出错")
	case st.Core == model.CoreMissing:
		return i18n.T("未找到内核")
	case st.Core == model.CoreRunning:
		return i18n.T("已连接")
	case st.Core == model.CoreStarting || on:
		return i18n.T("正在连接…")
	}
	return i18n.T("未连接")
}

// vpnDetail is what follows the state: the time connected and the speeds,
// the error, or what a tap does.
func (p *Page) vpnDetail(c *ui.Context, st model.Status) {
	t := c.Theme()
	switch st.Core {
	case model.CoreRunning:
		if !st.StartedAt.IsZero() { // the time alone: 已连接 is above it
			ui.Text(c, i18n.Tf("已运行 %s", env.FmtUptime(time.Since(st.StartedAt)))).FontSize(14).TextColor(t.TextMuted).FontFeatures("tnum")
		}
		ui.Row(c).Gap(24).Margin(4, 0, 0, 0).Children(func() {
			speed(c, "↑", st.Up, t.TextMuted)
			speed(c, "↓", st.Down, t.Accent)
		})
	case model.CoreError:
		if st.CoreError != "" {
			ui.Text(c, st.CoreError).FontSize(14).TextColor(t.Danger).TextAlign(ui.Center).MaxLines(3).MaxWidth(320)
		}
		logs := ui.ButtonBase(c.Key("see-logs")).Height(env.TouchSize).PaddingX(12).Center().Label(i18n.T("查看日志")).Cursor(ui.CursorPointer)
		logs.Children(func() { ui.Text(c, i18n.T("查看日志")).FontSize(15).TextColor(t.Accent) })
		if logs.Clicked() {
			p.e.Navigate(env.Logs, "")
		}
	case model.CoreStarting:
	default:
		ui.Text(c, i18n.T("轻点按钮连接")).FontSize(14).TextColor(t.TextMuted)
	}
}

// speed is one of the live speeds under the button.
func speed(c *ui.Context, arrow string, v int64, col ui.Color) {
	t := c.Theme()
	ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
		ui.Text(c, arrow).FontSize(15).TextColor(col)
		ui.Text(c, env.FmtSpeed(v)).FontSize(17).FontWeight(500).TextColor(t.Text).FontFeatures("tnum").SingleLine()
	})
}

// phoneMode is the 规则 / 全局 / 直连 control, the screen's width.
func (p *Page) phoneMode(c *ui.Context, st model.Status) {
	p.mu.Lock()
	pm := p.pendingMode
	p.mu.Unlock()
	mode := st.Mode
	if pm != nil {
		mode = *pm
	}
	idx := modeIndex(mode)
	if env.Segmented(c, "mode", &idx, i18n.T("规则"), i18n.T("全局"), i18n.T("直连")).Label(i18n.T("模式")).Changed() {
		p.setMode(modes[idx])
	}
}

// nodeCard is the node the main group uses, followed through the groups
// it picks: its name, protocol and latency. A tap opens the group in 代理.
func (p *Page) nodeCard(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := p.e.Kit
	px := p.e.API.Proxies()
	group := px.MainGroup
	node, path := mainNode(px)
	cd := env.Clickable(env.PhoneCard(c).Gap(6).MinHeight(env.TouchSize), i18n.T("当前节点，打开代理页"))
	cd.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("当前节点")).FontSize(13).TextColor(t.TextMuted).Grow(1)
			if group != "" {
				ui.Text(c, group).FontSize(13).TextColor(t.TextMuted).SingleLine().Shrink(1)
			}
			ui.Text(c, " ›").FontSize(15).TextColor(t.TextMuted)
		})
		if node == "" || st.Core != model.CoreRunning {
			ui.Text(c, "—").FontSize(20).FontWeight(600)
			return
		}
		pr := px.Proxies[node]
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, node).FontSize(20).FontWeight(600).SingleLine().Shrink(1).Grow(1)
			k.Delay(c, pr.Delay, pr.Testing)
		})
		meta := pr.Type
		if len(path) > 1 {
			meta = strings.Join(path[1:], " › ") + " · " + meta
		}
		ui.Text(c, meta).FontSize(13).TextColor(t.TextMuted).SingleLine()
	})
	if cd.Clicked() {
		p.e.Navigate(env.Proxies, group)
	}
}

// mainNode follows the main group's choice through the groups it picks to
// a node: the node, and the groups on the way (the main group first).
func mainNode(px model.ProxySnapshot) (string, []string) {
	groups := map[string]model.ProxyGroup{}
	for _, g := range px.Groups {
		groups[g.Name] = g
	}
	name := px.MainGroup
	var path []string
	for range 8 {
		g, ok := groups[name]
		if !ok {
			return name, path
		}
		path = append(path, g.Name)
		if g.Now == "" {
			return "", path
		}
		name = g.Now
	}
	return name, path
}

// phoneSubCard is the subscription in use: its name, the traffic used and
// the expiry. A tap opens 订阅.
func (p *Page) phoneSubCard(c *ui.Context) {
	t := c.Theme()
	var cur *model.Subscription
	for _, s := range p.e.API.Subscriptions() {
		if s.Current {
			cp := s
			cur = &cp
			break
		}
	}
	cd := env.Clickable(env.PhoneCard(c).Gap(6), i18n.T("当前订阅，打开订阅页"))
	cd.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("当前订阅")).FontSize(13).TextColor(t.TextMuted).Grow(1)
			ui.Text(c, " ›").FontSize(15).TextColor(t.TextMuted)
		})
		if cur == nil {
			ui.Text(c, i18n.T("尚未选择订阅")).FontSize(15).TextColor(t.TextMuted)
			return
		}
		ui.Text(c, cur.Name).FontSize(20).FontWeight(600).SingleLine()
		used := cur.Upload + cur.Download
		ui.Row(c).Gap(8).Children(func() {
			traffic := i18n.T("无流量信息")
			if cur.Total > 0 {
				traffic = i18n.Tf("已用 %s / %s GB", env.FmtGB(used), env.FmtGB(cur.Total))
			}
			ui.Text(c, traffic).FontSize(13).TextColor(t.TextMuted).FontFeatures("tnum").Grow(1).SingleLine()
			if !cur.Expire.IsZero() {
				txt, col := expiryText(cur.Expire, time.Now(), t)
				ui.Text(c, txt).FontSize(13).TextColor(col).FontFeatures("tnum").SingleLine()
			}
		})
		if cur.Total > 0 {
			frac := min(1, float64(used)/float64(cur.Total))
			bar := ui.Progress(c, frac).Label(i18n.T("已用流量")).Height(6)
			if frac > 0.85 {
				bar.TextColor(t.Warning)
			}
		}
	})
	if cd.Clicked() {
		p.e.Navigate(env.Subs, "")
	}
}

// setVPN turns the system VPN on or off. On a phone the core only runs in
// the VPN (the tunnel's runner points TUN at its descriptor), so the VPN
// is the core: on starts it, off stops it. The settings' TUN means nothing
// there. The system asks the user to allow the VPN the first time (the
// tunnel does); a failed start shows its error as a toast.
func (p *Page) setVPN(on bool) {
	p.setPending(&p.pendingCore, on)
	if on {
		p.run(p.e.API.StartCore, i18n.T("VPN 已连接"), &p.pendingCore)
		return
	}
	p.run(p.e.API.StopCore, i18n.T("VPN 已断开"), &p.pendingCore)
}

// PhoneMap is 活动's 地图: the exit globe the screen's width, turned and
// pinched with fingers, and the routes under it as a grouped list. The
// segmented control above names it: no card, no title.
func (p *Page) PhoneMap(c *ui.Context) {
	st := p.e.API.Status()
	run := st.Core == model.CoreRunning
	direct := st.Mode == model.ModeDirect
	rs := p.routes(time.Now())
	you, guessed := p.youPlace()
	var dests map[string][]dest
	if run && !direct {
		dests = p.destinations(rs, st.Mode, time.Now())
	}
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		_, h := c.Size()
		ui.Box(c).Height(max(240, h*0.42)).Shrink(0).FillWidth().Background(t.Surface).Children(func() {
			p.mapBox(c, rs, dests, you, guessed, run, direct).Fill()
		})
		ui.Scroll(c.Key("routes")).Grow(1).Basis(0).MinHeight(0).Children(func() {
			ui.Column(c).Padding(16, env.PhoneGap, 24, env.PhoneGap).Gap(8).Children(func() {
				p.phoneRoutes(c, st, rs, run, direct)
			})
		})
	})
}

// PhoneMapActions are 地图's top bar: 实测出口 as a refresh icon, a spinner
// while it measures.
func (p *Page) PhoneMapActions(c *ui.Context) {
	st := p.e.API.Status()
	if st.Core != model.CoreRunning || st.Mode == model.ModeDirect {
		return
	}
	p.mu.Lock()
	probing := p.probing
	p.mu.Unlock()
	if probing {
		ui.Box(c).Size(env.TouchSize, env.TouchSize).Center().Children(func() {
			ui.Spinner(c).Size(18, 18).Label(i18n.T("实测中…"))
		})
		return
	}
	if env.IconButton(c, p.e.Kit, "refresh", i18n.T("实测出口")).Clicked() {
		p.probe()
	}
}

// phoneRoutes are the routes as grouped list rows: 当前出口, then 其他路由;
// a tap opens the group in 代理.
func (p *Page) phoneRoutes(c *ui.Context, st model.Status, rs []route, run, direct bool) {
	t := c.Theme()
	k := p.e.Kit
	note := func(s string) {
		ui.Text(c, s).FontSize(15).TextColor(t.TextMuted).TextAlign(ui.Center).Padding(24, 0)
	}
	switch {
	case !run:
		note(i18n.T("内核未启动"))
		return
	case direct:
		note(i18n.T("直连模式：流量不经过节点，无出口可显示"))
		return
	case len(rs) == 0:
		note(i18n.T("暂无路由"))
		return
	}
	px := p.e.API.Proxies()
	chains := p.e.API.ProxyChains()
	group := func(title string, rs []route) {
		ui.Text(c, title).FontSize(13).TextColor(t.TextMuted).Padding(8, 14, 0, 14)
		ui.Column(c).FillWidth().Radius(env.PhoneRadius).Background(t.Surface).Clip().Dividers(0.5, t.Border).Children(func() {
			for _, rt := range rs {
				name := profile.DisplayName(rt.Node, chains)
				if rt.place != nil {
					name = env.FlagPrefixFor(p.e.Mac, rt.place.Country) + name
				}
				city := i18n.T("位置未知")
				if rt.place != nil {
					city = rt.place.City
				}
				sub := strings.Join(rt.Groups, " · ") + " · " + city
				if rt.place != nil && rt.Exit.Source == model.LocateProbe {
					sub += " · " + i18n.T("实测")
				}
				sub += " · " + i18n.Tf("%s · %d 个连接", env.FmtSpeed(rt.Speed), rt.Connections)
				row := ui.ButtonBase(c.Key("route:"+rt.Group)).Row().MinHeight(56).Padding(8, 14).Gap(10).AlignItems(ui.Center).FillWidth().
					Label(i18n.Tf("%s：%s，在代理页查看", strings.Join(rt.Groups, " · "), rt.Node)).Cursor(ui.CursorPointer)
				if row.Pressed() {
					row.Background(t.SurfaceHover)
				}
				g := rt.Group
				row.Children(func() {
					ui.Box(c).Size(10, 10).Radius(3).Background(routeColor(rt, t)).Shrink(0)
					ui.Column(c).Gap(2).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
						ui.Text(c, name).FontSize(15).FontWeight(600).SingleLine()
						ui.Text(c, sub).FontSize(12).TextColor(t.TextMuted).MaxLines(2)
						if n, col := pathNote(rt.Exit, t); n != "" {
							ui.Text(c, n).FontSize(12).TextColor(col)
						}
					})
					pr := px.Proxies[rt.Node]
					k.Delay(c, pr.Delay, pr.Testing)
				})
				if row.Clicked() {
					p.e.Navigate(env.Proxies, g)
				}
			}
		})
	}
	group(i18n.T("当前出口"), rs[:1])
	if len(rs) > 1 {
		group(i18n.T("其他路由"), rs[1:])
	}
}
