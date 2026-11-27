// The phone shell (docs/phone-ui.md): a top bar with the page's title and
// its actions, the page in one column under it, and a bar of five tabs
// along the bottom — 首页, 代理, 订阅, 活动 (连接 / 地图 / 日志) and 设置,
// whose list pushes the settings' sections and the tools. The desktop
// shell (Shell.View) is untouched; Options.Phone chooses this one.

package view

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/overview"
	"lanflow/internal/view/settings"
)

// The bottom bar's tabs.
const (
	tabHome = iota
	tabProxies
	tabSubs
	tabActivity
	tabSettings
)

// The segments of 活动.
const (
	segConns = iota
	segMap
	segLogs
)

// phoneSettingsPage is the key of 设置's list for its scroll state;
// phoneActivityPage 活动's, which nothing scrolls.
const (
	phoneSettingsPage env.Page = "phone-settings"
	phoneActivityPage env.Page = "phone-activity"
)

// phoneTools are the pages 设置 lists under 工具, in the desktop sidebar's
// order; 抓包 shows once it is turned on, as in the sidebar.
var phoneTools = []env.Page{env.Rules, env.Net, env.Stats, env.Probe, env.Capture, env.Aggregate}

// phoneSettingsTabs are the settings sections 设置 lists: TUN (the VPN is
// the phone's TUN) and Agent (no local API on a phone) are left out.
var phoneSettingsTabs = []string{"general", "proxy", "dns", "sniffer", "override", "kernel", "advanced"}

// phoneTabOf returns the bottom bar's tab of page p.
func phoneTabOf(p env.Page) int {
	switch p {
	case env.Overview:
		return tabHome
	case env.Proxies:
		return tabProxies
	case env.Subs:
		return tabSubs
	case env.Conns, env.Logs:
		return tabActivity
	}
	return tabSettings
}

func (s *Shell) phoneTab() int { return phoneTabOf(s.page) }

// phonePushed reports whether the page shown was pushed from 设置's list,
// or pushed a page of its own, and so shows a back button.
func (s *Shell) phonePushed() bool {
	if _, ok := s.phoneSub(); ok {
		return true
	}
	return s.phoneTab() == tabSettings && !s.phoneHub
}

// phoneSub returns the page shown when it has pushed a page of its own.
func (s *Shell) phoneSub() (env.PhoneStack, bool) {
	if s.phoneHub {
		return nil, false
	}
	p := s.pages[s.page]
	if s.phoneTab() == tabActivity && s.actSeg == segMap {
		return nil, false
	}
	st, ok := p.(env.PhoneStack)
	if !ok {
		return nil, false
	}
	if _, _, ok := st.PhoneSub(); !ok {
		return nil, false
	}
	return st, true
}

// phoneTitle is the page's title, as the top bar shows it.
func (s *Shell) phoneTitle() string {
	if sub, ok := s.phoneSub(); ok {
		title, _, _ := sub.PhoneSub()
		return title
	}
	switch s.phoneTab() {
	case tabHome:
		return i18n.T("首页")
	case tabActivity:
		return i18n.T("活动|标签页")
	}
	switch {
	case s.phoneTab() == tabSettings && s.phoneHub:
		return i18n.T("设置")
	case s.page == env.Settings:
		// A section asked for and not yet shown (the page takes its arg
		// as it builds, after the bar).
		if a := s.args[env.Settings]; a != "" {
			key, _, _ := strings.Cut(a, ":")
			key, _, _ = strings.Cut(key, "#")
			if title := settings.TabTitleOf(key); title != "" {
				return title
			}
		}
		if sp, ok := s.pages[env.Settings].(*settings.Page); ok {
			return sp.TabTitle()
		}
	}
	return env.Title(s.page)
}

// phoneScroll is Env.PhoneScroll: the scroll state of a page's main scroll
// container, kept for the shell's large titles.
func (s *Shell) phoneScroll(p env.Page) *ui.ScrollState {
	if s.scrolls == nil {
		s.scrolls = map[env.Page]*ui.ScrollState{}
	}
	st := s.scrolls[p]
	if st == nil {
		st = &ui.ScrollState{}
		s.scrolls[p] = st
	}
	return st
}

