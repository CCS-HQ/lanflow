package env

// What pages share on a phone (docs/phone-ui.md): the top bar's actions,
// icon buttons, cards without borders and the large titles of iOS.

import (
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// PhoneBar is a page that puts actions in the phone's top bar, at its
// trailing end: icon buttons (IconButton), a ⋯ menu. The shell calls
// PhoneActions while it draws the bar, before the page's View.
type PhoneBar interface {
	PhoneActions(c *ui.Context)
}

// PhoneLargeTitle is a page whose content starts with LargeTitle on iOS:
// the shell's top bar leaves its title out until the page scrolls the
// large one away (the page's scroll container tracks Env.PhoneScroll).
type PhoneLargeTitle interface {
	PhoneLargeTitle() bool
}

// Material reports whether the phone's look is Material (Android): top
// bars with the title at the start and no large titles. Cupertino (iOS)
// otherwise.
func Material() bool { return mobile.Current() == mobile.Material }

// PhoneGap is the content's side margin on a phone, and the gap between
// its blocks.
const PhoneGap = 16

// PhoneRadius is the corner of a phone's cards.
const PhoneRadius = 12

// IconButton is a phone's icon button: TouchSize square, no border, the
// icon in the accent color, label for screen readers.
func IconButton(c *ui.Context, k Kit, icon, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key("iconbtn:"+label)).Size(TouchSize, TouchSize).Radius(TouchSize / 2).Center().
		Label(label).TextColor(t.Accent).Cursor(ui.CursorPointer)
	if b.Pressed() {
		b.Background(t.Accent.Alpha(0.12))
	}
	b.Children(func() { k.Icon(c, icon, 20) })
	return b
}

// PhoneCard is a phone's card: the surface color on the background, no
// border, PhoneRadius corners and PhoneGap inside.
func PhoneCard(c *ui.Context) ui.Element {
	t := c.Theme()
	return ui.Column(c).Radius(PhoneRadius).Background(t.Surface).Padding(PhoneGap).Gap(8)
}

// LargeTitle draws iOS's large title at the top of a page's scroll
// content (34 pt bold); nothing on Material, whose top bar shows it.
func LargeTitle(c *ui.Context, title string) {
	if Material() {
		return
	}
	ui.Text(c, title).FontSize(32).Bold().Padding(4, 0, 6, 0).Role(ui.RoleHeading).Level(1).SingleLine()
}

// LargeTitleHeight is how far a page scrolls before the top bar shows the
// small title in place of the large one.
const LargeTitleHeight = 44

// Scroll returns the page's scroll container on a phone, tracking the
// state the shell reads for the large title (Env.PhoneScroll); a plain
// Scroll elsewhere.
func (e *Env) Scroll(c *ui.Context, p Page) ui.Element {
	sc := ui.Scroll(c.Key("phone-scroll:" + string(p)))
	if e.PhoneScroll != nil {
		sc.TrackScroll(e.PhoneScroll(p))
	}
	return sc
}

// Segmented is a phone's segmented control: the screen's width, equal
// segments TouchSize high less the frame's, the chosen one raised on
// iOS and tinted on Material. Changed reports a new choice in *i.
func Segmented(c *ui.Context, key string, i *int, labels ...string) ui.Element {
	t := c.Theme()
	material := Material()
	box := ui.Row(c.Key("seg:" + key)).FillWidth().Height(38).Padding(3).Gap(3).Radius(10).Background(t.TextMuted.Alpha(0.12)).Role(ui.RoleTabList)
	box.Children(func() {
		for n, l := range labels {
			on := n == *i
			seg := ui.ButtonBase(c.Key(n)).Grow(1).Basis(0).Radius(8).Center().Role(ui.RoleTab).Label(l).Selected(on).Cursor(ui.CursorPointer)
			col := t.TextMuted
			if on {
				col = t.Text
				if material {
					seg.Background(t.Accent.Alpha(0.18))
					col = t.Accent
				} else {
					seg.Background(t.Background).Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.12))
				}
			}
			seg.Children(func() {
				txt := ui.Text(c, l).FontSize(14).TextColor(col).SingleLine()
				if on {
					txt.FontWeight(600)
				}
			})
			if seg.Clicked() && !on {
				*i = n
				box.SetChanged()
			}
		}
	})
	return box
}

// PhoneStack is a page that pushes pages of its own on a phone (a setting's
// editor, a list of choices, a connection's detail): the shell shows its
// title and a back button to parent, and back pops it first.
type PhoneStack interface {
	// PhoneSub reports the pushed page's title and the name of the page
	// under it, ok false when none is pushed.
	PhoneSub() (title, parent string, ok bool)
	// PhonePop closes the pushed page.
	PhonePop()
}

// NavRow is a phone's list row that goes somewhere: the label (and a
// smaller second line), the current value muted and a chevron at the end,
// TouchSize high at least. Clicked reports a tap.
func NavRow(c *ui.Context, k Kit, label, desc, value string) ui.Element {
	t := c.Theme()
	row := ui.ButtonBase(c.Key("nav:"+label)).Row().MinHeight(TouchSize).Padding(8, 14).Gap(10).AlignItems(ui.Center).FillWidth().
		Label(label).Cursor(ui.CursorPointer)
	if row.Pressed() {
		row.Background(t.SurfaceHover)
	}
	if Reveal(row, label) { // a jump from a search
		row.Background(t.Accent.Alpha(0.12))
	}
	row.Children(func() {
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
			ui.Text(c, label).FontSize(15)
			if desc != "" {
				ui.Text(c, desc).FontSize(12).TextColor(t.TextMuted)
			}
		})
		if value != "" {
			ui.Text(c, value).FontSize(15).TextColor(t.TextMuted).SingleLine().Shrink(1).MaxWidth(170).FontFeatures("tnum")
		}
		ui.Box(c).TextColor(t.TextMuted.Alpha(0.7)).Children(func() { k.Icon(c, "chevron", 13) })
	})
	return row
}

// ActionRow is a phone's list row that does something: its label in the
// accent color (red when danger), TouchSize high, the whole row tappable.
func ActionRow(c *ui.Context, label string, danger, disabled bool) ui.Element {
	t := c.Theme()
	col := t.Accent
	if danger {
		col = t.Danger
	}
	if disabled {
		col = t.TextMuted
	}
	row := ui.ButtonBase(c.Key("act:"+label)).Row().MinHeight(TouchSize).Padding(8, 14).AlignItems(ui.Center).FillWidth().
		Label(label).Disabled(disabled).Cursor(ui.CursorPointer)
	if row.Pressed() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() { ui.Text(c, label).FontSize(15).TextColor(col) })
	return row
}

// SearchBar is a phone's search field: the system's look, a tinted
// rounded bar with no border, 36 pt high, the width it is given.
func SearchBar(c *ui.Context, key string, text *string, placeholder, label string) ui.Element {
	t := c.Theme()
	return ui.SearchField(c.Key(key), text).Placeholder(placeholder).Label(label).
		Height(36).Radius(10).Border(0, ui.Transparent).Background(t.TextMuted.Alpha(0.12)).Grow(1).Shrink(1).MinWidth(0)
}
