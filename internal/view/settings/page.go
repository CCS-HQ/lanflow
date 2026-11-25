// Package settings is the 设置 page (docs/design.md §5.8): seven tabs — 常规,
// 代理, DNS, 嗅探, 覆写, 内核, 高级. 常规/代理/内核 apply at once through
// UpdateSettings; DNS, 嗅探 and 高级 collect edits and apply them with the
// sticky 应用 bar; 覆写 edits YAML overrides (§6.9) and shows the runtime
// config (§6.10).
package settings

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/sysproxy"
	"lanflow/internal/view/env"
	"lanflow/internal/view/logs"
)

// appVersion is shown in 关于.
const appVersion = "0.1.13"

const (
	tabGeneral = iota
	tabProxy
	tabTUN
	tabDNS
	tabSniffer
	tabOverride
	tabKernel
	tabAdvanced
)

var (
	tabNames = []string{"常规", "代理|设置", "TUN", "DNS", "嗅探", "覆写", "内核", "高级"}
	tabKeys  = []string{"general", "proxy", "tun", "dns", "sniffer", "override", "kernel", "advanced"}
)

func tabDesc(tab int) string {
	switch tab {
	case tabGeneral:
		return i18n.T("启动、外观、菜单栏与全局快捷键")
	case tabProxy:
		return i18n.T("代理端口、局域网共享、绕过地址与测速")
	case tabTUN:
		return i18n.T("虚拟网卡接管、协议栈与内网兼容")
	case tabDNS:
		return i18n.T("域名解析方式、上游服务器与防泄漏")
	case tabSniffer:
		return i18n.T("从流量中还原真实域名，改善规则分流")
	case tabOverride:
		return i18n.T("用 YAML 追加节点、策略组或规则")
	case tabKernel:
		return i18n.T("Mihomo 版本管理与地理数据库更新")
	case tabAdvanced:
		return i18n.T("独立端口、局域网认证与底层调优")
	default:
		return ""
	}
}

// Page holds page-local UI state.
type Page struct {
	e   *env.Env
	tab int

	// immediate settings (常规 / 代理 / 内核)
	port     field
	portErr  string
	geoURLs  [4]field
	bypass   dialogText
	atrust   atrustDialog // aTrust 兼容 › 配置… (atrust.go)
	checking bool
	saving   bool           // a settings save is running or queued; switches wait for it
	caches   cacheState     // 缓存 row of 数据 (caches.go)
	menuBar  menuBarPreview // 菜单栏 preview (menubar.go)

	// DNS (dns.go)
	dns        dnsForm
	dnsLoaded  model.DNSSettings
	dnsInit    bool
	hosts      dialogText
	applying   bool
	dnsSession int

	// 嗅探 (sniffer.go)
	snf       snfForm
	snfLoaded model.SnifferSettings
	snfInit   bool

	// 高级 (advanced.go)
	adv        advForm
	advLoaded  model.AdvancedSettings
	advInit    bool
	advSession int

	// 覆写 (override.go)
	ov  overrideDialog
	rt  runtimeDialog
	ovl overrideListUI
	// 铺满窗口 of the YAML editor and the runtime config, kept between
	// openings (dialogsize.go)
	dlgFull bool

	// 延迟测试 (delay.go)
	delayURL, delayMs       field
	delayURLErr, delayMsErr string

	// 内核更新与设置导出 (kernel.go)
	export exportDialog
	// 内核版本管理 (cores.go)
	cores coresUI

	// 全局快捷键 (hotkey.go), Wi-Fi 规则, 下载镜像, 备份与同步 (feat2.go)
	f2 feat2UI
	// 配置同步 (configsync.go)
	sync syncUI

	// 反馈问题 / 反馈上报 of 关于 (feedback.go)
	fbk feedbackUI

	// 应用更新 (update.go)
	upd      Updater
	relaunch func()

	// Settings saves run one at a time, in order (save): saveQ holds the
	// ones not finished, oldest first, saveBusy says the first is running,
	// and draft is the newest of them, shown in place of the API's
	// settings until the queue is empty. UI thread only.
	saveQ    []saveReq
	saveBusy bool
	draft    *model.Settings
	// shown is what current last returned: the snapshot a control's
	// callback changed, and so the base of the save it asks for.
	shown    model.Settings
	hasShown bool

	// State that jobs change is written on the UI thread, in the done
	// function of Env.Then, and read by the view without a lock. mu guards
	// the few fields below (and those their own comments name), which
	// other goroutines still write and every reader locks for.
	mu     sync.Mutex
	toast  string
	latest string // newer core version found by 检查更新, "" = none
	update updateState
}

// field is a text input that commits when it is submitted or loses the
// focus, showing the current value meanwhile.
type field struct {
	text    string
	editing bool
}

// dialogText is a dialog editing one multi-line text.
type dialogText struct {
	open    bool
	text    string
	session int
}

// New returns the page.
func New(e *env.Env) *Page {
	return &Page{e: e, upd: mygoUpdater{}, relaunch: func() { mygo.App.Relaunch() }}
}