// phoneLarge reports whether what shows starts with a large title (iOS),
// and the key of its scroll state.
func (s *Shell) phoneLarge() (env.Page, bool) {
	if env.Material() {
		return "", false
	}
	if _, sub := s.phoneSub(); sub {
		return "", false
	}
	switch {
	case s.phoneTab() == tabSettings && s.phoneHub:
		return phoneSettingsPage, true
	case s.phoneTab() == tabActivity:
		// 活动's large title stays: its segments and lists scroll under
		// it, each on its own (phoneActivityPage is never scrolled).
		return phoneActivityPage, true
	}
	if lt, ok := s.pages[s.page].(env.PhoneLargeTitle); ok && lt.PhoneLargeTitle() {
		return s.page, true
	}
	return "", false
}

// phoneBack goes back one step: from a page pushed from 设置 to its list,
// from another tab to 首页. It reports false on 首页, where the system's
// back leaves the app.
func (s *Shell) phoneBack() bool {
	if sub, ok := s.phoneSub(); ok {
		sub.PhonePop()
		s.Redraw()
		return true
	}
	switch {
	case s.phonePushed():
		s.phoneShowHub()
	case s.page != env.Overview || s.phoneHub:
		s.phoneHub = false
		s.navigate(env.Overview, "")
	default:
		return false
	}
	return true
}

// phoneShowHub shows 设置's list. The page under it stays the shell's page
// when it is one of 设置's, but frees what it keeps for drawing.
func (s *Shell) phoneShowHub() {
	if h, ok := s.pages[s.page].(interface{ Hidden() }); ok {
		h.Hidden()
	}
	if phoneTabOf(s.page) != tabSettings {
		s.page = env.Settings
	}
	s.phoneHub = true
	s.shown.Store(s.page)
	s.Redraw()
}

// phoneNavigate is navigate's part on a phone: 连接 and 日志 are segments
// of 活动, and a page of 设置 shows itself, not the list.
func (s *Shell) phoneNavigate(p env.Page) {
	switch p {
	case env.Conns:
		s.actSeg = segConns
	case env.Logs:
		s.actSeg = segLogs
	}
	s.phoneHub = false
}

// phoneView draws the window on a phone.
func (s *Shell) phoneView(c *ui.Context) {
	t := c.Theme()
	s.drain(c)
	st := s.api.Status()
	s.sidebarState()
	tab := s.phoneTab()
	bg := t.Background
	ui.Column(c).Fill().Background(bg).Children(func() {
		top := mobile.TopBarOptions{Title: s.phoneTitle(), Background: &bg}
		if key, ok := s.phoneLarge(); ok && s.phoneScroll(key).Y < env.LargeTitleHeight {
			top.Title = "" // the page's large title shows it
		}
		if s.phonePushed() {
			top.Leading = func() { s.phoneBackButton(c) }
		}
		_, sub := s.phoneSub()
		switch {
		case s.phoneHub || sub:
		case tab == tabActivity && s.actSeg == segMap:
			if ov, ok := s.pages[env.Overview].(*overview.Page); ok {
				top.Trailing = func() { ov.PhoneMapActions(c) }
			}
		default:
			if b, ok := s.pages[s.page].(env.PhoneBar); ok {
				top.Trailing = func() { b.PhoneActions(c) }
			}
		}
		phoneTopBar(c, top)
		// Keyed by what shows, so each keeps its own scroll and focus.
		key := string(s.page)
		switch {
		case tab == tabSettings && s.phoneHub:
			key = "phone-settings-list" // not a page's key: "settings" is 设置's page
		case tab == tabActivity:
			key = "phone-activity"
		}
		ui.Column(c.Key("phone:" + key)).Grow(1).Basis(0).MinHeight(0).FillWidth().Clip().Children(func() {
			switch key {
			case "phone-settings-list":
				s.phoneSettings(c)
			case "phone-activity":
				s.phoneActivity(c)
			default:
				if p := s.pages[s.page]; p != nil {
					p.View(c)
				}
			}
		})
		sel := tab
		bar := mobile.BottomBar(c, &sel, s.phoneButtons(st)...).Label(i18n.T("页面"))
		if bar.Changed() {
			switch sel {
			case tabSettings:
				// 设置 opens on its list, as a tab's stack returns to its
				// root.
				s.phoneShowHub()
			case tabActivity:
				// Back on the segment it showed last.
				seg := s.actSeg
				if seg == segLogs {
					s.navigate(env.Logs, "")
				} else {
					s.navigate(env.Conns, "")
				}
				s.actSeg = seg
			default:
				s.navigate([]env.Page{env.Overview, env.Proxies, env.Subs}[sel], "")
			}
		}
	})
	// Android's back: only taken while there is somewhere to go back to, so
	// that on 首页 it leaves the app.
	if (s.page != env.Overview || s.phoneHub) && c.Shortcut(0, ui.KeyBack) {
		s.phoneBack()
	}
	s.dialogs(c, st)
	s.toastView(c)
}

