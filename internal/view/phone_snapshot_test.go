package view

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Phone screen sizes in points: an iPhone's 390×844 with its status bar and
// home indicator, and a narrow Android phone's 360×780.
var phoneSizes = []struct {
	name                string
	w, h                int
	insetTop, insetDown float32
	look                mobile.Look
}{
	{"iphone", 390, 844, 47, 34, mobile.Cupertino},
	{"android", 360, 780, 24, 0, mobile.Material},
}

// phoneSnapshots are the phone's frames for review: every tab, 活动's
// segments, 设置 and its pages, the settings' sections and a few states.
var phoneSnapshots = []snapshot{
	// 首页: connected, not connected, connecting, an error, first run.
	{name: "home", page: env.Overview},
	{name: "home-stopped", page: env.Overview, prep: stopCore},
	{name: "home-connecting", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, _ *ui.Tester) {
		setCore(s, model.CoreStarting, "")
	}},
	{name: "home-error", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, _ *ui.Tester) {
		setCore(s, model.CoreError, "start mihomo: permission denied")
	}},
	{name: "home-first-run", page: env.Overview, prep: snapFirstRun},
	// Notifications (docs/notifications.md): a confirmation, a failure with
	// its action, the same note twice, a core crash from the service.
	{name: "toast-done", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		showNote(s, tt, env.Note{Title: i18n.Tf("已切换到 %s", "东京 03")})
	}},
	{name: "toast-error", page: env.Subs, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		s.errorToast(i18n.T("订阅更新失败：连接超时。仍在使用上次的配置。"), "")
		settleToast(tt)
	}},
	{name: "toast-merged", page: env.Proxies, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		showNote(s, tt, env.Note{Title: i18n.T("已复制")})
		showNote(s, tt, env.Note{Title: i18n.T("已复制")})
		showNote(s, tt, env.Note{Level: env.News, Title: i18n.T("已重新连接澜流后台服务")})
	}},
	{name: "toast-core-crash", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		queueNotice(s, app.Notice{Kind: "core-crash", Detail: "exit status 1"})
		settleToast(tt)
	}},
	{name: "home-scrolled", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Scroll(195, 500, 0, 300)
		tt.Advance(time.Second)
		tt.Frame()
	}},
	{name: "proxies", page: env.Proxies},
	{name: "proxies-group", page: env.Proxies, arg: "流媒体"},
	{name: "proxies-search", page: env.Proxies, arg: env.QueryArg("东京")},
	{name: "proxies-stopped", page: env.Proxies, prep: stopCore},
	{name: "subs", page: env.Subs},
	// 活动's segments.
	{name: "activity-conns", page: env.Conns},
	{name: "activity-conns-detail", page: env.Conns, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		for _, txt := range tt.Texts() {
			if strings.HasSuffix(txt, ":443") {
				if r, ok := tt.Find(txt); ok {
					tapAt(tt, r)
				}
				return
			}
		}
	}},
	{name: "activity-map", page: env.Conns, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("地图"))
	}},
	{name: "activity-logs", page: env.Logs},
	// 设置 and its pages.
	{name: "settings", page: env.Overview, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tapTab(t, tt, i18n.T("设置"))
	}},
	{name: "settings-scrolled", page: env.Overview, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tapTab(t, tt, i18n.T("设置"))
		tt.Scroll(195, 500, 0, 300)
		tt.Advance(time.Second)
		tt.Frame()
	}},
	{name: "rules", page: env.Rules},
	{name: "net", page: env.Net},
	{name: "net-connectivity", page: env.Net, prep: clickOnly("连通性")},
	{name: "net-dns", page: env.Net, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, "DNS")
		tt.Frame()
		click(t, tt, i18n.T("查询"))
	}},
	{name: "net-scrolled", page: env.Net, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Scroll(195, 500, 0, 600)
	}},
	{name: "stats", page: env.Stats},
	{name: "stats-domain", page: env.Stats, prep: clickOnly("域名")},
	{name: "stats-process", page: env.Stats, prep: clickOnly("进程")},
	{name: "stats-proxy", page: env.Stats, prep: clickOnly("代理|统计")},
	{name: "stats-scrolled", page: env.Stats, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Scroll(195, 500, 0, 600)
	}},
	{name: "stats-empty", page: env.Stats, prep: func(t *testing.T, api *fake.Fake, _ *Shell, _ *ui.Tester) {
		if err := api.ResetTrafficStats(); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "probe", page: env.Probe},
	{name: "probe-leak-result", page: env.Probe, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("开始检查"))
		tt.Frame()
		_ = tt.Click(i18n.T("仅遵守系统代理的应用由澜流解析域名"))
	}},
	{name: "probe-ip-result", page: env.Probe, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("IP 纯净度"))
		tt.Frame()
		click(t, tt, i18n.T("检测出口"))
		tt.Frame()
		click(t, tt, i18n.T("在线检测"))
	}},
	{name: "probe-unlock-result", page: env.Probe, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("解锁检测"))
		tt.Frame()
		click(t, tt, i18n.T("开始检测"))
	}},
	{name: "probe-region-result", page: env.Probe, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("地区探查"))
		tt.Frame()
		click(t, tt, i18n.T("开始探查"))
	}},
	{name: "aggregate", page: env.Aggregate},
	{name: "capture", page: env.Capture},
	{name: "settings-general", page: env.Settings, arg: "general"},
	{name: "settings-proxy", page: env.Settings, arg: "proxy"},
	{name: "settings-dns", page: env.Settings, arg: "dns"},
	{name: "settings-sniffer", page: env.Settings, arg: "sniffer"},
	{name: "settings-override", page: env.Settings, arg: "override"},
	// DNS taken over, its 上游服务器 pushed; its per-domain servers.
	{name: "settings-dns-edit", page: env.Settings, arg: "dns", prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		phoneClick(t, tt, i18n.T("DNS 设置来源"))
		phoneClick(t, tt, i18n.T("由澜流接管"))
		phoneClick(t, tt, i18n.T("上游服务器"))
	}},
	{name: "settings-dns-policy", page: env.Settings, arg: "dns", prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		phoneClick(t, tt, i18n.T("DNS 设置来源"))
		phoneClick(t, tt, i18n.T("由澜流接管"))
		phoneClick(t, tt, i18n.T("使用推荐设置"))
		tt.Scroll(195, 400, 0, 420)
		tt.Advance(time.Second)
		tt.Frame()
	}},
	{name: "settings-dns-policy-edit", page: env.Settings, arg: "dns", prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		phoneClick(t, tt, i18n.T("DNS 设置来源"))
		phoneClick(t, tt, i18n.T("由澜流接管"))
		phoneClick(t, tt, "geosite:geolocation-!cn")
	}},
	{name: "settings-override-entry", page: env.Settings, arg: "override", prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		phoneClick(t, tt, "去广告规则")
	}},
	{name: "settings-kernel", page: env.Settings, arg: "kernel"},
	{name: "settings-advanced", page: env.Settings, arg: "advanced"},
	{name: "settings-general-mid", page: env.Settings, arg: "general", prep: func(_ *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Scroll(195, 500, 0, 700)
		tt.Advance(time.Second)
		tt.Frame()
	}},
	{name: "settings-general-scrolled", page: env.Settings, arg: "general", prep: func(_ *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Scroll(195, 500, 0, 5000)
		tt.Advance(time.Second)
		tt.Frame()
	}},
	{name: "settings-kernel-scrolled", page: env.Settings, arg: "kernel", prep: func(_ *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Scroll(195, 500, 0, 5000)
		tt.Advance(time.Second)
		tt.Frame()
	}},
	{name: "settings-edit", page: env.Settings, arg: "kernel", prep: clickOnly("GeoIP 数据库")},
	{name: "settings-export", page: env.Settings, arg: "general", prep: clickOnly("导出设置…")},
	{name: "dialog-import", page: env.Subs, arg: "import-url"},
	// 订阅, 聚合订阅 and 规则 (internal/view/subs, internal/view/rules).
	{name: "subs-empty", page: env.Subs, prep: snapFirstRun},
	{name: "subs-swiped", page: env.Subs, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		r, ok := tt.Find("备用机场")
		if !ok {
			t.Fatalf("no 备用机场: %q", tt.Texts())
		}
		// The progress bar spans the card: start a finger near its end.
		bar, _ := tt.Find(i18n.T("已用流量"))
		x, y := bar.X+bar.W-8, r.Y+r.H/2
		tt.FingerDown(x, y)
		tt.Advance(50 * time.Millisecond)
		for range 9 {
			x -= 20
			tt.FingerMove(x, y)
			tt.Frame()
			tt.Advance(16 * time.Millisecond)
		}
		tt.FingerUp(x, y)
		for range 120 {
			tt.Advance(16 * time.Millisecond)
		}
	}},
	{name: "aggregate-list", page: env.Aggregate, prep: aggregateSaved},
	{name: "aggregate-new", page: env.Aggregate, arg: "new"},
	{name: "aggregate-edit", page: env.Aggregate, prep: aggregatePrep()},
	{name: "aggregate-edit-preview", page: env.Aggregate, prep: aggregatePrep("生成配置", "预览")},
	{name: "rules-custom", page: env.Rules, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("自定义"))
	}},
	{name: "rules-add", page: env.Rules, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("添加规则"))
	}},
	{name: "rules-test", page: env.Rules, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("测试…"))
		tt.Frame()
		tt.Type("www.google.com")
		click(t, tt, i18n.T("测试"))
	}},
}