// ActiveTab returns the key of the currently selected settings tab.
func (p *Page) ActiveTab() string {
	if p.tab >= 0 && p.tab < len(tabKeys) {
		return tabKeys[p.tab]
	}
	return tabKeys[0]
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	p.flushToast()
	if a := e.Arg(env.Settings); a != "" {
		p.handleArg(a)
	}
	s := p.current()
	p.saving = p.draft != nil
	st := e.API.Status()
	p.loadForms(s)
	if p.tab != tabGeneral {
		p.caches.loaded = false // 缓存 row out of view (caches.go)
	}

	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("设置"), tabDesc(p.tab), nil)
		// Dedicated full-width pill tab bar so all 7 tabs stay legible on
		// compact windows without colliding with the page title.
		ui.Box(c).Padding(0, 20, 10, 20).Shrink(0).Children(func() {
			ui.Row(c.Key("tabs")).Padding(3).Gap(3).Radius(10).Background(t.Surface).
				Border(1, t.Border.Alpha(0.6)).AlignItems(ui.Center).Label(i18n.T("设置分栏")).Children(func() {
				labels := i18n.Options(tabNames...)
				for i, lbl := range labels {
					active := p.tab == i
					btn := ui.ButtonBase(c.Key("tab:"+tabKeys[i])).Grow(1).Basis(0).Height(28).Radius(7).
						AlignItems(ui.Center).Justify(ui.Center).Label(lbl).Cursor(ui.CursorPointer)
					if active {
						btn.Background(t.Accent)
					} else if btn.Hovered() {
						btn.Background(t.SurfaceHover)
					}
					btn.Children(func() {
						txt := ui.Text(c, lbl).FontSize(12.5).FontWeight(500).SingleLine()
						if active {
							txt.TextColor(t.AccentText).FontWeight(600)
						} else {
							txt.TextColor(t.TextMuted)
						}
					})
					if btn.Clicked() && !active {
						p.requestTab(i)
					}
				}
			})
		})
		ui.Scroll(c.Key(tabKeys[p.tab])).Grow(1).Padding(6, 22, 24).Gap(22).Children(func() {
			switch p.tab {
			case tabGeneral:
				p.general(c, t, s)
			case tabProxy:
				p.proxy(c, t, s, st)
			case tabTUN:
				p.tunTab(c, t, s, st)
			case tabDNS:
				p.dnsTab(c, t, s)
			case tabSniffer:
				p.snifferTab(c, t, s)
			case tabOverride:
				p.overrideTab(c, t)
			case tabKernel:
				p.kernel(c, t, s, st)
			case tabAdvanced:
				p.advancedTab(c, t, s, st)
			}
		})
		switch p.tab {
		case tabDNS:
			if p.dnsDirty() {
				p.applyBar(c, t, p.discardDNS, p.applyDNS)
			}
		case tabSniffer:
			if p.snfDirty() {
				p.applyBar(c, t, p.discardSniffer, p.applySniffer)
			}
		case tabAdvanced:
			if p.advDirty() {
				p.applyBar(c, t, p.discardAdvanced, p.applyAdvanced)
			}
		}
	})
	p.bypassDialog(c, t, s)
	p.atrustDialog(c, t, s)
	p.hostsDialog(c, t)
	p.overrideEditor(c, t)
	p.runtimeDialog(c, t)
	p.overrideListDialogs(c, t)
}

func (p *Page) handleArg(a string) {
	if key, ok := strings.CutPrefix(a, "override:"); ok {
		p.requestTab(tabOverride)
		if p.tab == tabOverride {
			p.openOverride(key)
		}
		return
	}
	for i, k := range tabKeys {
		if k == a {
			p.requestTab(i)
		}
	}
}

// requestTab switches tabs, asking first when the current tab has
// unapplied changes.
func (p *Page) requestTab(tab int) {
	if tab == p.tab {
		return
	}
	dirty := (p.tab == tabDNS && p.dnsDirty()) || (p.tab == tabSniffer && p.snfDirty()) || (p.tab == tabAdvanced && p.advDirty())
	if !dirty {
		p.tab = tab
		return
	}
	from := p.tab
	p.e.Confirm(i18n.T("放弃未应用的更改？"), i18n.Tf("你在「%s」中的修改还没有应用，切换分栏将丢弃这些修改。", i18n.T(tabNames[from])), i18n.T("放弃"), true, func() {
		switch from {
		case tabDNS:
			p.discardDNS()
		case tabSniffer:
			p.discardSniffer()
		case tabAdvanced:
			p.discardAdvanced()
		}
		p.tab = tab
	})
}

// Tab returns the current tab's key (for tests).
func (p *Page) Tab() string { return tabKeys[p.tab] }

func (p *Page) flushToast() {
	p.mu.Lock()
	msg := p.toast
	p.toast = ""
	p.mu.Unlock()
	if msg != "" {
		p.e.Toast(msg, false)
	}
}

