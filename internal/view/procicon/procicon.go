// Package procicon draws application icons beside process names, for the
// 连接 list, the topology's 进程 column and the 统计 page: the icon
// API.ProcessIcon returns for the process's path, decoded once, or a
// generic application glyph while none is known.
package procicon

import (
	_ "image/png" // process icons are PNG

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
)

// Size is an icon's edge in a text row, in DIPs.
const Size = 16

// Glyph is the generic application icon, a monochrome line drawing that
// takes the text colour, for processes without an icon of their own.
var Glyph = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="2.5" width="12" height="11" rx="2.5"/><path d="M2 6.2h12"/><circle cx="4.6" cy="4.35" r=".6" fill="currentColor" stroke="none"/><circle cx="6.8" cy="4.35" r=".6" fill="currentColor" stroke="none"/></svg>`))

// iconEdge is the longer edge an icon is kept at, in pixels: three times
// its size on screen covers every display scale, whatever size the system
// handed out (a 512×512 icon would keep a megabyte to draw 16 DIPs).
const iconEdge = 3 * Size

// maxIcons is how many decoded icons a cache keeps: over it, the ones
// decoded first are dropped (and decoded again if they show again), so a
// long session with many short-lived processes does not grow it forever.
const maxIcons = 256

// Cache keeps the decoded icons of a view, by process path.
type Cache struct {
	api   app.API
	src   map[string]ui.ImageSource
	order []string        // the paths of src, oldest first
	fail  map[string]bool // bytes that did not decode: never asked again
}

// New returns an empty cache reading icons from api.
func New(api app.API) *Cache {
	return &Cache{api: api, src: map[string]ui.ImageSource{}, fail: map[string]bool{}}
}

// Source returns the drawable icon of the process at path, nil while none
// is known (TopicProcIcons redraws when one lands) or for good.
func (c *Cache) Source(path string) ui.ImageSource {
	if c == nil || path == "" || c.fail[path] {
		return nil
	}
	if s, ok := c.src[path]; ok {
		return s
	}
	data := c.api.ProcessIcon(path)
	if data == nil {
		return nil
	}
	img, err := DecodeImage(data, iconEdge)
	if err != nil {
		if len(c.fail) >= 4*maxIcons {
			clear(c.fail)
		}
		c.fail[path] = true
		return nil
	}
	s := ui.NewBitmap(img)
	if len(c.order) >= maxIcons {
		// Drop the oldest quarter at once rather than one each time.
		drop := maxIcons / 4
		for _, p := range c.order[:drop] {
			delete(c.src, p)
		}
		c.order = append(c.order[:0], c.order[drop:]...)
	}
	c.src[path] = s
	c.order = append(c.order, path)
	return s
}

// Draw adds the icon of the process at path as an element of size DIPs:
// its own icon, or Glyph in the muted text colour.
func (c *Cache) Draw(ctx *ui.Context, t *ui.Theme, path string, size float32) {
	if s := c.Source(path); s != nil {
		ui.Image(ctx, s).Size(size, size).Shrink(0).Radius(3)
		return
	}
	ui.Icon(ctx, Glyph).Size(size, size).Shrink(0).TextColor(t.TextMuted)
}

// Paint draws the icon of the process at path into r with a painter: its
// own icon, or Glyph in the painter's text colour.
func (c *Cache) Paint(pt *ui.Painter, path string, r ui.Rect) {
	if s := c.Source(path); s != nil {
		pt.Image(s, r, ui.Contain)
		return
	}
	pt.Image(Glyph, r, ui.Contain)
}
