package env

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
)

// Dialogs that hold a lot (an error's full text, a report, a config, a
// list of backups) share these helpers so that they grow with the window,
// never run off it, and can be enlarged to nearly the whole window.
//
// ui.Modal itself keeps its panel 20 DIPs inside the window
// (MaxWidth/MaxHeight); content taller than that is cut off unless a part
// of it shrinks and scrolls, which DialogBody and LongText do.

// dialogMargin is the room ui.Modal keeps between its panel and the
// window's edges, on each side.
const dialogMargin = 20

// DialogWidth returns the width of a dialog whose width at the minimum
// window size is base: it grows in proportion with the window, up to
// 1.75 × base, and never goes past the room ui.Modal allows. Content-heavy
// dialogs use it in place of a fixed Width(base).
//
//	ui.Column(c).Width(env.DialogWidth(c, 560)).Gap(12).Children(...)
func DialogWidth(c *ui.Context, base float32) float32 {
	w, _ := c.Size()
	room := w - 2*dialogMargin - 40 // the panel's own padding
	if w <= 0 {
		return base
	}
	v := base * w / 1000
	v = max(v, base)
	v = min(v, base*1.75)
	return max(min(v, room), min(base, room))
}

// DialogMaxHeight returns the most a part of a dialog may take of the
// window's height when reserve DIPs go to the rest of it (title, text,
// buttons): what is left of the window inside ui.Modal's margins, never
// below 120. Use it in place of a fixed MaxHeight on a dialog's Scroll so
// that it shows more on a tall window.
func DialogMaxHeight(c *ui.Context, reserve float32) float32 {
	_, h := c.Size()
	return max(h-2*dialogMargin-40-reserve, 120)
}

// DialogOptions configure Dialog.
type DialogOptions struct {
	// Title is the bold heading; Icon, when set, draws before it (a
	// warning icon in its color, say).
	Title string
	Icon  func()
	// Width is the dialog's width at the minimum window size (460 when
	// 0); it grows with the window as DialogWidth says.
	Width float32
	// Expanded, when not nil, shows a 放大/还原 button at the end of the
	// title row; while *Expanded is true the dialog fills the window but
	// for a margin, and the body takes all the height.
	Expanded *bool
}

// Dialog shows a modal dialog with a title row, a body and a footer of
// buttons, sized for a lot of content: it is DialogWidth(o.Width) wide,
// as high as its content up to the window's height, and with o.Expanded
// it can be enlarged to fill the window.
//
// body is laid out in a column that takes the height the window leaves
// and shrinks when the content is taller: put the long part in a LongText
// or a ui.Scroll with Grow(1) (and MinHeight(0)) so that it scrolls rather
// than pushing the buttons off the window. footer draws the buttons, laid
// out in a row ending at the right (a ui.Spacer splits off a left group);
// it may be nil. It returns the panel, a zero Element while closed; Dismissed on it
// reports Escape or a click outside, as ui.Modal's does.
//
//	env.Dialog(c, &d.open, env.DialogOptions{Title: "运行配置", Width: 560, Expanded: &d.big},
//		func() { env.LongText(c, d.text, true).Grow(1) },
//		func() { if ui.PrimaryButton(c, "好").Clicked() { d.open = false } })
func Dialog(c *ui.Context, open *bool, o DialogOptions, body, footer func()) ui.Element {
	if !*open {
		return ui.Element{}
	}
	base := o.Width
	if base <= 0 {
		base = 460
	}
	big := o.Expanded != nil && *o.Expanded
	w, h := c.Size()
	panel := ui.Modal(c, open, func() {
		col := ui.Column(c).Gap(12).MinHeight(0).Shrink(1)
		if big {
			// The panel's padding is 20 on each side; it is as big as
			// ui.Modal lets it be.
			col.Width(max(w-2*dialogMargin-40, 0)).Height(max(h-2*dialogMargin-40, 0))
		} else {
			col.Width(DialogWidth(c, base))
		}
		col.Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
				if o.Icon != nil {
					ui.Box(c).Shrink(0).Children(o.Icon)
				}
				ui.Text(c, o.Title).FontSize(15).FontWeight(600).Grow(1).Shrink(1)
				if o.Expanded != nil {
					ExpandButton(c, o.Expanded)
				}
			})
			b := ui.Column(c).Gap(12).MinHeight(0).Shrink(1)
			if big {
				b.Grow(1)
			}
			b.Children(body)
			if footer != nil {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Justify(ui.End).Shrink(0).Children(footer)
			}
		})
	})
	return panel
}

var (
	expandIcon  = dialogSVG(`<path d="M9.5 2.5h4v4M13.5 2.5L9 7M6.5 13.5h-4v-4M2.5 13.5L7 9"/>`)
	restoreIcon = dialogSVG(`<path d="M13.5 6.5h-4v-4M9.5 6.5l4-4M2.5 9.5h4v4M6.5 9.5l-4 4"/>`)
)

func dialogSVG(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

// ExpandButton draws a small icon button that toggles *expanded: 放大 to
// fill the window, 还原 back to the dialog's size. It reports a toggle.
// Dialog puts one in its title row; a page's own dialog or panel can use
// it too.
func ExpandButton(c *ui.Context, expanded *bool) bool {
	t := c.Theme()
	label, icon := i18n.T("放大窗口"), expandIcon
	if *expanded {
		label, icon = i18n.T("还原大小"), restoreIcon
	}
	b := ClickableRow(c, label).Padding(4).Radius(6).Shrink(0).TextColor(t.TextMuted).Tooltip(label)
	if b.Hovered() {
		b.Background(t.Surface).TextColor(t.Text)
	}
	b.Children(func() { ui.Icon(c, icon).Size(14, 14).Shrink(0) })
	if b.Clicked() {
		*expanded = !*expanded
		return true
	}
	return false
}

// LongText draws text the user may need to read in full and copy (an
// error with its details, a log, a report, a config) in a bordered box:
// selectable, wrapped, and scrolling once it is taller than the room its
// parent leaves, as the box shrinks to fit (MinHeight 0). Chain
// MaxHeight(DialogMaxHeight(...)) to cap it, or Grow(1) to fill an
// expanded Dialog. mono sets it in the monospaced font, smaller.
func LongText(c *ui.Context, text string, mono bool) ui.Element {
	t := c.Theme()
	box := ui.Scroll(c).MinHeight(0).Shrink(1).Radius(6).Background(t.Surface).Border(1, t.Border).Selectable()
	box.Children(func() {
		tx := ui.Text(c, text).Padding(8, 10)
		if mono {
			tx.Font("monospace").FontSize(12)
		} else {
			tx.FontSize(13)
		}
	})
	return box
}