// phoneBackButton is the top bar's back button of a pushed page.
func (s *Shell) phoneBackButton(c *ui.Context) {
	t := c.Theme()
	btn := ui.ButtonBase(c.Key("phone:back")).Row().Height(env.TouchSize).MinWidth(env.TouchSize).PaddingX(8).Gap(2).AlignItems(ui.Center).
		Label(i18n.T("返回")).TextColor(t.Accent)
	btn.Children(func() {
		ui.Icon(c, icons["chevron"]).Size(18, 18).Rotate(180)
		if !env.Material() { // iOS names where it goes; Android's arrow alone
			parent := i18n.T("设置")
			if sub, ok := s.phoneSub(); ok {
				_, parent, _ = sub.PhoneSub()
			}
			ui.Text(c, parent).FontSize(17).TextColor(t.Accent).SingleLine().MaxWidth(110)
		}
	})
	if btn.Clicked() {
		s.phoneBack()
	}
}

// phoneButtons are the bottom bar's tabs.
func (s *Shell) phoneButtons(st model.Status) []mobile.BarButton {
	btn := func(label, icon string) mobile.BarButton {
		svg := icons[icon]
		return mobile.BarButton{Label: label, Icon: func(p *ui.Painter, r ui.Rect, color ui.Color) { p.Icon(svg, r, color) }}
	}
	act := btn(i18n.T("活动|标签页"), "conns")
	if st.Connections > 0 && st.Core == model.CoreRunning {
		act.Badge = fmt.Sprint(st.Connections)
		if st.Connections > 99 {
			act.Badge = "99+"
		}
	}
	set := btn(i18n.T("设置"), "settings")
	if s.sbCap.Enabled {
		set.Badge = "•" // HTTPS decryption runs (docs/capture.md §7.2)
	}
	return []mobile.BarButton{
		btn(i18n.T("首页"), "overview"),
		btn(env.Title(env.Proxies), "proxies"),
		btn(env.Title(env.Subs), "subs"),
		act,
		set,
	}
}

// phoneActivity is 活动: 连接, the exit map and 日志, under a segmented
// control.
func (s *Shell) phoneActivity(c *ui.Context) {
	seg := s.actSeg
	_, sub := s.phoneSub()
	if !sub && !env.Material() {
		ui.Box(c).PaddingX(env.PhoneGap).Shrink(0).Children(func() { env.LargeTitle(c, i18n.T("活动|标签页")) })
	}
	ui.Row(c).Padding(4, env.PhoneGap, 10, env.PhoneGap).Shrink(0).Hidden(sub).Children(func() {
		sc := env.Segmented(c, "activity", &seg, i18n.T("连接"), i18n.T("地图"), i18n.T("日志")).Label(i18n.T("活动|标签页"))
		if sc.Changed() {
			s.actSeg = seg
			switch seg {
			case segConns:
				s.navigate(env.Conns, "")
			case segLogs:
				s.navigate(env.Logs, "")
			default:
				s.Redraw()
			}
		}
	})
	ui.Column(c.Key(fmt.Sprint("act:", s.actSeg))).Grow(1).Basis(0).MinHeight(0).FillWidth().Children(func() {
		switch s.actSeg {
		case segMap:
			// The map reads the exits: the overview's topics rebuild it.
			s.shown.Store(env.Overview)
			if ov, ok := s.pages[env.Overview].(*overview.Page); ok {
				ov.PhoneMap(c)
			}
		case segLogs:
			s.shown.Store(env.Logs)
			s.pages[env.Logs].View(c)
		default:
			s.shown.Store(env.Conns)
			s.pages[env.Conns].View(c)
		}
	})
}

