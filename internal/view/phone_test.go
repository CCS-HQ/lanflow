package view

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/settings"
	"lanflow/internal/view/subs"
)

// statusAPI is the fake with its core's state replaced while core is
// set: states the instant fake passes through at once (starting) or
// cannot reach (an error), for 首页's snapshots and tests.
type statusAPI struct {
	*fake.Fake
	core *model.CoreState
	err  string
}

func (a *statusAPI) Status() model.Status {
	st := a.Fake.Status()
	if a.core != nil {
		st.Core, st.CoreError = *a.core, a.err
	}
	return st
}

// phoneStatus is the statusAPI of the phone tests' shells, by the shell.
var phoneStatus = map[*Shell]*statusAPI{}

// setCore shows the core in state c (with err) on s's page.
func setCore(s *Shell, c model.CoreState, err string) {
	a := phoneStatus[s]
	a.core, a.err = &c, err
}

// newPhoneTest is newTest for the phone shell, on an iPhone's screen.
func newPhoneTest(t *testing.T) (*fake.Fake, *Shell, *ui.Tester) {
	t.Helper()
	api := fake.NewInstant()
	sa := &statusAPI{Fake: api}
	s := New(sa, Options{Phone: true, Sync: true})
	phoneStatus[s] = sa
	t.Cleanup(func() { delete(phoneStatus, s) })
	t.Cleanup(s.Close)
	s.pages[env.Subs].(*subs.Page).SetAsync(func(fn func()) { fn() }, func() (string, bool) { return "/tmp/test-config.yaml", true })
	tt := ui.NewTester(s.View, 390, 844)
	tt.SetInsets(47, 0, 34, 0)
	return api, s, tt
}

// TestPhoneConns checks the 连接 page on a phone: a list in place of the
// table without the topology, the detail in place of the list, and its
// back button.
func TestPhoneConns(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	s.navigate(env.Conns, "")
	tt.Frame()
	if tt.HasText(i18n.T("拓扑")) || tt.HasText(i18n.T("规则载荷")) {
		t.Fatalf("topology toggle or detail on the phone's list: %q", tt.Texts())
	}
	// The fake's connections come in no fixed order: the first row shown.
	host := ""
	for _, txt := range tt.Texts() {
		if strings.HasSuffix(txt, ":443") {
			host = txt
			break
		}
	}
	r, ok := tt.Find(host)
	if !ok {
		t.Fatalf("no connection rows: %q", tt.Texts())
	}
	tapAt(tt, r)
	if !tt.HasText(i18n.T("规则载荷")) || s.phoneTitle() != host {
		t.Fatalf("no detail after a tap (title %q): %q", s.phoneTitle(), tt.Texts())
	}
	if tt.HasText(i18n.T("搜索连接")) || tt.HasText(i18n.T("日志")) {
		t.Fatalf("the list's search or 活动's segments show over the detail: %q", tt.Texts())
	}
	// The top bar's back closes it; the list's actions are in the top bar.
	click(t, tt, i18n.T("返回"))
	tt.Frame()
	if tt.HasText(i18n.T("规则载荷")) || !tt.HasText(i18n.T("搜索连接")) {
		t.Fatalf("back does not return to the list: %q", tt.Texts())
	}
	for _, a := range []string{"筛选连接", "暂停", "更多"} {
		if _, ok := tt.Access(i18n.T(a)); !ok {
			t.Errorf("no %s in the top bar", a)
		}
	}
	// One segmented control: 连接 / 地图 / 日志, not 活动 / 已关闭 under it.
	if tt.HasText(i18n.T("已关闭")) {
		t.Fatalf("活动 / 已关闭 shows as a second segmented control: %q", tt.Texts())
	}
}

// TestPhoneLogs checks that the 日志 page on a phone wraps its lines
// rather than scrolling sideways.
func TestPhoneLogs(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	s.navigate(env.Logs, "")
	tt.Frame()
	if !tt.HasText(i18n.T("搜索日志")) {
		t.Fatalf("no toolbar: %q", tt.Texts())
	}
	for _, txt := range tt.Texts() {
		if r, ok := tt.Find(txt); ok && r.X+r.W > 390+1 {
			t.Fatalf("%q runs off the screen: %+v", txt, r)
		}
	}
}

