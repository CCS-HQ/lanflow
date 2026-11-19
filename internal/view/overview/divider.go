// The draggable gutter between the exit map and the side column.

package overview

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
)

const (
	// sideDefault is the side column's share of the room, in percent
	// (docs/design.md §5.1: the map takes about two thirds).
	sideDefault = 36
	// sideMin and sideMax bound a drag of the divider.
	sideMin, sideMax = 20, 60
	// gutterWidth is the gap between the map and the side column; the
	// whole gap is the divider's handle.
	gutterWidth = 16
	// sideMinWidth and mapMinWidth keep both from collapsing in a small
	// window whatever the share.
	sideMinWidth = 240
	mapMinWidth  = 320
	// sideStep is how far one arrow key moves the divider, in percent.
	sideStep = 2
)

// sideShare is the side column's share in percent, the default until the
// divider is moved.
func (p *Page) sideShare() float32 {
	if p.side == 0 {
		return sideDefault
	}
	return p.side
}

// setSide clamps and stores the side column's share.
func (p *Page) setSide(v float32) {
	p.side = min(max(v, sideMin), sideMax)
}

// sideDivider is the gap between the map and the side column: dragging it
// moves the room between them, the arrow keys do once it has the focus, and
// a double click puts it back. total is the row's width in the last frame.
func (p *Page) sideDivider(c *ui.Context, total float32) {
	t := c.Theme()
	d := ui.Box(c.Key("side-divider")).Width(gutterWidth).Shrink(0).Focusable().Role(ui.RoleSplitter).
		Label(i18n.T("调整出口地图与右侧栏宽度")).Tooltip(i18n.T("拖动调整宽度，双击复位")).Cursor(ui.CursorResizeEW)
	dx, _, held := d.Dragged()
	if held && dx != 0 && total > gutterWidth {
		// Dragging right narrows the side column.
		p.setSide(p.sideShare() - dx*100/(total-gutterWidth))
	}
	if d.DoubleClicked() {
		p.side = 0
	}
	if d.Shortcut(0, ui.KeyLeft) {
		p.setSide(p.sideShare() + sideStep)
	}
	if d.Shortcut(0, ui.KeyRight) {
		p.setSide(p.sideShare() - sideStep)
	}
	active := held || d.Hovered() || d.FocusVisible()
	d.Draw(func(pt *ui.Painter, r ui.Rect) {
		if !active {
			return
		}
		// A short grip in the middle of the gap, as the pointer finds it.
		h := min(r.H, 48)
		pt.Fill(ui.Rect{X: r.X + r.W/2 - 1.5, Y: r.Y + (r.H-h)/2, W: 3, H: h}, t.Accent.Alpha(0.6), 1.5)
	})
}
