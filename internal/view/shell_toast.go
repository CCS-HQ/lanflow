package view

import (
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
)

// Toast types beyond mygo's "error": what the icon of a toast tells.
const (
	toastSuccess = "success"
	toastInfo    = "info"
)

// toastWidth is the widest a toast gets; the window's width bounds it too.
const toastWidth = 480

// toastView builds the window's toasts with the shell's own look
// (docs/design.md §6.5): cards of the theme's surface, so they read the
// same in the light and the dark appearance, in the bottom left corner of
// the page area. That corner is the one pages keep free: their tools are
// in the header, the map's controls at the bottom right, 「回到底部」 at the
// bottom center, and the switches of 概览, which most toasts answer, at the
// top.
func (s *Shell) toastView(c *ui.Context) {
	t := c.Theme()
	fill, shadow := t.Background, ui.RGBA(0, 0, 0, 0.14)
	if t.Dark {
		// The page is Background: a card of it would only show by its border.
		fill, shadow = t.SurfaceHover, ui.RGBA(0, 0, 0, 0.45)
	}
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.AlignItems(ui.Start).Gap(8).Padding(0, 16, 16, SidebarWidth+1+16)
		for _, tt := range toasts {
			p := ui.ToastBase(c, tt)
			opacity, dy := toastFade(c, tt, p.Left())
			box := p.Root.Row().AlignItems(ui.Center).Gap(10).Padding(9, 10, 9, 12).Radius(10).MaxWidth(toastWidth).
				Background(fill).Border(1, t.Border).TextColor(t.Text).
				Shadow(0, 8, 24, 0, shadow).Opacity(opacity).Translate(0, dy)
			box.Children(func() {
				icon, col := "check", t.Success
				switch tt.Type {
				case "error":
					icon, col = "warn", t.Danger
				case toastInfo:
					icon, col = "info", t.Accent
				}
				ui.Box(c).TextColor(col).Shrink(0).Children(func() { s.env.Kit.Icon(c, icon, 16) })
				ui.Column(c).Shrink(1).Gap(2).Padding(0, 4, 0, 0).Children(func() {
					title := ui.Text(c, tt.Title)
					if tt.Description != "" {
						title.FontWeight(600)
						ui.Text(c, tt.Description).FontSize(12).TextColor(t.TextMuted)
					}
				})
				if tt.Action != "" {
					b := p.ActionButton().Padding(4, 8).Radius(t.Radius).Shrink(0).TextColor(t.Accent).FontWeight(600)
					if b.Hovered() {
						b.Background(t.Accent.Alpha(0.12))
					}
					b.Children(func() { ui.Text(c, tt.Action) })
				}
				x := p.CloseButton().Label(i18n.T("关闭")).Padding(4).Radius(t.Radius).Shrink(0).TextColor(t.TextMuted)
				if x.Hovered() {
					x.Background(t.Text.Alpha(0.08)).TextColor(t.Text)
				}
				x.Children(func() { s.env.Kit.Icon(c, "close", 12) })
			})
		}
	})
}

// toastFade returns the opacity of a toast and how far below its place it
// is drawn: it rises in as it shows and fades at the end of its time.
// left is what ToastParts.Left gives, which stands still while the user
// holds the toasts.
func toastFade(c *ui.Context, tt ui.Toast, left time.Duration) (opacity, dy float32) {
	const fade = 180 * time.Millisecond
	if tt.Timeout <= 0 || left <= 0 {
		return 1, 0
	}
	switch age := tt.Timeout - left; {
	case age < fade:
		f := float32(age) / float32(fade)
		c.AnimationFrame()
		return f, 8 * (1 - f)
	case left < fade:
		c.AnimationFrame()
		return float32(left) / float32(fade), 0
	default:
		c.After(left - fade)
		return 1, 0
	}
}