// TestPhoneTabs walks the bottom bar: 首页, 代理, 订阅, 活动 (with its
// segments) and 设置, whose list pushes a page with a back button, and
// Android's back.
func TestPhoneTabs(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	if s.Page() != env.Overview || !tt.HasText(i18n.T("已连接")) {
		t.Fatalf("starts on %s: %q", s.Page(), tt.Texts())
	}
	// The desktop's sidebar, search and system proxy are not there.
	if tt.HasText(i18n.T("全局搜索")) || tt.HasText(i18n.T("系统代理")) {
		t.Fatalf("desktop parts on the phone: %q", tt.Texts())
	}
	for _, tab := range []struct {
		label string
		page  env.Page
	}{{"代理", env.Proxies}, {"订阅", env.Subs}, {"活动", env.Conns}, {"首页", env.Overview}} {
		tapTab(t, tt, i18n.T(tab.label))
		if s.Page() != tab.page {
			t.Fatalf("tab %s shows %s", tab.label, s.Page())
		}
	}

	// 活动: 连接, 地图 and 日志 under one segmented control.
	tapTab(t, tt, i18n.T("活动|标签页"))
	click(t, tt, i18n.T("日志"))
	tt.Frame()
	if s.Page() != env.Logs || !tt.HasText(i18n.T("搜索日志")) {
		t.Fatalf("活动 › 日志: page %s, %q", s.Page(), tt.Texts())
	}
	click(t, tt, i18n.T("地图"))
	tt.Frame()
	if !tt.HasText(i18n.T("出口地图")) || s.actSeg != segMap {
		t.Fatalf("活动 › 地图: %q", tt.Texts())
	}
	if tt.HasText(i18n.T("放大")) || tt.HasText("+") {
		t.Fatal("the phone's map has zoom buttons")
	}
	// Leaving and coming back keeps the segment.
	tapTab(t, tt, i18n.T("首页"))
	tapTab(t, tt, i18n.T("活动|标签页"))
	if s.actSeg != segMap {
		t.Fatalf("活动 forgot its segment: %d", s.actSeg)
	}
	click(t, tt, i18n.T("连接"))
	tt.Frame()
	if s.Page() != env.Conns {
		t.Fatalf("活动 › 连接: page %s", s.Page())
	}

	tapTab(t, tt, i18n.T("设置"))
	if !s.phoneHub || !tt.HasText(i18n.T("工具")) || !tt.HasText(env.Title(env.Rules)) || !tt.HasText(settingsTitle("dns")) {
		t.Fatalf("设置 shows no list: %q", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, i18n.T("返回")); ok {
		t.Fatal("设置's list has a back button")
	}
	phoneClick(t, tt, env.Title(env.Stats))
	if s.Page() != env.Stats || s.phoneHub {
		t.Fatalf("统计 from 设置: page %s hub %v", s.Page(), s.phoneHub)
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, i18n.T("返回")); !ok {
		t.Fatalf("a pushed page has no back button: %q", tt.Texts())
	}
	// 设置 stays the chosen tab under a pushed page.
	if info, ok := tt.AccessByRole(ui.RoleTab, i18n.T("设置")); !ok || !info.Selected {
		t.Fatalf("设置 is not chosen under 统计: %+v", info)
	}
	click(t, tt, i18n.T("返回"))
	tt.Frame()
	if !s.phoneHub || !tt.HasText(i18n.T("工具")) {
		t.Fatalf("back does not return to 设置: %q", tt.Texts())
	}

	// Android's back: from a pushed page to 设置, from 设置 to 首页, and on
	// 首页 the system's (it leaves the app).
	phoneClick(t, tt, env.Title(env.Rules))
	tt.Key(0, ui.KeyBack)
	tt.Frame()
	if !s.phoneHub {
		t.Fatalf("back from 规则: page %s hub %v", s.Page(), s.phoneHub)
	}
	tt.Key(0, ui.KeyBack)
	tt.Frame()
	if s.phoneHub || s.Page() != env.Overview {
		t.Fatalf("back from 设置: page %s hub %v", s.Page(), s.phoneHub)
	}
	if s.phoneBack() {
		t.Fatal("back on 首页 is taken by the app")
	}
}

// tapTab taps a tab of the bottom bar with a finger.
func tapTab(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleTab, label)
	if !ok {
		t.Fatalf("no tab %q: %q", label, tt.Texts())
	}
	x, y := info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2
	tt.FingerDown(x, y)
	tt.FingerUp(x, y)
	tt.Frame()
}

// TestPhoneSettingsSections opens the settings' sections from 设置: the
// top bar names each, and what a phone has not is left out.
func TestPhoneSettingsSections(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	tapTab(t, tt, i18n.T("设置"))
	if tt.HasText("TUN") || tt.HasText("Agent") {
		t.Fatalf("设置 lists TUN or Agent: %q", tt.Texts())
	}
	hidden := map[string][]string{
		"general": {"开机时启动", "启动后自动开启系统代理", "启动时不显示窗口", "关闭窗口时", "全局快捷键", "配置文件夹", "应用更新"},
		"proxy":   {"允许局域网连接", "不走代理的地址"},
		"kernel":  {"内核更新", "安装其他版本…", "内核文件"},
	}
	for _, key := range phoneSettingsTabs {
		s.phoneShowHub()
		tt.Frame()
		title := settingsTitle(key)
		phoneClick(t, tt, title)
		if s.Page() != env.Settings || s.phoneTitle() != title {
			t.Fatalf("%s: page %s title %q", key, s.Page(), s.phoneTitle())
		}
		if tt.HasText(i18n.T("设置分栏")) {
			t.Fatalf("%s: the desktop's tabs show", key)
		}
		shown := map[string]string{"general": "外观", "proxy": "代理端口", "dns": "DNS 设置来源", "kernel": "日志级别", "advanced": "HTTP 端口"}
		if want, ok := shown[key]; ok && !tt.HasText(i18n.T(want)) {
			t.Fatalf("%s does not show %q: %q", key, want, tt.Texts())
		}
		for _, txt := range hidden[key] {
			if envtestHasScrolled(tt, i18n.T(txt)) {
				t.Errorf("%s shows %q on a phone", key, txt)
			}
		}
	}
}