// current returns the settings to show: the draft being saved, else the
// API's. It is the snapshot the controls change and hand to save, so it is
// remembered (shown) as what the next save's change is measured against.
func (p *Page) current() model.Settings {
	s := p.e.API.Settings()
	if d := p.draft; d != nil {
		s = *d
	}
	p.shown, p.hasShown = s, true
	return s
}

// saveReq is one queued save: the change from base, what the controls
// showed, to s, what one of them made of it. done runs on the UI thread.
type saveReq struct {
	s    model.Settings
	base model.Settings
	done func(err error)
}

// save applies s at once. The error is toasted unless onErr takes it;
// onErr runs on the UI thread.
func (p *Page) save(s model.Settings, ok string, onErr func(error)) {
	p.saveThen(s, func(err error) {
		switch {
		case err == nil:
			if ok != "" {
				p.e.Toast(ok, false)
			}
		case onErr != nil:
			onErr(err)
		default:
			p.e.Toast(err.Error(), true)
		}
	})
}

// saveThen queues a save of s, a snapshot of the settings with a control's
// change in it, and calls done on the UI thread with its outcome. Only
// what s changes against the snapshot the controls showed (shown) is saved
// (API.UpdateSettings merges it into the stored settings), so a save
// never puts back what another page, the tray, an agent or the
// configuration sync stored meanwhile. Saves still run one at a time and
// in the order made: two changes of one option must land in that order.
// s becomes the draft, which the controls show and build their next
// snapshot on; the draft goes only when no save is left, as the API's
// settings then hold all of them.
func (p *Page) saveThen(s model.Settings, done func(err error)) {
	cp := s
	if !p.hasShown {
		p.current()
	}
	// Not current(): a save that finished between the frame that drew the
	// control and its callback has changed what current returns, and the
	// difference would count as this control's change.
	base := p.shown
	p.draft = &cp
	p.saveQ = append(p.saveQ, saveReq{s: cp, base: base, done: done})
	p.pumpSaves()
}

// redraft makes the draft anew after a save failed: the stored settings
// with the changes still queued. The failed change is in none of them
// (each request holds only its own), so the controls stop showing it.
func (p *Page) redraft() {
	d := p.e.API.Settings()
	for _, q := range p.saveQ {
		d = model.MergeSettings(d, q.base, q.s)
	}
	p.draft = &d
}

// pumpSaves starts the oldest queued save unless one is running.
func (p *Page) pumpSaves() {
	if p.saveBusy || len(p.saveQ) == 0 {
		return
	}
	p.saveBusy = true
	req := p.saveQ[0]
	p.e.Then(func() error { return p.e.API.UpdateSettings(req.base, req.s) }, func(err error) {
		p.saveBusy = false
		p.saveQ = p.saveQ[1:]
		if len(p.saveQ) == 0 {
			p.saveQ, p.draft = nil, nil
		} else if err != nil {
			p.redraft()
		}
		if req.done != nil {
			req.done(err)
		}
		p.pumpSaves()
	})
}

// loadForms fills the DNS and sniffer forms from the saved settings when
// they have no unapplied edits.
func (p *Page) loadForms(s model.Settings) {
	if !p.dnsInit || (!p.dnsDirty() && !dnsEqual(s.DNS, p.dnsLoaded)) {
		p.dns, p.dnsLoaded, p.dnsInit = dnsFormFrom(s.DNS), s.DNS, true
		p.dnsSession++
	}
	if !p.snfInit || (!p.snfDirty() && !snfEqual(s.Sniffer, p.snfLoaded)) {
		p.snf, p.snfLoaded, p.snfInit = snfFormFrom(s.Sniffer), s.Sniffer, true
	}
	if !p.advInit || (!p.advDirty() && advFormFrom(s.Advanced) != advFormFrom(p.advLoaded)) {
		p.adv, p.advLoaded, p.advInit = advFormFrom(s.Advanced), s.Advanced, true
		p.advSession++
	}
}

// ---- 常规 ----