// TestPhoneSnapshots renders phoneSnapshots on phone screens, light and
// dark, to LANFLOW_SNAP_DIR (skipped otherwise), as TestSnapshots does
// for the desktop; LANFLOW_SNAP_ONLY limits the run by name.
func TestPhoneSnapshots(t *testing.T) {
	dir := os.Getenv("LANFLOW_SNAP_DIR")
	if dir == "" {
		t.Skip("set LANFLOW_SNAP_DIR to write snapshots")
	}
	only := os.Getenv("LANFLOW_SNAP_ONLY")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mobile.SetLook(mobile.Automatic) })
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, sz := range phoneSizes {
			for _, sh := range phoneSnapshots {
				if only != "" && !strings.Contains(sh.name, only) {
					continue
				}
				mobile.SetLook(sz.look) // each system's bars, whatever runs the tests
				api, s, tt := newPhoneTest(t)
				tt.SetDark(dark)
				tt.SetSize(sz.w, sz.h)
				tt.SetInsets(sz.insetTop, 0, sz.insetDown, 0)
				s.navigate(sh.page, sh.arg)
				tt.Frame()
				if sh.prep != nil {
					sh.prep(t, api, s, tt)
					tt.Frame()
				}
				name := filepath.Join(dir, "phone-"+sh.name+"-"+sz.name+"-"+theme+".png")
				f, err := os.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(f, tt.Image()); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
		}
	}
}