// TestPhoneVPNSwitch taps 首页's connect button: on starts the core (it
// runs only in the VPN), off stops it; the settings' TUN is left alone.
func TestPhoneVPNSwitch(t *testing.T) {
	api, _, tt := newPhoneTest(t)
	if err := api.StopCore(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(i18n.T("未连接")) {
		t.Fatalf("stopped core: %q", tt.Texts())
	}
	btn, ok := tt.AccessByRole(ui.RoleSwitch, i18n.T("连接 VPN"))
	if !ok || btn.Bounds.W < 100 || btn.Bounds.H < 100 {
		t.Fatalf("connect button: %+v %v", btn.Bounds, ok)
	}
	tapAt(tt, btn.Bounds)
	tun := api.Settings().TUN
	if st := api.Status(); st.Core != model.CoreRunning {
		t.Fatalf("VPN on: core %v", st.Core)
	}
	if api.Settings().TUN != tun {
		t.Fatal("the connect button changed the TUN setting")
	}
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText(i18n.T("已连接")) {
		t.Fatalf("connected: %q", tt.Texts())
	}
	btn, ok = tt.AccessByRole(ui.RoleSwitch, i18n.T("断开 VPN"))
	if !ok || !btn.Checked {
		t.Fatalf("disconnect button: %+v %v", btn, ok)
	}
	tapAt(tt, btn.Bounds)
	if st := api.Status(); st.Core != model.CoreStopped {
		t.Fatalf("VPN off: core %v", st.Core)
	}
}

// tapAt taps the middle of r with a finger.
func tapAt(tt *ui.Tester, r ui.Rect) {
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.FingerDown(x, y)
	tt.FingerUp(x, y)
	tt.Frame()
}

// TestPhoneHomeCards: 首页's node card opens the main group in 代理, the
// subscription card 订阅; the exit map is not on 首页.
func TestPhoneHomeCards(t *testing.T) {
	api, s, tt := newPhoneTest(t)
	if tt.HasText(i18n.T("出口地图")) {
		t.Fatal("the exit map is on 首页")
	}
	node, _ := overviewMainNode(api)
	if !tt.HasText(node) {
		t.Fatalf("no current node %q: %q", node, tt.Texts())
	}
	phoneClick(t, tt, i18n.T("当前节点"))
	if s.Page() != env.Proxies {
		t.Fatalf("node card opens %s", s.Page())
	}
	tapTab(t, tt, i18n.T("首页"))
	phoneClick(t, tt, i18n.T("当前订阅"))
	if s.Page() != env.Subs {
		t.Fatalf("subscription card opens %s", s.Page())
	}
}

// overviewMainNode is the node the main group uses, through its groups.
func overviewMainNode(api *fake.Fake) (string, string) {
	px := api.Proxies()
	groups := map[string]string{}
	for _, g := range px.Groups {
		groups[g.Name] = g.Now
	}
	name := px.MainGroup
	for range 8 {
		now, ok := groups[name]
		if !ok {
			break
		}
		name = now
	}
	return name, px.MainGroup
}

// TestPhoneTouchTargets checks that what a finger taps in the shell is at
// least env.TouchSize high: the tabs and 设置's rows.
func TestPhoneTouchTargets(t *testing.T) {
	_, _, tt := newPhoneTest(t)
	for _, label := range []string{"首页", "代理", "订阅", "活动", "设置"} {
		info, ok := tt.AccessByRole(ui.RoleTab, i18n.T(label))
		if !ok || info.Bounds.H < env.TouchSize {
			t.Errorf("tab %s: %+v", label, info.Bounds)
		}
	}
	tapTab(t, tt, i18n.T("设置"))
	for _, p := range []env.Page{env.Rules, env.Net, env.Stats} {
		info, ok := tt.AccessByRole(ui.RoleButton, env.Title(p))
		if !ok || info.Bounds.H < env.TouchSize {
			t.Errorf("row %s: %+v (%v)", p, info.Bounds, ok)
		}
	}
}

// TestPhoneLargeTitle: on iOS, 设置's large title is the page's; the top
// bar shows the small one once it scrolls away. Android has none.
func TestPhoneLargeTitle(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	tapTab(t, tt, i18n.T("设置"))
	if env.Material() {
		if _, ok := s.phoneLarge(); ok {
			t.Fatal("a large title on Material")
		}
		return
	}
	if _, ok := s.phoneLarge(); !ok {
		t.Fatal("设置 has no large title")
	}
	before := countText(tt, i18n.T("设置")) // the large title and the tab
	tt.Scroll(195, 400, 0, 300)
	tt.Advance(time.Second)
	tt.Frame()
	tt.Frame()
	if s.phoneScroll(phoneSettingsPage).Y < env.LargeTitleHeight {
		t.Fatalf("did not scroll: %v", s.phoneScroll(phoneSettingsPage).Y)
	}
	if after := countText(tt, i18n.T("设置")); after != before+1 {
		t.Fatalf("the top bar shows no title once scrolled: %d then %d", before, after)
	}
}

// countText counts the drawn texts equal to s.
func countText(tt *ui.Tester, s string) int {
	n := 0
	for _, txt := range tt.Texts() {
		if txt == s {
			n++
		}
	}
	return n
}

// TestDesktopHasNoTabs: the desktop shell stays as it was, without the
// phone's bars.
func TestDesktopHasNoTabs(t *testing.T) {
	_, _, tt := newTest(t)
	if _, ok := tt.AccessByRole(ui.RoleTab, i18n.T("活动|标签页")); ok {
		t.Fatal("the desktop shows the phone's bottom bar")
	}
	if !tt.HasText(i18n.T("系统代理")) || tt.HasText(i18n.T("连接 VPN")) {
		t.Fatalf("desktop 概览 changed: %q", tt.Texts())
	}
}

// envtestHasScrolled reports whether text shows anywhere on the page,
// scrolling its content down a screen at a time.
func envtestHasScrolled(tt *ui.Tester, text string) bool {
	for range 15 {
		if tt.HasText(text) {
			return true
		}
		tt.Scroll(195, 400, 0, 300)
		tt.Frame()
	}
	return tt.HasText(text)
}

// settingsTitle is the name of a settings section, as 设置 lists it.
func settingsTitle(key string) string { return settings.TabTitleOf(key) }

// phoneClick scrolls the content until text is above the bottom bar, then
// taps it with a finger.
func phoneClick(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	// The bar's top, as drawn: 49 pt on Cupertino, 80 on Material (the
	// look follows the system the tests run on).
	bar, ok := tt.AccessByRole(ui.RoleTab, i18n.T("设置"))
	if !ok {
		t.Fatalf("no bottom bar: %q", tt.Texts())
	}
	barTop := bar.Bounds.Y
	tt.Advance(time.Second) // what still moves comes to rest first
	tt.Frame()
	for range 12 {
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("find %q: %q", text, tt.Texts())
		}
		// A text clipped away by its scroll container has no size.
		if r.W > 0 && r.H > 0 && r.Y >= 100 && r.Y+r.H/2 <= barTop-4 {
			x, y := r.X+r.W/2, r.Y+r.H/2
			tt.FingerDown(x, y)
			tt.FingerUp(x, y)
			tt.Frame()
			return
		}
		dy := float32(200)
		if r.Y < 100 || (r.H == 0 && r.Y < barTop/2) {
			dy = -200
		}
		tt.Scroll(r.X, 400, 0, dy)
		tt.Advance(time.Second) // let a fling settle before finding it again
		tt.Frame()
		tt.Frame()
	}
	t.Fatalf("cannot scroll %q above the bottom bar", text)
}

