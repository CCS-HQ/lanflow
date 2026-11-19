package settings

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/view/env"
)

// dialogSize returns the content size of a dialog whose body is one long
// text that takes the height left (覆写 YAML, 运行时配置): env.DialogWidth
// of baseW wide, prefH high on a window of the default height and taller
// on taller windows (never past the room ui.Modal allows), or all the
// room when full (env.ExpandButton toggles it). A fixed size left these a
// small box on a large window.
func dialogSize(c *ui.Context, baseW, prefH float32, full bool) (w, h float32) {
	const margin = 80 // ui.Modal's 20 to the window and the panel's padding, each side
	ww, wh := c.Size()
	if ww <= 0 || wh <= 0 { // no window yet
		return baseW, prefH
	}
	roomW, roomH := max(ww-margin, 0), max(wh-margin, 0)
	if full {
		return roomW, roomH
	}
	return env.DialogWidth(c, baseW), min(max(prefH, prefH*wh/780), roomH)
}
