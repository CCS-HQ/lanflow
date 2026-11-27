package view

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"lanflow/internal/i18n"
	"lanflow/internal/view/env"
)

// Toast types beyond mygo's "error": what the icon of a toast tells.
const (
	toastSuccess = "success"
	toastInfo    = "info"
)

// toastWidth is the widest a desktop toast gets; the page bounds it too.
const toastWidth = 480

// noteLook is what a toast's look needs beyond ui.Toast (its Data): the
// level of the notification and how many times it came while showing.
type noteLook struct {
	level env.Level
	count int
}

// toastTitle is the toast's line, with ×N once the same notification came
// again while it showed (env.Note.Key).
func toastTitle(tt ui.Toast) string {
	if l, ok := tt.Data.(noteLook); ok && l.count > 1 {
		return tt.Title + " ×" + strconv.Itoa(l.count)
	}
	return tt.Title
}

// toastView builds the window's toasts (docs/notifications.md): the same
// levels everywhere, placed as each system places them. On the desktop
// they rise in the bottom left corner of the page area; on iOS they come
// down under the status bar; on Android they are snackbars over the tab
// bar.
func (s *Shell) toastView(c *ui.Context) {
	switch {
	case !s.opts.Phone:
		s.desktopToasts(c)
	case mobile.Current() == mobile.Material:
		s.snackbars(c)
	default:
		s.phoneToasts(c)
	}
}

// toastIcon is the icon and its color for a toast's type.
func toastIcon(t *ui.Theme, typ string) (string, ui.Color) {
	switch typ {
	case "error":
		return "warn", t.Danger
	case toastInfo:
		return "info", t.Accent
	}
	return "check", t.Success
}

// newestFirst returns toasts with the newest first: toasts that come down
// from the top push the older ones below them.
func newestFirst(toasts []ui.Toast) []ui.Toast {
	out := make([]ui.Toast, len(toasts))
	for i, tt := range toasts {
		out[len(toasts)-1-i] = tt
	}
	return out
}

