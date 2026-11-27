// 常规 and 内核 on a phone (docs/phone-ui.md): native list rows in place
// of the desktop's bordered inputs, buttons and pop-up menus — a value
// and › that push an editor or a list of choices, switches kept, and an
// action row (env.ActionRow) for each button. The shared pieces here are
// a block of rows with a header and a footnote, and a row that only shows
// a value.

package settings

import (
	"errors"
	"path"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/logs"
)

// phoneBlock is a group of rows on a phone: its header (none when ""),
// the rows, and a footnote under them (none when "").
func phoneBlock(c *ui.Context, title, foot string, rows func()) {
	t := c.Theme()
	sec := ui.Column(c).Gap(6).Shrink(0).FillWidth()
	lit := title != "" && env.Reveal(sec, title) // a jump from the search
	sec.Children(func() {
		if title != "" {
			tx := ui.Text(c, title).FontSize(13).FontWeight(600).Padding(0, 4).SingleLine()
			if lit {
				tx.TextColor(t.Accent)
			}
		}
		phoneGroup(c, rows)
		if foot != "" {
			phoneFoot(c, foot)
		}
	})
}

// phoneFoot is a footnote of a group, or a note on a pushed page.
func phoneFoot(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted).PaddingX(14)
}

// phoneDangerNote is a note in the danger color on a pushed page.
func phoneDangerNote(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(13).TextColor(c.Theme().Danger).PaddingX(14)
}

// infoRow is a row that only shows: the label (and a smaller second
// line), the value muted at the end; no chevron, no tap.
func infoRow(c *ui.Context, label, desc, value string) {
	t := c.Theme()
	row := ui.Row(c.Key("info:"+label)).MinHeight(env.TouchSize).Padding(8, 14).Gap(10).AlignItems(ui.Center).FillWidth()
	if env.Reveal(row, label) {
		row.Background(t.Accent.Alpha(0.12))
	}
	row.Children(func() {
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
			ui.Text(c, label).FontSize(15)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(t.TextMuted)
			}
		})
		if value != "" {
			ui.Text(c, value).FontSize(15).TextColor(t.TextMuted).SingleLine().Shrink(1).MaxWidth(170).FontFeatures("tnum")
		}
	})
}

// phoneSpacer leaves the gap between two groups of a pushed page.
func phoneSpacer(c *ui.Context) { ui.Box(c).Height(8) }

// phoneField is a full-width input in a group, no border; password hides
// what is typed. It reports a submit.
func phoneField(c *ui.Context, key, label, placeholder string, text *string, password, disabled bool) ui.Element {
	var in ui.Element
	ui.Box(c).Padding(6, 10).Children(func() {
		in = ui.TextInput(c.Key(key), text).Label(label).Placeholder(placeholder).FillWidth().Height(36).FontSize(16).
			Border(0, ui.Transparent).Background(ui.Transparent).Autocorrect(false).Capitalize(ui.CapitalizeNone).Disabled(disabled)
		if password {
			in.Password()
		}
	})
	return in
}

// httpURL reports whether v is an http:// or https:// address.
func httpURL(v string) bool {
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")
}

// ---- 常规 ----

// phoneGeneral is 常规 on a phone: no login item, window, tray, system
// proxy, shortcuts, folder or self-update; the look, 抓包, the data, the
// sync, the backup and 关于, as list rows.
func (p *Page) phoneGeneral(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	e.Kit.Section(c, i18n.T("常规"), "", func() { p.lookRows(c, s) })
	e.Kit.Section(c, i18n.T("功能"), "", func() {
		p.switchRow(c, i18n.T("抓包（HTTPS 解密）"), i18n.T("显示「抓包」页，可按域名或程序解密 HTTPS 请求；关闭时同时停止解密"), s.CaptureEnabled, func(on bool) {
			s.CaptureEnabled = on
			p.save(s, "", nil)
		})
	})
	phoneBlock(c, i18n.T("数据"), i18n.T("设置、覆写与自定义规则保存为 lanflow.yaml；导入会按文件内容应用"), func() {
		if env.ActionRow(c, i18n.T("导出设置…"), false, false).Clicked() {
			p.export.subs = false
			p.pushPage(i18n.T("导出设置"), p.phoneExport)
		}
		if env.ActionRow(c, i18n.T("导入设置…"), false, false).Clicked() {
			go p.importSettings()
		}
	})
	phoneBlock(c, "", i18n.T("把日志、运行时配置和网络状态打包成 ZIP，便于排查问题；节点地址、密码和订阅令牌已去除"), func() {
		if env.ActionRow(c, i18n.T("导出诊断信息…"), false, false).Clicked() {
			go logs.ExportDiagnostics(e)
		}
	})
	p.phoneCacheRows(c)   // 缓存 (caches.go)
	p.phoneConfigSync(c)  // 配置同步 (configsync.go)
	p.phoneBackup(c, s)   // 备份与同步 (backup.go)
	p.phoneAbout(c, t, s) // 关于 (feedback.go)
}