func (p *Page) general(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	tray := i18n.T("隐藏到托盘")
	if e.Mac {
		tray = i18n.T("隐藏到菜单栏")
	}
	// Two columns at ≥1100 pt (docs/design.md §5.8): 常规 and the shortcuts
	// on the left, data, backup and about on the right.
	env.Columns(c, 20, func() {
		e.Kit.Section(c, i18n.T("启动与窗口"), "", func() {
			p.switchRow(c, i18n.T("开机时启动"), "", s.OpenAtLogin, func(on bool) { s.OpenAtLogin = on; p.save(s, "", nil) })
			p.switchRow(c, i18n.T("启动后自动开启系统代理"), "", s.AutoSystemProxy, func(on bool) { s.AutoSystemProxy = on; p.save(s, "", nil) })
			where := i18n.T("通知区域")
			if e.Mac {
				where = i18n.T("菜单栏")
			}
			p.switchRow(c, i18n.T("启动时不显示窗口"), i18n.Tf("仅在%s显示图标", where), s.SilentStart, func(on bool) { s.SilentStart = on; p.save(s, "", nil) })
			closeOpts := []string{tray, i18n.T("退出澜流")}
			closeVal := closeOpts[1]
			if s.CloseToTray {
				closeVal = closeOpts[0]
			}
			p.selectRow(c, i18n.T("关闭窗口时"), "", closeVal, closeOpts, func(v string) { s.CloseToTray = v == closeOpts[0]; p.save(s, "", nil) })
		})
		e.Kit.Section(c, i18n.T("外观与语言"), "", func() {
			appOpts := []string{i18n.T("跟随系统"), i18n.T("浅色"), i18n.T("深色")}
			appVals := []string{"system", "light", "dark"}
			p.selectRow(c, i18n.T("外观"), "", mapName(s.Appearance, appVals, appOpts), appOpts, func(v string) {
				s.Appearance = mapValue(v, appOpts, appVals)
				p.save(s, "", nil)
			})
			langOpts := []string{i18n.Autonym(i18n.ZH), i18n.Autonym(i18n.EN)}
			langVals := []string{"zh", "en"}
			p.selectRow(c, i18n.T("语言"), "", mapName(s.Language, langVals, langOpts), langOpts, func(v string) {
				s.Language = mapValue(v, langOpts, langVals)
				p.save(s, "", nil)
			})
		})
		e.Kit.Section(c, i18n.T("功能"), "", func() {
			p.switchRow(c, i18n.T("抓包（HTTPS 解密）"), i18n.T("显示「抓包」页，可按域名或程序解密 HTTPS 请求；关闭时同时停止解密"), s.CaptureEnabled, func(on bool) {
				s.CaptureEnabled = on
				p.save(s, "", nil)
			})
		})
		if e.Mac {
			p.menuBarSection(c, t, s)
		}
		e.Kit.Section(c, i18n.T("全局快捷键"), i18n.T("在任何应用中按下即可触发；留空即关闭"), func() { p.shortcutRows(c, t, s) })
	}, func() {
		e.Kit.Section(c, i18n.T("数据"), "", func() {
			e.Kit.Row(c, i18n.T("配置文件夹"), e.API.ConfigDir(), func() {
				ui.Button(c, revealLabel(e)).OnClick(func() { env.RevealInFolder(e.API.ConfigDir()) })
			})
			e.Kit.Row(c, i18n.T("导出 / 导入设置"), i18n.T("设置、覆写与自定义规则保存为 lanflow.yaml；导入会按文件内容应用"), func() {
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, i18n.T("导出…")).Clicked() {
						p.export.open, p.export.subs = true, false
					}
					if ui.Button(c, i18n.T("导入…")).Clicked() {
						go p.importSettings()
					}
				})
			})
			e.Kit.Row(c, i18n.T("诊断信息"), i18n.T("把日志、运行时配置和网络状态打包成 ZIP，便于排查问题；节点地址、密码和订阅令牌已去除"), func() {
				if ui.Button(c, i18n.T("导出诊断信息…")).Clicked() {
					go logs.ExportDiagnostics(e)
				}
			})
			p.cacheRow(c)
			p.exportDialog(c, t)
		})
		p.configSyncSection(c, t) // 配置同步 (configsync.go)
		p.backupSection(c, t, s)  // 备份与同步 (backup.go)
		e.Kit.Section(c, i18n.T("关于"), "", func() {
			ui.Row(c).Gap(12).MinHeight(56).PaddingX(12).Children(func() {
				ui.Column(c).Gap(4).Grow(1).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Baseline).Children(func() {
						ui.Text(c, i18n.T("澜流 Lanflow")).Bold()
						ui.Text(c, AppVersion()).TextColor(t.TextMuted).FontFeatures("tnum")
					})
					ui.RichText(c).FontSize(12).TextColor(t.TextMuted).Children(func() {
						ui.Text(c, i18n.T("基于 Mihomo（GPL-3.0）· "))
						ui.Link(c, i18n.T("Mihomo 源代码"), "https://github.com/MetaCubeX/mihomo/tree/"+app.PinnedCoreVersion)
						ui.Text(c, " · ")
						ui.Link(c, i18n.T("许可证"), "https://github.com/MetaCubeX/mihomo/blob/"+app.PinnedCoreVersion+"/LICENSE")
					})
				})
			})
			p.updateRows(c, t, s)
			p.feedbackRows(c, t, s)
		})
	})
}

// ---- 代理 ----