// TestPhoneEnglishUIHasNoChinese renders the phone's pages, 设置 and the
// settings' sections in English, core running and stopped, and fails on
// Chinese left in the chrome, as TestEnglishUIHasNoChinese does for the
// desktop.
func TestPhoneEnglishUIHasNoChinese(t *testing.T) {
	i18n.SetLang(i18n.EN)
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	api, s, tt := newPhoneTest(t)
	c := &englishChecker{t: t, data: append(api.DataStrings(), i18n.Autonym(i18n.ZH)), seen: map[string]bool{}}
	pages := append([]env.Page{env.Overview, env.Proxies, env.Subs, env.Conns, env.Logs}, phoneTools...)
	for _, running := range []bool{true, false} {
		if !running {
			if err := api.StopCore(); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range pages {
			s.phoneHub = false
			s.navigate(p, "")
			c.frame("phone page "+string(p), tt)
		}
		s.actSeg = segMap
		s.navigate(env.Conns, "")
		s.actSeg = segMap
		c.frame("phone 活动 › 地图", tt)
		s.phoneShowHub()
		c.frame("phone 设置", tt)
		for _, key := range phoneSettingsTabs {
			s.phoneHub = false
			s.navigate(env.Settings, key)
			c.frame("phone settings "+key, tt)
		}
	}
}

// TestPhoneSubsCards: on a phone the 订阅 page shows borderless cards
// without the desktop's buttons: a tap on one uses it, a swipe left shows
// 编辑 and 删除, and the top bar's + opens the import menu.
func TestPhoneSubsCards(t *testing.T) {
	api, s, tt := newPhoneTest(t)
	s.navigate(env.Subs, "")
	tt.Frame()
	for _, gone := range []string{i18n.T("双击切换订阅 · 可将 YAML 文件拖到此处导入"), i18n.T("全部更新"), i18n.Tf("更多 %s", "备用机场")} {
		if _, ok := tt.Find(gone); ok {
			t.Fatalf("%q shows on a phone: %q", gone, tt.Texts())
		}
	}
	// (备用机场's config fails the fake's check: 本地测试's does not.)
	click(t, tt, "本地测试")
	tt.Frame()
	for _, sub := range api.Subscriptions() {
		if sub.Current && sub.Name != "本地测试" {
			t.Fatalf("tapping a card did not use it: %s is current", sub.Name)
		}
	}
	wait := func(d time.Duration) {
		for range int(d / (16 * time.Millisecond)) {
			tt.Advance(16 * time.Millisecond)
		}
	}
	// A swipe left on 备用机场 shows 删除 at the card's end; a tap on it asks.
	r, ok := tt.Find("备用机场")
	if !ok {
		t.Fatalf("no 备用机场: %q", tt.Texts())
	}
	y := r.Y + r.H/2
	tt.FingerDown(350, y)
	wait(50 * time.Millisecond)
	for x := float32(330); x >= 150; x -= 20 {
		tt.FingerMove(x, y)
		tt.Frame()
		tt.Advance(16 * time.Millisecond)
	}
	wait(200 * time.Millisecond)
	tt.FingerUp(150, y)
	wait(2 * time.Second)
	tt.FingerDown(350, y)
	tt.FingerUp(350, y)
	tt.Frame()
	tt.Frame()
	if !tt.HasText(i18n.Tf("删除「%s」？", "备用机场")) {
		t.Fatalf("swiped 备用机场, then tapped 删除: no confirmation: %q", tt.Texts())
	}
	click(t, tt, i18n.T("取消"))
	tt.Frame()
	// (The pull to refresh: subs.TestPhonePullRefresh; a desktop system's
	// scroll views have no overscroll for a finger to pull.)
	// The top bar's + is a finger's size and opens the imports.
	plus, ok := tt.Find(i18n.T("导入"))
	if !ok || plus.W < env.TouchSize || plus.H < env.TouchSize || plus.Y > 120 || plus.X+plus.W > 390 {
		t.Fatalf("+ of the top bar: %v %v", plus, ok)
	}
	click(t, tt, i18n.T("导入"))
	if menu := strings.Join(tt.Menu(), "|"); !strings.Contains(menu, i18n.T("从链接导入…")) || !strings.Contains(menu, i18n.T("新建聚合订阅…")) {
		t.Fatalf("+ menu: %q", menu)
	}
}

// TestPhoneRulesList: on a phone the 规则 page lists the rules in two
// lines, and 添加规则 opens a dialog that fits the screen.
func TestPhoneRulesList(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	s.navigate(env.Rules, "")
	tt.Frame()
	for _, want := range []string{i18n.T("自定义规则"), i18n.T("订阅规则"), "openai.com", "google.com"} {
		if !tt.HasText(want) {
			t.Fatalf("no %q: %q", want, tt.Texts())
		}
	}
	add, ok := tt.Find(i18n.T("添加规则"))
	if !ok || add.H < env.TouchSize || add.X+add.W > 390 {
		t.Fatalf("添加规则: %v %v", add, ok)
	}
	click(t, tt, i18n.T("添加规则"))
	tt.Frame()
	save, ok := tt.Find(i18n.T("保存"))
	if !ok || save.X+save.W > 390 {
		t.Fatalf("the add dialog's 保存 is off the screen: %v %v", save, ok)
	}
}

// TestPhoneNetStatsProbe checks the phone layouts of 网络, 统计 and 检测:
// 网络 shows the VPN in place of the system proxy and TUN, and the pages'
// buttons stay on the screen.
func TestPhoneNetStatsProbe(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	s.navigate(env.Net, "")
	tt.Frame()
	if !tt.HasText("VPN") || tt.HasText(i18n.T("系统代理")) || tt.HasText(i18n.T("TUN 模式")) {
		t.Errorf("网络 on a phone: want the VPN line and no system proxy or TUN; texts %q", tt.Texts())
	}
	onScreen := func(text string) {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("no %q; texts %q", text, tt.Texts())
		}
		if r.X < 0 || r.X+r.W > 390 {
			t.Errorf("%q runs off the screen: %+v", text, r)
		}
	}
	s.navigate(env.Probe, "")
	tt.Frame()
	onScreen(i18n.T("开始检查"))
	s.navigate(env.Stats, "")
	tt.Frame()
	onScreen(i18n.T("重置统计"))
}

