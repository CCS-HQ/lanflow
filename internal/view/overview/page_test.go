package overview

import (
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

type rec struct {
	toasts, nav []string
}

func testEnv(f *fake.Fake) (*env.Env, *rec) {
	r := &rec{}
	e := &env.Env{API: f, Kit: env.Basic(), Mac: true,
		Navigate: func(p env.Page, a string) { r.nav = append(r.nav, string(p)+":"+a) },
		Arg:      func(env.Page) string { return "" },
		Toast:    func(m string, bad bool) { r.toasts = append(r.toasts, m) },
		Run: func(job func() error, ok string) {
			if err := job(); err != nil {
				r.toasts = append(r.toasts, err.Error())
			} else if ok != "" {
				r.toasts = append(r.toasts, ok)
			}
		},
		Confirm: func(title, body, okLabel string, danger bool, onOK func()) { onOK() },
		Redraw:  func() {},
	}
	return e, r
}

// TestMapDrawsRoutesAndDestinations renders the overview over the fake API
// and checks the map produced clickable arcs and destination countries,
// and that clicking an arc opens the connections page filtered to its route.
func TestMapDrawsRoutesAndDestinations(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 960, 640)
	tt.Frame()
	if len(p.hits) == 0 {
		t.Fatalf("no arcs drawn; texts: %q", tt.Texts())
	}
	groups := map[string]bool{}
	for _, h := range p.hits {
		groups[h.group] = true
	}
	if !groups["节点选择"] {
		t.Errorf("main route not drawn: %v", groups)
	}
	ds := p.dests.lists["节点选择"]
	if len(ds) == 0 {
		t.Fatalf("no destination countries for 节点选择: %+v", p.dests.lists)
	}
	countries := map[string]int{}
	for _, d := range ds {
		countries[d.place.Country] = d.conns
	}
	if countries["US"] == 0 || countries["NL"] == 0 {
		t.Errorf("expected US and NL destinations, got %v", countries)
	}
	you, _ := p.youPlace()
	if got := mapDescription(you, p.routes(time.Time{}), p.dests.lists); got != "出口地图：从你所在的上海到东京、香港；目标国家：美国、荷兰" {
		t.Errorf("map description: %q", got)
	}

	// Click the middle of the main route's arc.
	var main hitArc
	for _, h := range p.hits {
		if h.group == "节点选择" {
			main = h
		}
	}
	mid := main.pts[len(main.pts)/2]
	tt.ClickAt(mid[0], mid[1])
	tt.Frame()
	if len(r.nav) != 1 || r.nav[0] != "conns:节点选择" {
		t.Errorf("clicking the arc should open the connections page filtered to the route, got %v", r.nav)
	}
}

// noTotal is the fake API with the subscriptions' total traffic unknown.
type noTotal struct{ app.API }

func (n noTotal) Subscriptions() []model.Subscription {
	subs := n.API.Subscriptions()
	for i := range subs {
		subs[i].Total = 0
	}
	return subs
}

// TestSubscriptionExpiryWithoutTotal: the expiry shows even when the
// total traffic is unknown (docs/design.md §5.1 当前订阅).
func TestSubscriptionExpiryWithoutTotal(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	e.API = noTotal{f}
	p := New(e)
	tt := ui.NewTester(p.View, 960, 640)
	tt.Frame()
	if !tt.HasText("到期 2027-03-01") {
		t.Errorf("expiry missing without a total: %q", tt.Texts())
	}
	if tt.HasText("已用流量") {
		t.Errorf("progress bar should not show without a total")
	}
}

// TestSideDividerResizes: dragging the gap between the map and the side
// column moves room between them, within bounds, and a double click puts
// the default back.
func TestSideDividerResizes(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1200, 720)
	tt.Frame()
	tt.Frame()
	r, ok := tt.Find("调整出口地图与右侧栏宽度")
	if !ok {
		t.Fatalf("no divider; texts: %q", tt.Texts())
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Frame()
	tt.Move(x-200, y)
	tt.Frame()
	tt.Release(x-200, y)
	tt.Frame()
	if p.sideShare() <= sideDefault {
		t.Fatalf("side share %v after dragging left, want > %v", p.sideShare(), sideDefault)
	}
	p.setSide(1000)
	if p.sideShare() != sideMax {
		t.Fatalf("side share %v, want clamped to %v", p.sideShare(), sideMax)
	}
}

type coreErrorAPI struct{ app.API }

func (c coreErrorAPI) Status() model.Status {
	st := c.API.Status()
	st.Core = model.CoreError
	st.CoreError = "unexpected exit"
	return st
}

func TestTerminalRowVisibleOnResize(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h float32
		side float32
		mac  bool
		err  bool
	}{
		{"default", 980, 780, 0, false, false},
		{"min-680", 800, 680, 0, false, false},
		{"min-640", 800, 640, 0, false, false},
		{"min-600", 800, 600, 0, false, false},
		{"short-540", 800, 540, 0, false, false},
		{"min-600-narrow-side", 800, 600, sideMin, false, false},
		{"min-600-wide-side", 800, 600, sideMax, false, false},
		{"min-600-mac", 800, 600, 0, true, false},
		{"min-680-core-error-banner", 800, 680, 0, false, true},
		{"min-600-core-error-banner", 800, 600, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fake.NewInstant()
			e, r := testEnv(f)
			if tc.err {
				e.API = coreErrorAPI{f}
			}
			e.Mac = tc.mac
			p := New(e)
			if tc.side > 0 {
				p.setSide(tc.side)
			}
			tt := ui.NewTester(p.View, int(tc.w), int(tc.h))
			tt.Frame()
			tt.Frame()
			lbl, okLbl := tt.Find("终端代理")
			btn, okBtn := tt.Find("复制终端代理命令")
			goos := runtime.GOOS
			if tc.mac {
				goos = "darwin"
			}
			cmd := ProxyCommand(f.Status().MixedPort, goos)
			cmdR, okCmd := tt.Find(cmd)
			if !okLbl || !okBtn || !okCmd {
				t.Fatalf("missing element: lbl=%v cmd=%v btn=%v", okLbl, okCmd, okBtn)
			}
			for _, item := range []struct {
				name string
				r    ui.Rect
			}{
				{"label", lbl},
				{"cmd", cmdR},
				{"copy", btn},
			} {
				if item.r.W <= 0 || item.r.H <= 0 {
					t.Errorf("%s clipped to empty rect: %+v", item.name, item.r)
				}
				if item.r.Y < 0 || item.r.Y+item.r.H > tc.h {
					t.Errorf("%s overflows vertically (h=%.0f): %+v", item.name, tc.h, item.r)
				}
				if item.r.X < 0 || item.r.X+item.r.W > tc.w {
					t.Errorf("%s overflows horizontally (w=%.0f): %+v", item.name, tc.w, item.r)
				}
			}
			// Verify the copy button is actually clickable at this size.
			if err := tt.Click("复制终端代理命令"); err != nil {
				t.Fatalf("click copy button: %v", err)
			}
			if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "已复制" {
				t.Errorf("expected copy toast, got %v", r.toasts)
			}
		})
	}
}