func (p *Page) proxy(c *ui.Context, t *ui.Theme, s model.Settings, st model.Status) {
	e := p.e
	// Two columns at ≥1100 pt: 代理 on the left, the rest on the right.
	env.Columns(c, 20, func() {
		e.Kit.Section(c, i18n.T("代理"), "", func() {
			e.Kit.Row(c, i18n.T("代理端口"), i18n.T("HTTP 和 SOCKS5 共用此端口"), func() {
				ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
					if p.portErr == "" {
						p.port.show(strconv.Itoa(s.MixedPort))
					}
					in := ui.TextInput(c.Key("port"), &p.port.text).Width(84).TextAlign(ui.End).FontFeatures("tnum").
						Label(i18n.T("代理端口")).Invalid(p.portErr != "")
					if in.Changed() {
						p.portErr = ""
					}
					if p.port.commit(in) {
						p.commitPort(s)
					}
					if p.portErr != "" {
						ui.Text(c, p.portErr).FontSize(11).TextColor(t.Danger).MaxWidth(320).TextAlign(ui.End)
					}
				})
			})
			lanDesc := i18n.T("仅本机可以使用代理")
			if s.AllowLAN {
				addr := st.LANAddress
				if addr == "" {
					addr = i18n.T("本机局域网地址")
				}
				lanDesc = i18n.Tf("其他设备可通过 %s:%d 使用代理", addr, s.MixedPort)
			}
			p.switchRow(c, i18n.T("允许局域网连接"), lanDesc, s.AllowLAN, func(on bool) { s.AllowLAN = on; p.save(s, "", nil) })
			if s.AllowLAN && len(s.Advanced.Authentication) == 0 {
				e.Kit.Banner(c, "warn", i18n.T("没有设置代理认证：同一网络里的任何设备都能使用这个代理。"), func() {
					ui.Button(c, i18n.T("设置认证…")).OnClick(func() { p.requestTab(tabAdvanced) })
				})
			}
			e.Kit.Row(c, i18n.T("不走代理的地址"), i18n.Tf("%d 条", len(s.Bypass)), func() {
				ui.Button(c, i18n.T("编辑…")).OnClick(func() {
					p.bypass.text = strings.Join(s.Bypass, "\n")
					p.bypass.session++
					p.bypass.open = true
				})
			})
			p.switchRow(c, i18n.T("实测节点出口位置"), i18n.T("经节点访问 www.cloudflare.com/cdn-cgi/trace，确定真实出口位置"), s.ProbeExits, func(on bool) {
				s.ProbeExits = on
				p.save(s, "", nil)
			})
		})
	}, func() {
		p.delaySection(c, t, s)
		p.wifiSection(c, t, s) // 按 Wi-Fi 切换订阅 (wifi.go)
	})
}

// ---- TUN ----

func (p *Page) tunTab(c *ui.Context, t *ui.Theme, s model.Settings, st model.Status) {
	e := p.e
	e.Kit.Section(c, i18n.T("TUN 模式"), i18n.T("通过虚拟网卡接管系统全部流量，包括不遵守系统代理的终端与应用"), func() {
		tunDesc := i18n.T("接管所有应用的流量，首次开启需要安装权限组件")
		if st.TUNAvailable {
			tunDesc = i18n.T("接管所有应用的流量，包括不遵守系统代理的程序")
		}
		p.switchRow(c, i18n.T("TUN 模式（增强模式）"), tunDesc, st.TUN, p.setTUN)
		if st.HelperReinstall {
			e.Kit.Banner(c, "warn", i18n.T("已安装的权限组件来自另一个版本的澜流，TUN 模式暂不可用，需要重新安装权限组件。"), func() {
				ui.Button(c, i18n.T("重新安装…")).OnClick(func() { env.ReinstallHelper(p.e, s.TUN) })
			})
		}
		if st.TUNAvailable || st.TUN {
			stackOpts := []string{"mixed", "system", "gvisor"}
			stack := s.TUNStack
			if stack == "" {
				stack = "mixed"
			}
			p.selectRow(c, i18n.T("TUN 协议栈"), i18n.T("一般保持 mixed；兼容性问题时可改为 system 或 gvisor"), stack, stackOpts, func(v string) {
				s.TUNStack = v
				p.save(s, "", nil)
			})
			if e.Mac {
				p.switchRow(c, i18n.T("自动设置系统 DNS"), i18n.T("开启 TUN 时把系统 DNS 指向 223.5.5.5，让 DNS 查询经过澜流；关闭 TUN 或退出时恢复原设置"), s.TUNSetDNS, func(on bool) {
					s.TUNSetDNS = on
					p.save(s, "", nil)
				})
			}
		}
		p.atrustRow(c, s, st)
	})
	e.Kit.Section(c, i18n.T("进阶 TUN 参数"), "", func() {
		e.Kit.Row(c, i18n.T("MTU、严格路由与排除网段"), i18n.T("在「高级」分栏中配置精细的虚拟网卡参数"), func() {
			ui.Button(c, i18n.T("前往高级设置")).OnClick(func() { p.requestTab(tabAdvanced) })
		})
	})
}

func (p *Page) commitPort(s model.Settings) {
	n, err := strconv.Atoi(strings.TrimSpace(p.port.text))
	if err != nil || n < 1 || n > 65535 {
		p.portErr = i18n.T("端口须为 1–65535 之间的数字")
		return
	}
	if n == s.MixedPort {
		p.portErr = ""
		return
	}
	s.MixedPort = n
	p.save(s, i18n.Tf("代理端口已改为 %d", n), func(err error) { p.portErr = err.Error() })
}

