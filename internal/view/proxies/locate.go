package proxies

import (
	"bytes"
	_ "image/jpeg" // group icons may be JPEG
	_ "image/png"  // or PNG
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/view/procicon"
)

// Group icons (docs/design.md §5.2, the proxy-group `icon` field) and
// 「定位当前节点」: scroll to the group's selected member and flash it.

// iconSize is the icon's edge in the group header, in DIPs.
const iconSize = 16

// highlightFor is how long the located node stays tinted.
const highlightFor = 1500 * time.Millisecond

// locateState is the node being revealed: set by the header button,
// consumed by the node row of that group's current member.
type locateState struct {
	group string
	until time.Time // highlight end; zero = nothing to show
	shown bool      // the row scrolled into view once
}

// iconEdge is the longer edge a raster group icon is kept at, in pixels:
// three times its size on screen, whatever size the subscription links.
const iconEdge = 3 * iconSize

// groupIcon draws a group's icon at 16 pt, if the reference decodes to an
// image the UI can draw (SVG, PNG, JPEG); nothing otherwise. Decoded
// images are cached by reference. The bytes come from the subscription:
// a raster one is decoded off the UI thread, refused when its header
// tells of a huge picture and kept scaled down (decodeIcon).
func (p *Page) groupIcon(c *ui.Context, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" || p.iconFail[ref] {
		return
	}
	src, ok := p.icons[ref]
	if !ok {
		src = p.loadIcon(ref)
		if src == nil {
			return // not loaded or decoded yet, or never
		}
	}
	ui.Image(c, src).Size(iconSize, iconSize).Shrink(0).Radius(3)
}

// loadIcon returns the icon of ref once it is decoded, nil until then: it
// takes what a decoding goroutine left, or starts one for bytes the
// service has (TopicProxies redraws when they land).
func (p *Page) loadIcon(ref string) ui.ImageSource {
	p.mu.Lock()
	img, decoded := p.iconDone[ref]
	delete(p.iconDone, ref)
	busy := p.iconBusy[ref]
	p.mu.Unlock()
	if decoded {
		if img == nil {
			p.iconFail[ref] = true
			return nil
		}
		src := ui.NewBitmap(img)
		p.icons[ref] = src
		return src
	}
	if busy {
		return nil
	}
	data, have := p.e.API.IconData(ref)
	if !have {
		return nil
	}
	if isSVG(data) {
		// An SVG is parsed here: it holds no pixels to blow up.
		src := decodeIcon(data)
		if src == nil {
			p.iconFail[ref] = true
			return nil
		}
		p.icons[ref] = src
		return src
	}
	p.mu.Lock()
	p.iconBusy[ref] = true
	p.mu.Unlock()
	redraw := p.e.Redraw
	p.decode(func() {
		img, _ := procicon.DecodeImage(data, iconEdge)
		p.mu.Lock()
		delete(p.iconBusy, ref)
		p.iconDone[ref] = img // nil: no picture, or too large a one
		p.mu.Unlock()
		if redraw != nil {
			redraw()
		}
	})
	return nil
}

// isSVG reports whether image bytes are an SVG document.
func isSVG(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return bytes.HasPrefix(trimmed, []byte("<svg")) || bytes.HasPrefix(trimmed, []byte("<?xml"))
}

// decodeIcon turns image bytes into something ui.Image draws: an SVG, or
// a bitmap for the raster formats the standard library decodes, at most
// iconEdge pixels a side; nil for what does not decode or is too large
// to (procicon.MaxPixels).
func decodeIcon(data []byte) ui.ImageSource {
	if isSVG(data) {
		if s, err := ui.ParseSVG(bytes.TrimSpace(data)); err == nil {
			return s
		}
		return nil
	}
	img, err := procicon.DecodeImage(data, iconEdge)
	if err != nil {
		return nil
	}
	return ui.NewBitmap(img)
}

// locateNow asks the page to reveal group's current member: the group is
// expanded, its row scrolls into view and stays tinted for a moment.
func (p *Page) locateNow(c *ui.Context, group string) {
	p.open[group] = true
	p.openInit[group] = true
	p.locate = locateState{group: group, until: c.Now().Add(highlightFor)}
	p.persist()
}

// locating reports whether the member row of group's current node should
// be revealed and tinted now; it schedules the frame that ends the tint.
func (p *Page) locating(c *ui.Context, group string, cur bool) (reveal, tint bool) {
	l := &p.locate
	if !cur || l.group != group || l.until.IsZero() {
		return false, false
	}
	if !c.Now().Before(l.until) {
		*l = locateState{}
		return false, false
	}
	c.After(l.until.Sub(c.Now()))
	reveal = !l.shown
	l.shown = true
	return reveal, true
}
