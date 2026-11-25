// Package overview is the 概览 page (docs/design.md §5.1): the switches,
// the exit map with its route list, the subscription and live-speed
// cards and the terminal proxy command.
package overview

import (
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	mu           sync.Mutex
	probing      bool
	side         float32 // the side column's share in percent, 0 = sideDefault (divider.go), remembered in the UI state

	flows     map[string]*flow // exit map animation state by route group
	land      landCache
	ground    ground     // the graticule and land dots, drawn once per view (globe_ground.go)
	view      viewState  // projection glide
	dests     destState  // destination countries by route
	hits      []hitArc   // arcs of the last frame, for click testing
	hitOrigin [2]float32 // map box origin of the last frame
}

// New returns the page.
func New(e *env.Env) *Page { return &Page{e: e, flows: map[string]*flow{}} }

// ProxyCommand is the shell command that points a terminal at the proxy,
// for the usual shell of the system goos names (a runtime.GOOS value):
// PowerShell on Windows, bash/zsh on macOS and Linux. It is one of the
// variants the tray's submenu offers (env.ShellCommands).
func ProxyCommand(port int, goos string) string { return env.ShellCommandFor(port, goos).Cmd }

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	k := p.e.Kit
	st := p.e.API.Status()
	k.Header(c, i18n.T("概览"), "", nil)
	if !st.HasSubscriptions {
		p.emptyState(c, st)
		return
	}
	// The whole page fits the window without scrolling (docs/design.md
	// §4): under the switches, the exit globe takes the left two thirds
	// and all the height; the routes, the subscription, the speed and the
	// terminal command stack on the right, the routes taking what the
	// others leave.
	// The routes are read, and their speeds eased, once per build for
	// both the map and the list.
	rs := p.routes(time.Now())
	p.e.PaneSize(env.SizeOverviewSide, &p.side)
	side := p.sideShare()
	ui.Column(c).Grow(1).Padding(0, 20, 16, 20).Gap(12).Clip().Children(func() {
		p.statusAlerts(c, st)
		// The divider between the map and the side column drags (or,
		// focused, takes the arrows) to give either more room: long node
		// names and merged groups in the route list, or a bigger globe.
		main := ui.Row(c.Key("main")).Grow(1).Shrink(1).MinHeight(0).AlignItems(ui.Stretch)
		total := main.Bounds().W
		main.Children(func() {
			p.exitCard(c, st, rs, 100-side)
			p.sideDivider(c, total)
			ui.Column(c).Grow(side).Basis(0).MinWidth(sideMinWidth).MinHeight(0).Gap(10).Children(func() {
				p.routeCard(c, st, rs)
				p.terminalRow(c, st)
			})
		})
	})
}

// statusAlerts shows only actionable error/warning banners (core crashed,
// core missing, or config sync error), since normal switches and traffic now
// live permanently in the sidebar.
func (p *Page) statusAlerts(c *ui.Context, st model.Status) {
	k := p.e.Kit
	if st.Core == model.CoreError {
		msg := i18n.T("Mihomo 内核意外退出，自动重启也失败了。系统代理已自动关闭，避免断网。")
		if st.CoreError != "" {
			msg = i18n.Tf("Mihomo 内核意外退出：%s。系统代理已自动关闭，避免断网。", st.CoreError)
		}
		k.Banner(c, "error", msg, func() {
			if ui.Button(c, i18n.T("查看日志")).Clicked() {
				p.e.Navigate(env.Logs, "")
			}
			if ui.PrimaryButton(c, i18n.T("重新启动")).Clicked() {
				p.run(func() error { return p.e.API.RestartCore() }, i18n.T("内核已重启"), nil)
			}
		})
	}
	if st.Core == model.CoreMissing {
		p.missingBanner(c, st)
	}
	if cs := p.e.API.ConfigSyncStatus(); cs.Error != "" || len(cs.Conflicts) > 0 || cs.Locked {
		msg := i18n.Tf("同步文件有冲突副本（%s），请选择保留哪一个。", strings.Join(cs.Conflicts, i18n.T("、")))
		kind := "warn"
		if len(cs.Conflicts) == 0 {
			msg, kind = i18n.T("输入同步口令以解锁订阅链接。"), "info"
		}
		if cs.Error != "" {
			msg, kind = i18n.Tf("同步文件的修改没有加载，配置保持不变：%s", cs.Error), "error"
		}
		k.Banner(c, kind, msg, func() {
			if ui.Button(c, i18n.T("查看")).Clicked() {
				p.e.Navigate(env.Settings, "general")
			}
		})
	}
}