func (s *Shell) desktopToasts(c *ui.Context) {
	t := c.Theme()
	fill, shadow := t.Background, ui.RGBA(0, 0, 0, 0.14)
	if t.Dark {
		// The page is Background: a card of it would only show by its border.
		fill, shadow = t.SurfaceHover, ui.RGBA(0, 0, 0, 0.45)
	}
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		// In the bottom left corner of the page area, the one place pages
		// keep free: the top of every page is its header (title, 代理's
		// search field, tools) and then the controls toasts mostly answer
		// (概览's switches); the map's controls are at the bottom center,
		// 「复制」 of 终端代理 and the tables' last columns at the bottom
		// right, 「回到底部」 at the bottom center of 日志.
		viewport.Justify(ui.End).AlignItems(ui.Start).Gap(8).Padding(0, 16, 16, SidebarWidth+1+16)
		for _, tt := range toasts {
			p := ui.ToastBase(c, tt)
			opacity, dy := toastFade(c, tt, p.Left())
			box := p.Root.Row().AlignItems(ui.Center).Gap(10).Padding(8, 8, 8, 12).Radius(10).MaxWidth(toastWidth).
				Background(fill).Border(1, t.Border).TextColor(t.Text).
				Shadow(0, 8, 24, 0, shadow).Opacity(opacity).Translate(0, dy)
			box.Children(func() {
				icon, col := toastIcon(t, tt.Type)
				ui.Box(c).TextColor(col).Shrink(0).Children(func() { s.env.Kit.Icon(c, icon, 16) })
				s.toastText(c, tt, t.TextMuted)
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

// toastText is a toast's line and, when it has one, the smaller line
// under it.
func (s *Shell) toastText(c *ui.Context, tt ui.Toast, muted ui.Color) {
	ui.Column(c).Shrink(1).Grow(1).Gap(2).Children(func() {
		title := ui.Text(c, toastTitle(tt))
		if tt.Description != "" {
			title.FontWeight(600)
			ui.Text(c, tt.Description).FontSize(12).TextColor(muted)
		}
	})
}

// phoneToasts are iOS's: cards across the screen under the status bar,
// coming down from it, which a swipe up puts away (no close button: there
// is no pointer to aim it).
func (s *Shell) phoneToasts(c *ui.Context) {
	t := c.Theme()
	top, _, _, _ := c.SafeArea()
	fill, shadow := t.Background, ui.RGBA(0, 0, 0, 0.16)
	if t.Dark {
		fill, shadow = t.SurfaceHover, ui.RGBA(0, 0, 0, 0.5)
	}
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Justify(ui.Start).AlignItems(ui.Stretch).Gap(8).Padding(top+8, 12, 0, 12)
		for _, tt := range newestFirst(toasts) {
			p := ui.ToastBase(c, tt)
			opacity, dy := toastFade(c, tt, p.Left())
			off := s.swipeToast(c, p.Root, tt.ID, false)
			box := p.Root.Row().AlignItems(ui.Center).Gap(10).Padding(12, 12, 12, 14).Radius(16).MinHeight(env.TouchSize).
				Background(fill).TextColor(t.Text).Shadow(0, 8, 24, 0, shadow).
				Opacity(opacity).Translate(0, min(0, off)-3*dy)
			box.Children(func() {
				icon, col := toastIcon(t, tt.Type)
				ui.Box(c).TextColor(col).Shrink(0).Children(func() { s.env.Kit.Icon(c, icon, 18) })
				s.toastText(c, tt, t.TextMuted)
				if tt.Action != "" {
					p.ActionButton().Height(env.TouchSize).PaddingX(8).Shrink(0).TextColor(t.Accent).FontWeight(600).
						Children(func() { ui.Text(c, tt.Action) })
				}
			})
		}
	})
}

// snackbars are Android's: dark bars over the tab bar (light in the dark
// appearance), with the action in the bar, which a swipe to the side puts
// away.
func (s *Shell) snackbars(c *ui.Context) {
	t := c.Theme()
	_, _, bottom, _ := c.SafeArea()
	// Over the tab bar (80 pt in Material), or over the keyboard, which
	// hides the bar.
	if bottom < 100 {
		bottom += 80
	}
	fill, text, action := t.Text.Mix(t.Background, 0.1), t.Background, t.Accent.Mix(t.Background, 0.45)
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Justify(ui.End).AlignItems(ui.Stretch).Gap(8).Padding(0, 12, bottom+12, 12)
		for _, tt := range toasts {
			p := ui.ToastBase(c, tt)
			opacity, dy := toastFade(c, tt, p.Left())
			off := s.swipeToast(c, p.Root, tt.ID, true)
			box := p.Root.Row().AlignItems(ui.Center).Gap(8).Padding(6, 8, 6, 16).Radius(4).MinHeight(48).
				Background(fill).TextColor(text).Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, 0.25)).
				Opacity(opacity).Translate(off, dy)
			box.Children(func() {
				if tt.Type == "error" {
					ui.Box(c).TextColor(t.Danger.Mix(t.Background, 0.35)).Shrink(0).Children(func() { s.env.Kit.Icon(c, "warn", 16) })
				}
				s.toastText(c, tt, text.Alpha(0.75))
				if tt.Action != "" {
					p.ActionButton().Height(env.TouchSize).PaddingX(8).Shrink(0).TextColor(action).FontWeight(600).
						Children(func() { ui.Text(c, tt.Action) })
				}
			})
		}
	})
}

// swipeDismiss is how far a toast is swiped before letting go puts it away.
const swipeDismiss = 40

// swipeToast follows a finger dragging the toast root along one axis (side
// to side, or up only) and returns how far it is moved. Let go past
// swipeDismiss it closes the toast; short of it the toast goes back.
func (s *Shell) swipeToast(c *ui.Context, root ui.Element, id string, sideways bool) float32 {
	if s.toastSwipe == nil {
		s.toastSwipe = map[string]float32{}
	}
	off := s.toastSwipe[id]
	if dx, dy, ok := root.Dragged(); ok {
		if sideways {
			off += dx
		} else {
			off = min(0, off+dy)
		}
		s.toastSwipe[id] = off
		return off
	}
	if off != 0 && !root.Pressed() {
		delete(s.toastSwipe, id)
		if off >= swipeDismiss || off <= -swipeDismiss {
			c.CloseToast(id)
		}
		return 0
	}
	return off
}

// toastFade returns the opacity of a toast and how far from its place it
// is drawn: it slides in as it shows and fades at the end of its time.
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
