package proxies

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// 定位当前节点 expands the group and tints its current member for a
// moment; group icons decode from the fake's inline SVG.
func TestLocateAndGroupIcons(t *testing.T) {
	api := fake.NewInstant()
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	p.open["流媒体"], p.openInit["流媒体"] = false, true
	tt.Frame()
	info, ok := tt.AccessByRole(ui.RoleButton, "定位当前节点 流媒体")
	if !ok {
		t.Fatalf("no locate button: %q", tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	if !p.open["流媒体"] || p.locate.group != "流媒体" || p.locate.until.IsZero() {
		t.Fatalf("locate state: open=%v %+v", p.open["流媒体"], p.locate)
	}
	tt.Frame()
	if !p.locate.shown {
		t.Fatal("the current member's row should have been revealed")
	}
	if _, ok := p.icons[api.Proxies().Groups[0].Icon]; !ok {
		t.Fatal("the group's inline SVG icon should decode and be cached")
	}
	if decodeIcon([]byte("not an image")) != nil {
		t.Fatal("garbage must not decode")
	}
}

// iconAPI serves group icons from a map.
type iconAPI struct {
	*fake.Fake
	data map[string][]byte
}

func (a *iconAPI) IconData(ref string) ([]byte, bool) {
	if d, ok := a.data[ref]; ok {
		return d, true
	}
	return a.Fake.IconData(ref)
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.White})); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestRasterIconsBoundedAndOffThread: a raster group icon is decoded by
// the decode hook (a goroutine in the app), kept scaled down, and one
// whose header tells of a huge picture is refused without decoding it.
func TestRasterIconsBoundedAndOffThread(t *testing.T) {
	f := fake.NewInstant()
	api := &iconAPI{Fake: f, data: map[string][]byte{
		"ok.png":   pngOf(t, 512, 512),
		"bomb.png": pngOf(t, 4096, 4096), // a small file over procicon.MaxPixels
	}}
	if n := len(api.data["bomb.png"]); n > 512<<10 {
		t.Fatalf("the bomb is %d bytes: over the service's limit, so no test of the view", n)
	}
	e, _ := testEnv(f)
	e.API = api
	p := New(e)
	var jobs []func()
	p.decode = func(fn func()) { jobs = append(jobs, fn) }
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Children(func() {
			p.groupIcon(c, "ok.png")
			p.groupIcon(c, "bomb.png")
		})
	}, 200, 100)
	tt.Frame()
	if len(jobs) != 2 || len(p.icons) != 0 {
		t.Fatalf("decoding started %d jobs on the first build, %d icons decoded in the view", len(jobs), len(p.icons))
	}
	for _, j := range jobs {
		j()
	}
	tt.Frame()
	tt.Frame()
	if len(jobs) != 2 {
		t.Fatalf("decoded again: %d jobs", len(jobs))
	}
	b, ok := p.icons["ok.png"].(*ui.Bitmap)
	if !ok {
		t.Fatalf("ok.png not decoded: %T", p.icons["ok.png"])
	}
	if w, h := ui.ImageSize(b); w != iconEdge || h != iconEdge {
		t.Fatalf("ok.png kept at %v×%v, want %d", w, h, iconEdge)
	}
	if _, ok := p.icons["bomb.png"]; ok || !p.iconFail["bomb.png"] {
		t.Fatal("the huge picture must be refused")
	}
	if decodeIcon(api.data["bomb.png"]) != nil {
		t.Fatal("decodeIcon decoded the huge picture")
	}
}