// phoneAbout is 关于 on a phone: the version, the links as rows, 反馈.
func (p *Page) phoneAbout(c *ui.Context, t *ui.Theme, s model.Settings) {
	open := func(u string) { _ = mygo.Shell.OpenExternal(u) }
	phoneBlock(c, i18n.T("关于"), i18n.T("基于 Mihomo（GPL-3.0）"), func() {
		infoRow(c, i18n.T("澜流 Lanflow"), "", AppVersion())
		if p.navRow(c, i18n.T("Mihomo 源代码"), "", "") {
			open("https://github.com/MetaCubeX/mihomo/tree/" + app.PinnedCoreVersion)
		}
		if p.navRow(c, i18n.T("许可证"), "", "GPL-3.0") {
			open("https://github.com/MetaCubeX/mihomo/blob/" + app.PinnedCoreVersion + "/LICENSE")
		}
		if p.navRow(c, i18n.T("在 GitHub 上点 Star"), i18n.T("觉得好用欢迎点个 Star，也可在那里关注新版本"), "") {
			open(repoURL)
		}
	})
	p.phoneFeedback(c, t, s)
}

// ---- 内核 ----

// phoneKernel is 内核 on a phone: the core comes with the app, in its
// VPN; its version, the log level, 重启内核, the geo databases and the
// download mirror.
func (p *Page) phoneKernel(c *ui.Context, t *ui.Theme, s model.Settings, st model.Status) {
	e := p.e
	phoneBlock(c, i18n.T("内核"), "", func() {
		infoRow(c, i18n.T("内核版本"), "", env.Dash(st.CoreVersion))
		lvlOpts := []string{i18n.T("调试"), i18n.T("信息"), i18n.T("警告"), i18n.T("错误"), i18n.T("静默")}
		lvlVals := []string{"debug", "info", "warning", "error", "silent"}
		p.selectRow(c, i18n.T("日志级别"), "", mapName(s.LogLevel, lvlVals, lvlOpts), lvlOpts, func(v string) {
			s := p.current()
			s.LogLevel = mapValue(v, lvlOpts, lvlVals)
			p.save(s, "", nil)
		})
		if env.ActionRow(c, i18n.T("重启内核"), false, st.Core == model.CoreMissing || st.Core == model.CoreStarting).Clicked() {
			e.Run(e.API.RestartCore, i18n.T("内核已重启"))
		}
	})
	p.phoneGeo(c, s, st.Core == model.CoreRunning)
	p.phoneMirror(c, s) // 下载镜像 (mirror.go)
}

// phoneGeo is 外部资源 on a phone: a row per database (the file's name,
// its link behind), the mode, the automatic update and 更新地理数据库.
func (p *Page) phoneGeo(c *ui.Context, s model.Settings, running bool) {
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
	phoneBlock(c, i18n.T("外部资源"), i18n.T("规则匹配用到的地理数据库"), func() {
		for _, u := range urls {
			u := u
			cur := *u.get(&g)
			name := i18n.T("未设置")
			if strings.TrimSpace(cur) != "" {
				name = path.Base(cur)
			}
			if p.navRow(c, u.label, "", name) {
				p.pushEdit(u.label, u.desc, cur, func(v string) error {
					v = strings.TrimSpace(v)
					s := p.current()
					if v == *u.get(&s.Geo) {
						return nil
					}
					if !httpURL(v) {
						return errors.New(i18n.T("地址须以 http:// 或 https:// 开头"))
					}
					*u.get(&s.Geo) = v
					p.save(s, "", nil)
					return nil
				}, mono, placeholderOpt("https://"))
			}
		}
		modeOpts := []string{"MMDB", "DAT"}
		p.selectRow(c, i18n.T("GEOIP 规则使用"), i18n.T("对应 geodata-mode。一般保持 MMDB；订阅规则依赖 DAT 特有的分类时再改为 DAT"),
			mapName(g.Mode, []string{"db", "dat"}, modeOpts), modeOpts, func(v string) {
				s := p.current()
				s.Geo.Mode = mapValue(v, modeOpts, []string{"db", "dat"})
				p.save(s, "", nil)
			})
	})
	foot := i18n.T("应用内置一份初始数据库，首次启动无需下载")
	if !running {
		foot = i18n.T("连接后才能更新地理数据库")
	}
	phoneBlock(c, "", foot, func() {
		p.switchRow(c, i18n.T("自动更新"), i18n.T("由内核按间隔下载（geo-auto-update）"), g.AutoUpdate, func(on bool) {
			s.Geo.AutoUpdate = on
			if s.Geo.IntervalHours == 0 {
				s.Geo.IntervalHours = 24
			}
			p.save(s, "", nil)
		})
		if g.AutoUpdate {
			opts := []string{i18n.T("每 12 小时"), i18n.T("每 24 小时"), i18n.T("每 3 天"), i18n.T("每周")}
			vals := []int{12, 24, 72, 168}
			cur := opts[1]
			for i, v := range vals {
				if v == g.IntervalHours {
					cur = opts[i]
				}
			}
			p.selectRow(c, i18n.T("更新间隔"), "", cur, opts, func(v string) {
				s := p.current()
				for i, o := range opts {
					if o == v {
						s.Geo.IntervalHours = vals[i]
					}
				}
				p.save(s, "", nil)
			})
		}
		infoRow(c, i18n.T("上次更新"), "", geoUpdatedText(e.API.GeoDataInfo(), g.UpdatedAt))
		if env.ActionRow(c, i18n.T("更新地理数据库"), false, !running).Clicked() {
			e.Run(e.API.UpdateGeoData, i18n.T("地理数据库已更新"))
		}
	})
}
