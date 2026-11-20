package colwidth

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestFit: widths chosen for two Chinese characters stay; a longer title
// widens its column to what the header needs; untitled and flexible
// columns are left alone.
func TestFit(t *testing.T) {
	in := []ui.TableColumn{
		{Title: "状态", ID: "status", Width: 50, Sortable: true},
		{Title: "方法", ID: "method", Width: 58, Sortable: true},
		{Title: "请求数", ID: "req", Width: 76, Sortable: true},
		{Title: "Status", ID: "status-en", Width: 50, Sortable: true},
		{Title: "Requests", ID: "req-en", Width: 20},
		{Title: "A Rather Long Column Title", ID: "min", MinWidth: 40},
		{Title: "", ID: "action", Width: 30, Fixed: true},
		{Title: "Flexible", ID: "flex"},
	}
	var out []ui.TableColumn
	var need func(title string) float32
	ui.NewTester(func(c *ui.Context) {
		out = Fit(c, append([]ui.TableColumn(nil), in...))
		th := c.Theme()
		need = func(title string) float32 {
			w, _ := c.MeasureText(0, ui.Span{Text: title, Size: th.FontSize, Weight: 600})
			return w + 2*th.Space(2.5)
		}
	}, 400, 300).Frame()
	if len(out) != len(in) {
		t.Fatalf("%d columns out of %d", len(out), len(in))
	}
	for i := range in {
		got, was := out[i], in[i]
		switch was.ID {
		case "status", "method", "req", "action", "flex":
			if got.Width != was.Width || got.MinWidth != was.MinWidth {
				t.Errorf("%s (%q) changed: width %v → %v, min %v → %v", was.ID, was.Title, was.Width, got.Width, was.MinWidth, got.MinWidth)
			}
		case "status-en", "req-en":
			if got.Width <= was.Width || got.Width < need(was.Title) || got.MinWidth != 0 {
				t.Errorf("%s (%q): width %v, the header needs %v", was.ID, was.Title, got.Width, need(was.Title))
			}
		case "min":
			if got.Width != 0 || got.MinWidth < need(was.Title) {
				t.Errorf("min: width %v, min %v, the header needs %v", got.Width, got.MinWidth, need(was.Title))
			}
		}
	}
	if in[3].Width != 50 {
		t.Error("the test's own table was changed")
	}
}

// TestText: the widest text decides, with the padding added.
func TestText(t *testing.T) {
	var short, long float32
	ui.NewTester(func(c *ui.Context) {
		short = Text(c, "monospace", 12, 2, "Info")
		long = Text(c, "monospace", 12, 2, "Info", "Warning", "Error")
	}, 200, 100).Frame()
	if short <= 2 || long <= short {
		t.Fatalf("Info %v, with Warning %v", short, long)
	}
}
