package env

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Pane size keys in model.UIState.Sizes, one per resizable pane.
const (
	SizeConnsDetail   = "conns.detail"   // 连接 detail panel width, DIPs
	SizeCaptureDetail = "capture.detail" // 抓包 detail panel width, DIPs
	SizeOverviewSide  = "overview.side"  // 概览 side column share, percent
	SizeStatsChart    = "stats.chart"    // 统计 chart pane height, DIPs
)

// Detail panel bounds for DetailEdge: the narrowest the panel is dragged
// to, and the room it always leaves the list beside it.
const (
	MinDetailWidth = 240
	MinListWidth   = 320
)

// sidebarWidth is the shell's sidebar, which the page content never gets.
const sidebarWidth = 200

// PaneSize keeps *v, a resizable pane's size, in the remembered UI state
// under key: the first call for key loads the stored size into *v (a
// stored 0 or none leaves *v alone), later calls save *v whenever it
// changed since. Call it once a frame, before *v is used; 0 means the
// pane's default and is saved as "forget the size".
func (e *Env) PaneSize(key string, v *float32) {
	if e.panes == nil {
		e.panes = map[string]float32{}
	}
	last, seen := e.panes[key]
	if !seen {
		if s := e.API.UIState().Sizes[key]; s > 0 {
			*v = s
		}
		e.panes[key] = *v
		return
	}
	if *v == last {
		return
	}
	e.panes[key] = *v
	val := *v
	e.SaveUIState("size."+key, func(u *model.UIState) {
		if val <= 0 {
			delete(u.Sizes, key)
			return
		}
		if u.Sizes == nil {
			u.Sizes = map[string]float32{}
		}
		u.Sizes[key] = val
	})
}

// DetailEdge is the start edge of a detail panel beside a list: dragging
// it widens or narrows the panel, a double click puts it back at def.
// Build it first inside the panel, which carries the border it sits on.
// *w is the panel's width (0 = def); it returns the width to give the
// panel this frame, between MinDetailWidth and what leaves the list
// MinListWidth in the window.
func DetailEdge(c *ui.Context, w *float32, def float32) float32 {
	t := c.Theme()
	cur := *w
	if cur <= 0 {
		cur = def
	}
	edge := ui.Box(c.Key("detail-edge")).Absolute().Left(-3).Top(0).Width(6).FillHeight().
		Cursor(ui.CursorResizeEW).Role(ui.RoleSplitter).Label(i18n.T("调整详情宽度")).
		Tooltip(i18n.T("拖动调整宽度，双击复位"))
	if dx, _, ok := edge.Dragged(); ok && dx != 0 {
		// Start from what is shown, so a drag past a bound does not
		// keep building up beyond it.
		*w = ClampDetail(c, cur) - dx
	}
	if edge.DoubleClicked() {
		*w = 0
	}
	hot := edge.Hovered() || edge.Pressed()
	edge.Draw(func(p *ui.Painter, r ui.Rect) {
		if hot {
			p.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, t.Accent, 0)
		}
	})
	if *w <= 0 {
		return ClampDetail(c, def)
	}
	return ClampDetail(c, *w)
}

// ClampDetail keeps a detail panel width between MinDetailWidth and what
// leaves the list MinListWidth in the window.
func ClampDetail(c *ui.Context, w float32) float32 {
	win, _ := c.Size()
	hi := max(win-sidebarWidth-MinListWidth, MinDetailWidth)
	return min(max(w, MinDetailWidth), hi)
}