func (p *Page) setTUN(on bool) { env.SetTUN(p.e, on) }

func (p *Page) bypassDialog(c *ui.Context, t *ui.Theme, s model.Settings) {
	if !p.bypass.open {
		return
	}
	ui.Modal(c, &p.bypass.open, func() {
		ui.Column(c.Key(p.bypass.session)).Width(env.DialogWidth(c, 460)).Gap(12).Children(func() {
			ui.Text(c, i18n.T("不走代理的地址")).FontSize(15).Bold()
			ui.Text(c, i18n.T("每行一个：域名、IP 或网段。系统代理开启时，这些地址直接连接。")).FontSize(12).TextColor(t.TextMuted)
			ui.TextArea(c.Key("bypass"), &p.bypass.text).Font("monospace").FontSize(12).Height(min(env.DialogMaxHeight(c, 160), 520)).Label(i18n.T("不走代理的地址"))
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, i18n.T("恢复默认")).Clicked() {
					p.bypass.text = strings.Join(defaultBypass, "\n")
				}
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Clicked() {
					p.bypass.open = false
				}
				ui.PrimaryButton(c, i18n.T("保存")).OnClick(func() {
					s.Bypass = lines(p.bypass.text)
					p.bypass.open = false
					p.save(s, i18n.T("已更新不走代理的地址"), nil)
				})
			})
		})
	})
}

var defaultBypass = sysproxy.DefaultBypass

// ---- 内核 ----

func (p *Page) kernel(c *ui.Context, t *ui.Theme, s model.Settings, st model.Status) {
	e := p.e
	running := st.Core == model.CoreRunning
	// Two columns at ≥1100 pt: 内核 on the left, the resources on the right.
	env.Columns(c, 20, func() {
		p.coreVersionSection(c, t, s, st) // 内核版本 + installed list (cores.go)
		p.coreUpdateSection(c, s)         // 自动检查 / 更新通道 / 自动安装 (cores.go)
		e.Kit.Section(c, i18n.T("内核"), "", func() {
			file, fileDesc := i18n.T("内置"), ""
			if s.CorePath != "" {
				file, fileDesc = s.CorePath, i18n.T("自定义内核文件")
				if s.CoreVersion != "" {
					file, fileDesc = s.CoreVersion, i18n.T("已安装的内核版本")
				}
			}
			if st.Core == model.CoreMissing {
				fileDesc = i18n.T("未找到内核文件")
			}
			dl := e.API.CoreDownload()
			e.Kit.Row(c, i18n.T("内核文件"), fileDesc, func() {
				ui.Row(c).Gap(10).Children(func() {
					color := t.TextMuted
					if st.Core == model.CoreMissing {
						color = t.Danger
					}
					ui.Text(c, file).TextColor(color).SingleLine().MaxWidth(260).Tooltip(file)
					if st.Core == model.CoreMissing && !dl.Active {
						ui.PrimaryButton(c, i18n.T("下载内核")).OnClick(func() { p.updateCore("") })
					}
					if s.CorePath != "" && s.CoreVersion == "" {
						ui.Button(c, i18n.T("恢复内置")).Disabled(dl.Active).OnClick(func() { p.useCore("") })
					}
					ui.Button(c, i18n.T("更改…")).Disabled(dl.Active).OnClick(func() { go p.chooseCore() })
				})
			})
			lvlOpts := []string{i18n.T("调试"), i18n.T("信息"), i18n.T("警告"), i18n.T("错误"), i18n.T("静默")}
			lvlVals := []string{"debug", "info", "warning", "error", "silent"}
			p.selectRow(c, i18n.T("日志级别"), "", mapName(s.LogLevel, lvlVals, lvlOpts), lvlOpts, func(v string) {
				s.LogLevel = mapValue(v, lvlOpts, lvlVals)
				p.save(s, "", nil)
			})
			e.Kit.Row(c, "", "", func() {
				ui.Button(c, i18n.T("重启内核")).Disabled(st.Core == model.CoreMissing || st.Core == model.CoreStarting).OnClick(func() {
					e.Run(e.API.RestartCore, i18n.T("内核已重启"))
				})
			})
		})
	}, func() {
		p.geoSection(c, t, s, running)
		p.mirrorSection(c, t, s) // 下载镜像 (mirror.go)
	})
	p.installDialog(c, t) // 安装其他版本… (cores.go)
}

// checkUpdate asks the service for a newer core in the chosen channel and
// remembers it for the 更新到 button.
func (p *Page) checkUpdate(current string) {
	e := p.e
	p.checking = true
	var info model.CoreUpdateInfo
	e.Then(func() (err error) {
		info, err = e.API.CheckCoreUpdate()
		return err
	}, func(err error) {
		p.checking = false
		if err != nil {
			e.Toast(err.Error(), true)
			return
		}
		if current == "" {
			current = info.Current
		}
		latest, msg := "", i18n.T("已是最新版本")
		switch {
		case info.Available:
			latest, msg = info.Latest, i18n.Tf("有新版本 %s，当前为 %s。点击「更新」安装", info.Latest, current)
		case info.Installed && info.Latest != current:
			msg = i18n.Tf("最新版本 %s 已安装，可在列表中点「使用」切换", info.Latest)
		}
		p.mu.Lock()
		p.latest = latest
		p.mu.Unlock()
		e.Toast(msg, false)
	})
}