// TestPhoneLooks runs the shell's tests in both phone looks, whatever the
// system the tests run on: Cupertino (iOS) and Material (Android), whose
// bars and rows are of other heights.
func TestPhoneLooks(t *testing.T) {
	t.Cleanup(func() { mobile.SetLook(mobile.Automatic) })
	for _, look := range []struct {
		name string
		l    mobile.Look
	}{{"cupertino", mobile.Cupertino}, {"material", mobile.Material}} {
		mobile.SetLook(look.l)
		t.Run(look.name, func(t *testing.T) {
			t.Run("tabs", TestPhoneTabs)
			t.Run("settings", TestPhoneSettingsSections)
			t.Run("touch", TestPhoneTouchTargets)
			t.Run("vpn", TestPhoneVPNSwitch)
			t.Run("conns", TestPhoneConns)
			t.Run("logs", TestPhoneLogs)
			t.Run("subs", TestPhoneSubsCards)
			t.Run("rules", TestPhoneRulesList)
			t.Run("net-stats-probe", TestPhoneNetStatsProbe)
			t.Run("proxies-v2", TestPhoneProxiesV2)
			t.Run("settings-forms", TestPhoneSettingsForms)
			t.Run("settings-general-kernel", TestPhoneSettingsGeneralKernel)
		})
	}
}

