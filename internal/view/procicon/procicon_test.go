package procicon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// stub serves icons from a map and counts the asks.
type stub struct {
	*fake.Fake
	data  map[string][]byte
	calls map[string]int
}

func (s *stub) ProcessIcon(path string) []byte {
	s.calls[path]++
	return s.data[path]
}

func samplePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.SetNRGBA(0, 0, color.NRGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSource(t *testing.T) {
	api := &stub{Fake: fake.NewInstant(), data: map[string][]byte{}, calls: map[string]int{}}
	c := New(api)
	if c.Source("") != nil || api.calls[""] != 0 {
		t.Fatal("an empty path asked the API")
	}
	// Unknown yet: asked again every build, since the icon may land.
	if c.Source("/Applications/A.app") != nil || c.Source("/Applications/A.app") != nil {
		t.Fatal("icon out of nothing")
	}
	if api.calls["/Applications/A.app"] != 2 {
		t.Fatalf("asked %d times while unknown, want 2", api.calls["/Applications/A.app"])
	}
	// Landed: decoded once and kept.
	api.data["/Applications/A.app"] = samplePNG(t)
	s := c.Source("/Applications/A.app")
	b, ok := s.(*ui.Bitmap)
	if !ok || b == nil {
		t.Fatalf("decoded icon is %T", s)
	}
	if w, h := b.Size(); w != 8 || h != 8 {
		t.Fatalf("decoded icon is %d×%d", w, h)
	}
	if c.Source("/Applications/A.app") != s || api.calls["/Applications/A.app"] != 3 {
		t.Fatalf("decoded icon not cached (asked %d times)", api.calls["/Applications/A.app"])
	}
	// Bytes that are no image: given up on, not asked again.
	api.data["/Applications/B.app"] = []byte("not a png")
	if c.Source("/Applications/B.app") != nil || c.Source("/Applications/B.app") != nil {
		t.Fatal("icon out of garbage")
	}
	if api.calls["/Applications/B.app"] != 1 {
		t.Fatalf("asked %d times about garbage, want 1", api.calls["/Applications/B.app"])
	}
	var none *Cache
	if none.Source("/Applications/A.app") != nil {
		t.Fatal("a nil cache has icons")
	}
}

// TestDrawAndPaint renders an icon and the generic glyph both as elements
// and through a painter.
func TestDrawAndPaint(t *testing.T) {
	api := &stub{Fake: fake.NewInstant(), data: map[string][]byte{"/Applications/A.app": samplePNG(t)}, calls: map[string]int{}}
	c := New(api)
	painted := 0
	tt := ui.NewTester(func(ctx *ui.Context) {
		th := ctx.Theme()
		ui.Column(ctx).Children(func() {
			c.Draw(ctx, th, "/Applications/A.app", Size)
			c.Draw(ctx, th, "/usr/bin/curl", Size)
			ui.Text(ctx, "after")
			ui.Box(ctx).Size(40, 40).Draw(func(pt *ui.Painter, r ui.Rect) {
				c.Paint(pt, "/Applications/A.app", ui.Rect{X: r.X, Y: r.Y, W: Size, H: Size})
				c.Paint(pt, "/usr/bin/curl", ui.Rect{X: r.X + 20, Y: r.Y, W: Size, H: Size})
				painted++
			})
		})
	}, 200, 100)
	tt.Frame()
	if !tt.HasText("after") || painted == 0 {
		t.Fatalf("not rendered: painted %d, texts %q", painted, tt.Texts())
	}
	if c.Source("/Applications/A.app") == nil {
		t.Fatal("the icon was not decoded while drawing")
	}
}

// hugePNG is a small file holding a w×h picture of one colour.
func hugePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.White})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestDecodeImageLimits: a picture over MaxPixels is refused from its
// header, whatever its file size; a large one is scaled down.
func TestDecodeImageLimits(t *testing.T) {
	for _, c := range []struct {
		name       string
		w, h, edge int
		ok         bool
		ow, oh     int
	}{
		{"small", 8, 8, 48, true, 8, 8},
		{"icon scaled down", 512, 512, 48, true, 48, 48},
		{"wide", 400, 100, 48, true, 48, 12},
		{"tall", 100, 400, 48, true, 12, 48},
		{"no scaling asked", 300, 200, 0, true, 300, 200},
		{"at the limit", 2048, 2048, 64, true, 64, 64},
		{"over the limit", 2049, 2048, 64, false, 0, 0},
		{"thin but long", 9000, 1000, 64, false, 0, 0},
	} {
		data := hugePNG(t, c.w, c.h)
		img, err := DecodeImage(data, c.edge)
		if (err == nil) != c.ok {
			t.Errorf("%s (%d×%d, %d bytes): err %v, want ok=%v", c.name, c.w, c.h, len(data), err, c.ok)
			continue
		}
		if !c.ok {
			if err != ErrTooLarge {
				t.Errorf("%s: err %v, want ErrTooLarge", c.name, err)
			}
			continue
		}
		if b := img.Bounds(); b.Dx() != c.ow || b.Dy() != c.oh {
			t.Errorf("%s: decoded %d×%d, want %d×%d", c.name, b.Dx(), b.Dy(), c.ow, c.oh)
		}
	}
	if _, err := DecodeImage([]byte("not an image"), 16); err == nil {
		t.Error("garbage decoded")
	}
	// The colours survive the scaling, transparent pixels included.
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < 16; i++ {
		src.SetNRGBA(i%4, i/4, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	}
	src.SetNRGBA(0, 0, color.NRGBA{}) // one transparent pixel
	out := shrink(src, 2)
	if got := color.NRGBAModel.Convert(out.At(1, 1)).(color.NRGBA); got != (color.NRGBA{R: 200, G: 100, B: 50, A: 255}) {
		t.Errorf("opaque block became %+v", got)
	}
	if got := color.NRGBAModel.Convert(out.At(0, 0)).(color.NRGBA); got.R < 198 || got.R > 202 || got.A < 189 || got.A > 193 {
		t.Errorf("block with a transparent pixel became %+v, want the colour kept at 3/4 alpha", got)
	}
}

// TestCacheBounded: the cache drops its oldest icons over maxIcons and
// keeps large icons at iconEdge.
func TestCacheBounded(t *testing.T) {
	api := &stub{Fake: fake.NewInstant(), data: map[string][]byte{}, calls: map[string]int{}}
	c := New(api)
	api.data["/big"] = hugePNG(t, 512, 512)
	b, ok := c.Source("/big").(*ui.Bitmap)
	if !ok {
		t.Fatal("the large icon did not decode")
	}
	if w, h := ui.ImageSize(b); w != iconEdge || h != iconEdge {
		t.Fatalf("kept at %v×%v, want %d", w, h, iconEdge)
	}
	small := samplePNG(t)
	for i := 0; i < 2*maxIcons; i++ {
		p := "/app/" + string(rune('a'+i%26)) + "/" + string(rune('0'+i/26%10)) + string(rune('0'+i/260))
		api.data[p] = small
		if c.Source(p) == nil {
			t.Fatalf("icon %d did not decode", i)
		}
		if len(c.src) > maxIcons || len(c.src) != len(c.order) {
			t.Fatalf("after %d icons the cache holds %d (order %d)", i+1, len(c.src), len(c.order))
		}
	}
	if _, ok := c.src["/big"]; ok {
		t.Fatal("the oldest icon was not dropped")
	}
	// A dropped icon decodes again when it shows again.
	if c.Source("/big") == nil {
		t.Fatal("a dropped icon must decode again")
	}
}