// emptyState is the first-run guide (no subscriptions yet).
func (p *Page) emptyState(c *ui.Context, st model.Status) {
	k := p.e.Kit
	ui.Column(c).Grow(1).Center().Gap(20).Children(func() {
		k.Empty(c, "subs", i18n.T("还没有订阅"), i18n.T("导入机场提供的订阅链接或 YAML 配置文件后即可开始使用。"), func() {
			if ui.PrimaryButton(c, i18n.T("导入订阅")).Clicked() {
				p.e.Navigate(env.Subs, "import-url")
			}
			if ui.Button(c, i18n.T("从文件导入")).Clicked() {
				p.e.Navigate(env.Subs, "import-file")
			}
		})
		if st.Core == model.CoreMissing {
			ui.Box(c).MaxWidth(520).Children(func() { p.missingBanner(c, st) })
		}
	})
}

// run runs job through Env.Run and clears the optimistic switch state
// once it is done.
func (p *Page) run(job func() error, ok string, field **bool) {
	p.e.Run(func() error {
		err := job()
		if field != nil {
			p.mu.Lock()
			*field = nil
			p.mu.Unlock()
		}
		return err
	}, ok)
}

// missingBanner tells that the core binary was not found.
func (p *Page) missingBanner(c *ui.Context, st model.Status) {
	dl := p.e.API.CoreDownload()
	if dl.Active {
		p.e.Kit.Banner(c, "info", env.CoreDownloadText(dl), func() {
			ui.Spinner(c).Label(i18n.T("正在下载内核")).Size(14, 14)
		})
		return
	}
	msg := i18n.T("未找到 Mihomo 内核：内核文件不存在或无法执行。可下载官方内核，或选择本机已有的内核文件。")
	if st.CoreError != "" {
		msg = i18n.Tf("未找到 Mihomo 内核：%s", st.CoreError)
	}
	if dl.Error != "" {
		msg = dl.Error
	}
	p.e.Kit.Banner(c, "error", msg, func() {
		if ui.Button(c, i18n.T("选择内核文件")).Clicked() {
			go p.pickCore()
		}
		if ui.PrimaryButton(c, i18n.T("下载内核")).Clicked() {
			p.e.Run(func() error { return p.e.API.DownloadCore("") }, i18n.T("Mihomo 内核已下载完成"))
		}
	})
}

// pickCore lets the user choose a core binary and saves it in settings.
func (p *Page) pickCore() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: i18n.T("选择 Mihomo 内核文件")})
	if err != nil || len(paths) == 0 {
		return
	}
	// Only the two options are saved (app.EditSettings): the dialog stays
	// open as long as the user likes while other settings change.
	p.e.Run(func() error {
		return app.EditSettings(p.e.API, func(s *model.Settings) {
			s.CorePath = paths[0]
			s.CoreVersion = "" // a hand-picked file is not one of the installed versions
		})
	}, i18n.T("已选择内核文件"))
}

// ---- cards ----

func cardTitle(c *ui.Context, title string, right func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(24).Children(func() {
		ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine()
		ui.Spacer(c)
		if right != nil {
			right()
		}
	})
}

// expiryText is the subscription's expiry label and colour: red once
// expired, orange within a week, muted otherwise.
func expiryText(expire, now time.Time, t *ui.Theme) (string, ui.Color) {
	switch {
	case expire.Before(now):
		return i18n.T("已过期"), t.Danger
	case expire.Sub(now) < 7*24*time.Hour:
		return i18n.Tf("到期 %s", env.FmtDate(expire)), t.Warning
	default:
		return i18n.Tf("到期 %s", env.FmtDate(expire)), t.TextMuted
	}
}

// terminalRow shows the shell command with a copy button.
func (p *Page) terminalRow(c *ui.Context, st model.Status) {
	t := c.Theme()
	goos := runtime.GOOS
	if p.e.Mac {
		goos = "darwin"
	}
	cmd := ProxyCommand(st.MixedPort, goos)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Text(c, i18n.T("终端代理")).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
		ui.Text(c, cmd).Font("monospace").FontSize(11).SingleLine().Grow(1).Shrink(1).Basis(0).Padding(4, 8).Radius(6).
			Background(t.Surface).Border(1, t.Border).Selectable().Tooltip(cmd)
		b := ui.Button(c, "").Label(i18n.T("复制终端代理命令")).Tooltip(i18n.T("复制到剪贴板")).Shrink(0).Children(func() {
			p.e.Kit.Icon(c, "copy", 13)
			ui.Text(c, i18n.T("复制")).SingleLine()
		})
		if b.Clicked() {
			c.WriteClipboard(cmd)
			p.e.Toast(i18n.T("已复制"), false)
		}
	})
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicTraffic, app.TopicProxies, app.TopicSubs, app.TopicConns, app.TopicExits, app.TopicSettings, app.TopicCoreDownload}
}
