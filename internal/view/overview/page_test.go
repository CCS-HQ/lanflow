package overview

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
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
