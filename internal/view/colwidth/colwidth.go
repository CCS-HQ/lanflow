// Package colwidth widens table columns whose widths were chosen for
// their Chinese titles, so a longer title in another language (状态 →
// "Status", 请求数 → "Requests") is not cut in its header.
package colwidth

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// Fit returns cols, already translated (env.I18nColumns), with each
// fixed-width or minimum-width column at least as wide as its title
// needs in the header: the title in the header's weight and the header's
// padding (ui.Table: Space(2.5) on each side). The sort arrow is not
// counted, as the Chinese widths never counted it: a column wide enough
// keeps its width, so the Chinese layout does not change. cols is changed
// in place: pass a copy, as env.I18nColumns returns.
func Fit(c *ui.Context, cols []ui.TableColumn) []ui.TableColumn {
	t := c.Theme()
	for i := range cols {
		col := &cols[i]
		if col.Title == "" || (col.Width == 0 && col.MinWidth == 0) {
			continue
		}
		w, _ := c.MeasureText(0, ui.Span{Text: col.Title, Size: t.FontSize, Weight: 600})
		need := w + 2*t.Space(2.5) + 1
		need = float32(math.Ceil(float64(need)))
		switch {
		case col.Width > 0 && col.Width < need:
			col.Width = need
		case col.Width == 0 && col.MinWidth < need:
			col.MinWidth = need
		}
	}
	return cols
}

// Text returns the width in DIPs that the widest of texts takes in the
// font family at size, rounded up, with pad added: for a column of
// labels that must line up without a table.
func Text(c *ui.Context, family string, size, pad float32, texts ...string) float32 {
	var widest float32
	for _, s := range texts {
		if w, _ := c.MeasureText(0, ui.Span{Text: s, Font: family, Size: size}); w > widest {
			widest = w
		}
	}
	return float32(math.Ceil(float64(widest))) + pad
}