// pickCoreFile is the system's file dialog for a Mihomo binary (a
// variable so that tests can answer it).
var pickCoreFile = func() ([]string, error) {
	opts := mygo.OpenDialogOptions{
		Title:   i18n.T("选择 Mihomo 内核文件"),
		Message: i18n.T("选择一个 Mihomo 可执行文件；留空则使用内置内核。"),
	}
	if runtime.GOOS == "windows" {
		opts.Filters = []mygo.FileFilter{{Name: "Mihomo", Extensions: []string{"exe"}}}
	}
	return mygo.Dialog.Open(opts)
}

// chooseCore opens the system's file dialog for a Mihomo binary. On
// the live UI button click it runs off the UI thread so MyGo's blocking
// Dialog.Open never stalls or deadlocks the loop; tests also invoke
// chooseCore() directly.
func (p *Page) chooseCore() {
	paths, err := pickCoreFile()
	if err != nil || len(paths) == 0 {
		return
	}
	chosen := paths[0]
	apply := func() {
		// The snapshot is taken on the UI thread after the dialog
		// closes: settings saved behind it are preserved.
		s := p.current()
		s.CorePath = chosen
		s.CoreVersion = "" // a hand-picked file is not one of the installed versions
		p.save(s, i18n.T("已更换内核文件，重启内核后生效"), nil)
	}
	if p.e.RunThen != nil {
		p.e.OnUI(apply)
	} else {
		apply()
	}
}

func (p *Page) geoSection(c *ui.Context, t *ui.Theme, s model.Settings, running bool) {
	e := p.e
	g := s.Geo
	urls := []struct {
		label, desc string
		get         func(*model.GeoSettings) *string
	}{
		{i18n.T("GeoIP 数据库"), i18n.T("IP → 国家（DAT 格式）。选择 DAT 时用于 GEOIP 规则"), func(g *model.GeoSettings) *string { return &g.GeoIP }},
		{i18n.T("GeoSite 数据库"), i18n.T("域名分类，如 cn、google、youtube，供 GEOSITE 规则使用"), func(g *model.GeoSettings) *string { return &g.GeoSite }},
		{i18n.T("MMDB 数据库"), i18n.T("IP → 国家（MaxMind 格式）。默认用于 GEOIP 规则"), func(g *model.GeoSettings) *string { return &g.MMDB }},
		{i18n.T("ASN 数据库"), i18n.T("IP → 运营商，供 IP-ASN 规则和「网络」页使用"), func(g *model.GeoSettings) *string { return &g.ASN }},
	}
	e.Kit.Section(c, i18n.T("外部资源"), i18n.T("规则匹配用到的地理数据库"), func() {
		for i, u := range urls {
			i, u := i, u
			stackedRow(c, t, u.label, u.desc, func() {
				f := &p.geoURLs[i]
				f.show(*u.get(&g))
				in := ui.TextInput(c.Key("geo-"+strconv.Itoa(i)), &f.text).Font("monospace").FontSize(12).FillWidth().Label(u.label)
				if f.commit(in) {
					v := strings.TrimSpace(f.text)
					if v != *u.get(&g) && (strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")) {
						*u.get(&s.Geo) = v
						p.save(s, "", nil)
					}
				}
			})
		}
		e.Kit.Row(c, i18n.T("GEOIP 规则使用"), i18n.T("对应 geodata-mode。一般保持 MMDB；订阅规则依赖 DAT 特有的分类时再改为 DAT"), func() {
			mode := 0
			if g.Mode == "dat" {
				mode = 1
			}
			if ui.Segmented(c.Key("geomode"), &mode, "MMDB", "DAT").Label(i18n.T("GEOIP 规则使用")).Changed() {
				s.Geo.Mode = []string{"db", "dat"}[mode]
				p.save(s, "", nil)
			}
		})
		e.Kit.Row(c, i18n.T("自动更新"), i18n.T("由内核按间隔下载（geo-auto-update）"), func() {
			ui.Row(c).Gap(10).Children(func() {
				if g.AutoUpdate {
					opts := []string{i18n.T("每 12 小时"), i18n.T("每 24 小时"), i18n.T("每 3 天"), i18n.T("每周")}
					vals := []int{12, 24, 72, 168}
					cur := opts[1]
					for i, v := range vals {
						if v == g.IntervalHours {
							cur = opts[i]
						}
					}
					if ui.Select(c.Key("geoint"), &cur, opts).Label(i18n.T("更新间隔")).Changed() {
						for i, o := range opts {
							if o == cur {
								s.Geo.IntervalHours = vals[i]
							}
						}
						p.save(s, "", nil)
					}
				}
				on := g.AutoUpdate
				if ui.Switch(c.Key("geoauto"), &on).Label(i18n.T("自动更新")).Changed() {
					s.Geo.AutoUpdate = on
					if s.Geo.IntervalHours == 0 {
						s.Geo.IntervalHours = 24
					}
					p.save(s, "", nil)
				}
			})
		})
		e.Kit.Row(c, i18n.T("上次更新"), i18n.T("应用内置一份初始数据库，首次启动无需下载"), func() {
			ui.Row(c).Gap(10).Children(func() {
				ui.Text(c, geoUpdatedText(e.API.GeoDataInfo(), g.UpdatedAt)).TextColor(t.TextMuted).FontFeatures("tnum")
				ui.Button(c, i18n.T("立即更新")).Disabled(!running).Tooltip(i18n.T("需要内核运行")).OnClick(func() {
					e.Run(e.API.UpdateGeoData, i18n.T("地理数据库已更新"))
				})
			})
		})
	})
}

