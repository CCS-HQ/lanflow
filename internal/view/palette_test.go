package view

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/view/env"
	"lanflow/internal/view/settings"
)

func TestPaletteOpensAndNavigates(t *testing.T) {
	_, s, tt := newTest(t)
	tt.Key(ui.Cmd, ui.KeyK)
	if !s.pal.open || !tt.HasText("页面") || !tt.HasText("开关系统代理") {
		t.Fatalf("⌘K did not open the search: %q", tt.Texts())
	}
	tt.Type("统计")
	tt.Key(0, ui.KeyEnter)
	if s.pal.open || s.Page() != env.Stats {
		t.Fatalf("Enter on 统计: open %v, page %s", s.pal.open, s.Page())
	}
}

func TestPaletteSettingsRowRevealed(t *testing.T) {
	_, s, tt := newTest(t)
	s.OpenSearch()
	tt.Frame()
	tt.Type("fake-ip-filter")
	if !tt.HasText("Fake-IP 排除") {
		t.Fatalf("config key did not find the row: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	sp := s.pages[env.Settings].(*settings.Page)
	if s.Page() != env.Settings || sp.Tab() != "dns" {
		t.Fatalf("page %s tab %s", s.Page(), sp.Tab())
	}
	if p := env.RevealPending(); p != "" {
		t.Errorf("the row was not revealed: %q pending", p)
	}
}

func TestPaletteSelectsNode(t *testing.T) {
	api, s, tt := newTest(t)
	main := api.Proxies().Groups[0]
	target := ""
	for _, n := range main.All {
		if n != main.Now && api.Proxies().Proxies[n].Type != "Selector" && !strings.Contains(n, "自动") {
			target = n
			break
		}
	}
	if target == "" {
		t.Skip("no other node in the main group")
	}
	s.OpenSearch()
	tt.Frame()
	tt.Type(target)
	tt.Key(0, ui.KeyEnter)
	if now := api.Proxies().Groups[0].Now; now != target {
		t.Fatalf("main group now %q, want %q (texts %q)", now, target, tt.Texts())
	}
}

func TestPaletteSearchInPage(t *testing.T) {
	_, s, tt := newTest(t)
	s.OpenSearch()
	tt.Frame()
	tt.Type("zzqq")
	click(t, tt, "在「日志」中搜索「zzqq」")
	if s.Page() != env.Logs || !tt.HasText("没有匹配“zzqq”的日志") {
		t.Fatalf("page %s, texts %q", s.Page(), tt.Texts())
	}
}

func TestPaletteArrowKeys(t *testing.T) {
	_, s, tt := newTest(t)
	s.OpenSearch()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyUp)
	if s.pal.sel != 1 {
		t.Fatalf("sel %d after ↓↓↑", s.pal.sel)
	}
	tt.Key(0, ui.KeyEnter)
	if s.Page() != env.Order[1] {
		t.Fatalf("Enter on the second item opened %s", s.Page())
	}
}

func TestPalFilter(t *testing.T) {
	items := []palItem{
		{group: palSettings, title: "DNS 劫持", match: "dns 劫持 dns-hijack"},
		{group: palPages, title: "日志", match: "日志 logs"},
		{group: palActions, title: "清空 DNS 缓存", match: "清空 dns 缓存 flush dns cache"},
		{group: palNodes, title: "DNS", match: "dns"},
	}
	if got := palFilter(items, ""); len(got) != 2 {
		t.Fatalf("empty query keeps pages and actions: %+v", got)
	}
	got := palFilter(items, "dns")
	if len(got) != 3 || got[0].title != "DNS" {
		t.Fatalf("an exact title comes first: %+v", got)
	}
	if got := palFilter(items, "flush cache"); len(got) != 1 || got[0].group != palActions {
		t.Fatalf("every word must match: %+v", got)
	}
}

// Every page has its title key, for matching in both languages.
func TestPalettePageTitleKeys(t *testing.T) {
	for _, p := range env.Order {
		if _, ok := pageTitleKeys[p]; !ok {
			t.Errorf("page %s has no entry in pageTitleKeys", p)
		}
	}
}