// TestPhoneNoDesktopSwitches scans every page a phone shows, core running
// and stopped, for what only the
// desktop has: the system proxy and its address, the terminal proxy
// command, the TUN switch and the helper.
func TestPhoneNoDesktopSwitches(t *testing.T) {
	banned := []string{"恢复为关闭", "系统代理", "系统代理开关", "终端代理", "复制终端代理命令", "TUN 模式", "TUN 模式开关", "权限组件", "打开以让系统流量走代理", "内核开关"}
	api := fake.NewInstant()
	api.SetPendingRecovery(true) // no system proxy to recover on a phone: no prompt
	s := New(api, Options{Phone: true, Sync: true})
	t.Cleanup(s.Close)
	tt := ui.NewTester(s.View, 390, 844)
	tt.SetInsets(47, 0, 34, 0)
	pages := append([]env.Page{env.Overview, env.Proxies, env.Subs, env.Conns, env.Logs}, phoneTools...)
	check := func(where string) {
		t.Helper()
		tt.Frame()
		for _, txt := range tt.Texts() {
			if s.Page() == env.Overview && strings.Contains(txt, "127.0.0.1:") {
				t.Errorf("%s: a local proxy address on 概览: %q", where, txt)
			}
			for _, b := range banned {
				if txt == i18n.T(b) {
					t.Errorf("%s: %q on a phone", where, txt)
				}
			}
		}
	}
	for _, running := range []bool{true, false} {
		if !running {
			if err := api.StopCore(); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range pages {
			s.phoneHub = false
			s.navigate(p, "")
			check(fmt.Sprintf("%s (running %v)", p, running))
		}
		s.phoneShowHub()
		check(fmt.Sprintf("设置 (running %v)", running))
	}
}

// TestPhoneHomeStates: 首页's button and its lines while connecting and
// after an error, which shows the core's error text.
func TestPhoneHomeStates(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	setCore(s, model.CoreStarting, "")
	tt.Frame()
	if !tt.HasText(i18n.T("正在连接…")) {
		t.Fatalf("starting: %q", tt.Texts())
	}
	setCore(s, model.CoreError, "start mihomo: permission denied")
	tt.Frame()
	if !tt.HasText(i18n.T("连接出错")) || !tt.HasText("start mihomo: permission denied") {
		t.Fatalf("error: %q", tt.Texts())
	}
}

// TestPhoneProxiesV2: on a phone the 代理 page shows the groups as pills
// and the chosen group's members as cards; a tap on a pill chooses the
// group, a tap on a card the node, and the top bar tests the group.
func TestPhoneProxiesV2(t *testing.T) {
	api, s, tt := newPhoneTest(t)
	s.navigate(env.Proxies, "")
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleButton, i18n.T("测速当前组")); !ok {
		t.Fatalf("no 测速当前组 in the top bar: %q", tt.Texts())
	}
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "东京 03 · ") {
		t.Fatalf("the main group is not chosen: %q", tt.Texts())
	}
	tap := func(label string) ui.AccessInfo {
		t.Helper()
		info, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok {
			t.Fatalf("no button %q: %q", label, tt.Texts())
		}
		if info.Bounds.H < env.TouchSize {
			t.Errorf("%q is %v pt high, want at least %d", label, info.Bounds.H, env.TouchSize)
		}
		x, y := info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2
		tt.FingerDown(x, y)
		tt.FingerUp(x, y)
		tt.Frame()
		return info
	}
	tap("流媒体")
	if !strings.Contains(strings.Join(tt.Texts(), "|"), "香港 01") || !tt.HasText("台湾 01") {
		t.Fatalf("流媒体's cards do not show: %q", tt.Texts())
	}
	card := tap("新加坡 02")
	if card.Bounds.W > 390/2 {
		t.Errorf("a card is %v pt wide: not two to a row", card.Bounds.W)
	}
	tt.Frame()
	now := ""
	for _, g := range api.Proxies().Groups {
		if g.Name == "流媒体" {
			now = g.Now
		}
	}
	if now != "新加坡 02" {
		t.Fatalf("a tap on a card did not choose it: 流媒体 uses %q", now)
	}
	// Another page asks for a group: 首页's current node card.
	s.navigate(env.Proxies, "自动选择")
	tt.Frame()
	if !tt.HasText(i18n.T("自动测速") + " · " + i18n.Tn(6, "当前 %s · %d 个节点", "香港 01", 6)) {
		t.Fatalf("自动选择 is not chosen: %q", tt.Texts())
	}
}