// geoUpdatedText is the 外部资源 · 上次更新 value (docs/design.md:526): read
// from the database files themselves, so the core's own scheduled update
// counts too; the shipped copies say so instead of showing a date. The
// time of the last manual 立即更新 is the fallback when no file is found.
func geoUpdatedText(info model.GeoDataInfo, manual time.Time) string {
	switch {
	case info.Files > 0 && info.Bundled:
		return i18n.T("随应用附带")
	case !info.UpdatedAt.IsZero():
		return env.FmtDateTime(info.UpdatedAt.Local())
	case !manual.IsZero():
		return env.FmtDate(manual)
	}
	return "—"
}

// ---- shared rows ----

func (p *Page) switchRow(c *ui.Context, label, desc string, on bool, set func(bool)) {
	p.e.Kit.Row(c, label, desc, func() {
		v := on
		if ui.Switch(c.Key(label), &v).Label(label).Disabled(p.saving).Changed() {
			set(v)
		}
	})
}

func (p *Page) selectRow(c *ui.Context, label, desc, value string, options []string, set func(string)) {
	p.e.Kit.Row(c, label, desc, func() {
		v := value
		if ui.Select(c.Key(label), &v, options).Label(label).Changed() {
			set(v)
		}
	})
}

func (p *Page) applyBar(c *ui.Context, t *ui.Theme, discard, apply func()) {
	ui.Row(c).Gap(10).Padding(10, 24).Background(t.Surface).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, i18n.T("有未应用的更改。应用时会重新生成配置、校验，并让内核重新加载。")).FontSize(12).Grow(1)
		ui.Button(c, i18n.T("放弃")).Disabled(p.applying).OnClick(discard)
		label := i18n.T("应用")
		if p.applying {
			label = i18n.T("应用中…")
		}
		ui.PrimaryButton(c, label).Disabled(p.applying).OnClick(apply)
	})
}

// show sets the text shown while the field is not being edited.
func (f *field) show(current string) {
	if !f.editing {
		f.text = current
	}
}

// commit reports that the input was submitted or lost the focus after an
// edit; the caller then reads f.text.
func (f *field) commit(in ui.Element) bool {
	if in.Focused() {
		f.editing = true
	}
	if in.Submitted() || (f.editing && in.FocusLeft()) {
		f.editing = false
		return true
	}
	return false
}

// revealLabel names the button that shows a file in the system's file
// manager: 访达 on macOS, 资源管理器 on Windows, 文件管理器 elsewhere.
func revealLabel(e *env.Env) string { return revealLabelFor(e.Mac, runtime.GOOS) }

func revealLabelFor(mac bool, goos string) string {
	switch {
	case mac:
		return i18n.T("在访达中显示")
	case goos == "windows":
		return i18n.T("在资源管理器中显示")
	}
	return i18n.T("在文件管理器中显示")
}

func mapName(value string, values, names []string) string {
	for i, v := range values {
		if v == value {
			return names[i]
		}
	}
	return names[0]
}

func mapValue(name string, names, values []string) string {
	for i, n := range names {
		if n == name {
			return values[i]
		}
	}
	return values[0]
}

// lines splits a textarea into trimmed, non-empty lines (nil when none).
func lines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func joinLines(xs []string) string { return strings.Join(xs, "\n") }

// yaml is the editor language for override texts.
var yaml = editor.LanguageFor("override.yaml")

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicSettings, app.TopicSubs, app.TopicCoreDownload, app.TopicCores, app.TopicNet}
}

// stackedRow is a settings row whose control (a URL input, say) takes the
// row's whole width under the label and description: beside them, a
// fixed-width input squeezed the description into a narrow column of
// many lines and still cut the URL off.
func stackedRow(c *ui.Context, t *ui.Theme, label, desc string, control func()) {
	ui.Column(c).Gap(6).Padding(8, 12).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, label).FontSize(13)
			if desc != "" {
				ui.Text(c, desc).FontSize(11).TextColor(t.TextMuted)
			}
		})
		control()
	})
}