// phoneSettings is 设置's list: the settings' sections, then the tools.
func (s *Shell) phoneSettings(c *ui.Context) {
	t := c.Theme()
	icon := func(name string, col ui.Color) func() {
		return func() {
			ui.Box(c).Size(30, 30).Radius(8).Background(col).TextColor(ui.RGB(255, 255, 255)).Center().Shrink(0).Children(func() {
				ui.Icon(c, icons[name]).Size(18, 18)
			})
		}
	}
	s.env.Scroll(c, phoneSettingsPage).Fill().Children(func() {
		ui.Column(c).FillWidth().Gap(24).Padding(0, 0, 24, 0).Children(func() {
			ui.Box(c).PaddingX(env.PhoneGap).Children(func() { env.LargeTitle(c, i18n.T("设置")) })
			mobile.Section(c, "", func() {
				for _, key := range phoneSettingsTabs {
					o := mobile.RowOptions{Title: settings.TabTitleOf(key), Leading: icon(settingsIcons[key], settingsColor(t, key)), Disclosure: true}
					if mobile.ListRow(c.Key("set:"+key), o).Clicked() {
						s.navigate(env.Settings, key)
					}
				}
			})
			mobile.Section(c, i18n.T("工具"), func() {
				for _, p := range phoneTools {
					if p == env.Capture && !s.sbCapOn && !s.sbCap.Enabled {
						continue
					}
					o := mobile.RowOptions{Title: env.Title(p), Leading: icon(pageIcons[string(p)], t.Accent), Disclosure: true}
					if p == env.Capture && s.sbCap.Enabled {
						o.Trailing = func() { s.env.Kit.Tag(c, i18n.T("解密中"), "orange") }
					}
					if mobile.ListRow(c.Key("tool:"+string(p)), o).Clicked() {
						s.navigate(p, "")
					}
				}
			})
		})
	})
}

// settingsIcons are the icons of 设置's rows.
var settingsIcons = map[string]string{
	"general": "settings", "proxy": "proxies", "dns": "net", "sniffer": "search",
	"override": "doc", "kernel": "power", "advanced": "bolt",
}

// settingsColor is the tile behind a section's icon, as iOS's Settings
// colors its rows.
func settingsColor(t *ui.Theme, key string) ui.Color {
	switch key {
	case "general":
		return ui.RGB(142, 142, 147)
	case "proxy", "dns":
		return t.Accent
	case "sniffer", "override":
		return ui.RGB(88, 86, 214)
	case "kernel":
		return t.Success
	}
	return t.Warning
}

// phoneTopBar is the top bar: 44 pt with the title centered on iOS, 56 pt
// with it at the start on Material, as mobile.TopBar draws them, but with
// no room kept for a leading button that is not there (Material's title
// then starts at the margin) and the actions at the trailing end.
func phoneTopBar(c *ui.Context, o mobile.TopBarOptions) {
	material := env.Material()
	height, size := float32(44), float32(17)
	if material {
		height, size = 56, 22
	}
	ui.Row(c).FillWidth().Height(height).Shrink(0).Background(*o.Background).AlignItems(ui.Center).PaddingX(4).Role(ui.RoleGroup).Children(func() {
		lead := ui.Row(c).AlignItems(ui.Center)
		if !material || o.Leading != nil {
			lead.MinWidth(88) // iOS: room for 〈 设置, the title centered
		}
		if material && o.Leading == nil {
			lead.Width(env.PhoneGap - 4)
		}
		lead.Children(func() {
			if o.Leading != nil {
				o.Leading()
			}
		})
		title := ui.Row(c).Grow(1).Basis(0).AlignItems(ui.Center).MinWidth(0)
		if !material {
			title.Justify(ui.Center)
		}
		title.Children(func() {
			if o.Title != "" {
				ui.Text(c, o.Title).FontSize(size).FontWeight(600).SingleLine().Role(ui.RoleHeading).Level(1)
			}
		})
		trail := ui.Row(c).AlignItems(ui.Center).Justify(ui.End)
		if !material {
			trail.MinWidth(88)
		}
		trail.Children(func() {
			if o.Trailing != nil {
				o.Trailing()
			}
		})
	})
}