// TestPhoneSettingsForms: on a phone DNS, 嗅探, 覆写 and 高级 are list
// rows — no text field, pop-up menu or segmented control on a section —
// and a row pushes its editor or choices, whose edits bring the 应用 bar.
func TestPhoneSettingsForms(t *testing.T) {
	_, s, tt := newPhoneTest(t)
	noForms := func(where string) {
		t.Helper()
		for _, a := range tt.AccessAll() {
			switch a.Role {
			case ui.RoleTextField, ui.RoleComboBox, ui.RolePopUpButton, ui.RoleRadioGroup:
				t.Errorf("%s: a desktop control (role %v) %q on a phone", where, a.Role, a.Name)
			}
		}
	}
	open := func(key string, rows ...string) {
		t.Helper()
		s.navigate(env.Settings, key)
		tt.Frame()
		noForms(key)
		for _, r := range rows {
			if !envtestHasScrolled(tt, i18n.T(r)) {
				t.Errorf("%s: no row %q: %q", key, r, tt.Texts())
			}
			noForms(key)
		}
		s.navigate(env.Settings, key) // back to the top
		tt.Frame()
	}
	pushed := func(title string) {
		t.Helper()
		if got := s.phoneTitle(); got != i18n.T(title) {
			t.Fatalf("pushed %q, want %q: %q", got, title, tt.Texts())
		}
	}
	back := func() {
		t.Helper()
		if !s.phoneBack() {
			t.Fatal("nothing to go back from")
		}
		tt.Frame()
	}
	applyBar := i18n.T("有未应用的更改。应用时会重新生成配置、校验，并让内核重新加载。")
	typeInto := func(label, text string) {
		t.Helper()
		in, ok := tt.AccessByRole(ui.RoleTextField, i18n.T(label))
		if !ok {
			t.Fatalf("no field %q: %q", label, tt.Texts())
		}
		if in.Bounds.W < 300 {
			t.Errorf("field %q is %v pt wide: not the screen's width", label, in.Bounds.W)
		}
		tapAt(tt, in.Bounds)
		tt.Key(0, ui.KeyEnd)
		tt.Type(text)
		tt.Frame()
	}

	// DNS: the source and the mode are choices, the lists editors, the
	// per-domain servers a row each with 添加 at the end.
	open("dns", "DNS 设置来源", "模式", "上游服务器", "按域名指定服务器", "自定义 hosts")
	phoneClick(t, tt, i18n.T("DNS 设置来源"))
	pushed("DNS 设置来源")
	phoneClick(t, tt, i18n.T("由澜流接管"))
	if s.phoneTitle() != settingsTitle("dns") || !tt.HasText(applyBar) {
		t.Fatalf("choosing 由澜流接管: title %q, no 应用 bar: %q", s.phoneTitle(), tt.Texts())
	}
	noForms("dns (taken over)")
	phoneClick(t, tt, i18n.T("上游服务器"))
	pushed("上游服务器")
	back()
	phoneClick(t, tt, "geosite:geolocation-!cn")
	pushed("按域名指定服务器")
	if !tt.HasText(i18n.T("删除此项")) {
		t.Fatalf("a server's editor has no 删除此项: %q", tt.Texts())
	}
	back()
	phoneClick(t, tt, i18n.T("添加服务器…"))
	pushed("按域名指定服务器")
	typeInto("域名或规则集", "+.example.com")
	back()
	if !envtestHasScrolled(tt, "+.example.com") {
		t.Fatalf("the added server has no row: %q", tt.Texts())
	}
	phoneClick(t, tt, "+.example.com")
	phoneClick(t, tt, i18n.T("删除此项"))
	if s.phoneTitle() != settingsTitle("dns") || tt.HasText("+.example.com") {
		t.Fatalf("删除此项 left %q: %q", s.phoneTitle(), tt.Texts())
	}

	phoneClick(t, tt, i18n.T("放弃")) // the 应用 bar's: no prompt on the way out

	// 嗅探: each protocol's switch, its ports a row pushing their editor.
	open("sniffer", "嗅探设置来源", "HTTP 端口", "QUIC 端口", "强制嗅探的域名")
	phoneClick(t, tt, i18n.T("嗅探设置来源"))
	phoneClick(t, tt, i18n.T("由澜流接管"))
	phoneClick(t, tt, i18n.Tf("%s 端口", "HTTP"))
	pushed(i18n.Tf("%s 端口", "HTTP"))
	typeInto(i18n.Tf("%s 端口", "HTTP"), ", 81")
	back()
	if !envtestHasScrolled(tt, "80, 8080-8880, 81") || !tt.HasText(applyBar) {
		t.Fatalf("the ports' edit does not show or bring the 应用 bar: %q", tt.Texts())
	}

	phoneClick(t, tt, i18n.T("放弃"))

	// 覆写: an entry a row pushing its page, the ways to add action rows.
	open("override", "去广告规则", "新建…", "从链接导入…", "订阅使用的覆写", "运行时配置")
	phoneClick(t, tt, "去广告规则")
	pushed("去广告规则")
	for _, want := range []string{"编辑 YAML…", "更新", "复制链接", "删除覆写"} {
		if !envtestHasScrolled(tt, i18n.T(want)) {
			t.Errorf("an override's page has no %q: %q", want, tt.Texts())
		}
	}
	back()
	phoneClick(t, tt, i18n.T("从链接导入…"))
	pushed("从链接导入覆写")
	back()

	// 高级: pop-up menus are choices, the fields editors.
	open("advanced", "HTTP 端口", "进程匹配", "出站网卡", "缓存算法", "备用服务器 · 按 GeoIP 过滤")
	phoneClick(t, tt, i18n.T("进程匹配"))
	pushed("进程匹配")
	phoneClick(t, tt, i18n.T("严格"))
	if !tt.HasText(applyBar) {
		t.Fatalf("a choice does not bring the 应用 bar: %q", tt.Texts())
	}
	phoneClick(t, tt, i18n.T("HTTP 端口"))
	pushed("HTTP 端口")
	typeInto("HTTP 端口", "7891")
	back()
	if !envtestHasScrolled(tt, "7891") {
		t.Fatalf("HTTP 端口 does not show 7891: %q", tt.Texts())
	}
}

// TestPhoneSettingsGeneralKernel: 常规 and 内核 on a phone are list rows —
// no text field, pop-up menu or segmented control on the lists, a row per
// action — and a row pushes its editor (saved on 完成, kept open on a bad
// value) or its choices.
func TestPhoneSettingsGeneralKernel(t *testing.T) {
	api, s, tt := newPhoneTest(t)
	open := func(key string) {
		t.Helper()
		s.phoneHub = false
		s.navigate(env.Settings, key)
		tt.Frame()
	}
	for _, sec := range []struct {
		key  string
		want []string
	}{
		{"general", []string{"外观", "导出设置…", "导入设置…", "导出诊断信息…", "清理缓存", "同步位置", "服务器地址", "保留份数", "立即备份", "测试连接", "服务器上的备份", "上报地址", "访问令牌"}},
		{"kernel", []string{"内核版本", "日志级别", "重启内核", "GeoIP 数据库", "GEOIP 规则使用", "自动更新", "更新地理数据库", "GitHub 镜像", "镜像测速"}},
	} {
		open(sec.key)
		seen := map[string]bool{}
		for range 15 {
			for _, txt := range tt.Texts() {
				seen[txt] = true
			}
			for _, a := range tt.AccessAll() {
				if a.Role == ui.RoleTabList && a.Name == i18n.T("页面") {
					continue // the shell's bottom bar
				}
				switch a.Role {
				case ui.RoleTextField, ui.RoleComboBox, ui.RolePopUpButton, ui.RoleTabList:
					t.Errorf("%s: a desktop control on a phone: %v %q", sec.key, a.Role, a.Name)
				}
			}
			tt.Scroll(195, 400, 0, 300)
			tt.Advance(time.Second)
			tt.Frame()
		}
		for _, w := range sec.want {
			if !seen[i18n.T(w)] {
				t.Errorf("%s: no %q row", sec.key, w)
			}
		}
	}

	// An editor: 用户名 saves on 完成.
	open("general")
	phoneClick(t, tt, i18n.T("用户名"))
	if got := s.phoneTitle(); got != i18n.T("用户名") {
		t.Fatalf("用户名 pushed %q", got)
	}
	in, ok := tt.AccessByRole(ui.RoleTextField, i18n.T("用户名"))
	if !ok || in.Bounds.W < 300 {
		t.Fatalf("no full-width input: %+v %v", in.Bounds, ok)
	}
	tt.ClickAt(in.Bounds.X+in.Bounds.W/2, in.Bounds.Y+in.Bounds.H/2)
	tt.Type("alice")
	tt.Frame()
	phoneClick(t, tt, i18n.T("完成"))
	if got := api.Settings().WebDAV.User; got != "alice" {
		t.Fatalf("用户名 saved %q", got)
	}
	if got := s.phoneTitle(); got != i18n.T("常规") {
		t.Fatalf("完成 left %q open", got)
	}

	// A bad address keeps the editor open with its error.
	phoneClick(t, tt, i18n.T("服务器地址"))
	in, _ = tt.AccessByRole(ui.RoleTextField, i18n.T("服务器地址"))
	tt.ClickAt(in.Bounds.X+in.Bounds.W/2, in.Bounds.Y+in.Bounds.H/2)
	tt.Type("ftp://nas")
	tt.Frame()
	phoneClick(t, tt, i18n.T("完成"))
	if s.phoneTitle() != i18n.T("服务器地址") || !tt.HasText(i18n.T("WebDAV 地址须是以 http:// 或 https:// 开头的完整地址")) {
		t.Fatalf("a bad address closed the editor: %q %q", s.phoneTitle(), tt.Texts())
	}
	if api.Settings().WebDAV.URL != "" {
		t.Fatalf("a bad address was saved: %q", api.Settings().WebDAV.URL)
	}

	// A choice: 日志级别 › 调试.
	open("kernel")
	phoneClick(t, tt, i18n.T("日志级别"))
	if got := s.phoneTitle(); got != i18n.T("日志级别") {
		t.Fatalf("日志级别 pushed %q", got)
	}
	phoneClick(t, tt, i18n.T("调试"))
	if got := api.Settings().LogLevel; got != "debug" {
		t.Fatalf("日志级别 saved %q", got)
	}
}
