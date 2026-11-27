package view

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"

	"lanflow/internal/i18n"
	"lanflow/internal/view/env"
)

func TestNoteLevels(t *testing.T) {
	for l, want := range map[env.Level]time.Duration{env.Done: 2 * time.Second, env.News: 4 * time.Second, env.Attention: 6 * time.Second} {
		if got := l.Timeout(); got != want {
			t.Errorf("level %d: Timeout = %v, want %v", l, got, want)
		}
	}
	a, b := env.Note{Title: "已复制"}, env.Note{Level: env.Attention, Title: "已复制"}
	if a.MergeKey() == b.MergeKey() {
		t.Error("notes of different levels must not merge")
	}
	if a.MergeKey() != (env.Note{Title: "已复制", Detail: "x"}).MergeKey() {
		t.Error("notes of one level and title must merge")
	}
	if got := (env.Note{Title: "a", Key: "k"}).MergeKey(); got != "k" {
		t.Errorf("MergeKey with Key = %q, want k", got)
	}
}

// showNote queues n and lets its toast fade in.
func showNote(s *Shell, tt *ui.Tester, n env.Note) {
	s.toast(n)
	settleToast(tt)
}

func TestToastsMergeAndCount(t *testing.T) {
	_, s, tt := newTest(t)
	tt.Frame()
	copied := i18n.T("已复制")
	showNote(s, tt, env.Note{Title: copied})
	showNote(s, tt, env.Note{Title: copied})
	if !tt.HasText(copied + " ×2") {
		t.Fatalf("the same note twice should read ×2; texts: %q", tt.Texts())
	}
	n := 0
	for _, x := range tt.Texts() {
		if x == copied || x == copied+" ×2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the same note twice should be one toast, found %d", n)
	}
	// Once it has gone, the count starts again.
	tt.Advance(env.Done.Timeout() + time.Second)
	tt.Frame()
	showNote(s, tt, env.Note{Title: copied})
	if tt.HasText(copied+" ×2") || !tt.HasText(copied) {
		t.Fatalf("a note after the toast closed should count from 1; texts: %q", tt.Texts())
	}
}

func TestDesktopToastPlacement(t *testing.T) {
	for _, page := range []env.Page{env.Overview, env.Proxies} {
		_, s, tt := newTest(t)
		s.navigate(page, "")
		tt.Frame()
		msg := i18n.Tf("已切换到 %s", "东京 03")
		showNote(s, tt, env.Note{Title: msg})
		r, ok := tt.Find(msg)
		if !ok {
			t.Fatalf("%s: toast not found; texts: %q", page, tt.Texts())
		}
		if r.Y < DefaultHeight*2/3 {
			t.Errorf("%s: toast at y=%v: desktop toasts sit at the bottom, clear of the header and the controls under it", page, r.Y)
		}
		if r.X < SidebarWidth || r.X > SidebarWidth+120 {
			t.Errorf("%s: toast at x=%v: desktop toasts sit in the page's bottom left corner, clear of the sidebar and the map's controls", page, r.X)
		}
		if page == env.Proxies {
			// The header's search field stays reachable while it shows.
			click(t, tt, i18n.T("搜索节点"))
		}
	}
}

// phoneToast shows a note on a phone shell of the given look and returns
// where its line is, with the screen's size.
func phoneToast(t *testing.T, look mobile.Look, n env.Note) (*Shell, *ui.Tester, ui.Rect, float32, float32) {
	t.Helper()
	mobile.SetLook(look)
	t.Cleanup(func() { mobile.SetLook(mobile.Automatic) })
	_, s, tt := newPhoneTest(t)
	w, h := float32(390), float32(844)
	if look == mobile.Material {
		w, h = 360, 780
		tt.SetSize(int(w), int(h))
		tt.SetInsets(24, 0, 0, 0)
	}
	tt.Frame()
	showNote(s, tt, n)
	r, ok := tt.Find(n.Title)
	if !ok {
		t.Fatalf("toast not found; texts: %q", tt.Texts())
	}
	return s, tt, r, w, h
}

func TestPhoneToastPlacement(t *testing.T) {
	long := i18n.T("订阅更新失败：连接超时。仍在使用上次的配置。")
	t.Run("ios", func(t *testing.T) {
		_, _, r, w, _ := phoneToast(t, mobile.Cupertino, env.Note{Level: env.Attention, Title: long})
		if r.Y < 47 {
			t.Errorf("toast at y=%v is under the status bar", r.Y)
		}
		if r.Y > 200 {
			t.Errorf("toast at y=%v: iOS toasts come down from the top", r.Y)
		}
		if r.X < 12 || r.X+r.W > w-12 {
			t.Errorf("toast line %v leaves the screen (width %v)", r, w)
		}
		if r.W < w/2 {
			t.Errorf("toast line %v is squeezed: it should span the screen", r)
		}
	})
	t.Run("android", func(t *testing.T) {
		_, _, r, w, h := phoneToast(t, mobile.Material, env.Note{Level: env.Attention, Title: long})
		if r.Y+r.H > h-80 {
			t.Errorf("snackbar line %v covers the tab bar (screen height %v)", r, h)
		}
		if r.Y < h/2 {
			t.Errorf("snackbar at y=%v: Android snackbars sit at the bottom", r.Y)
		}
		if r.X < 12 || r.X+r.W > w-12 || r.W < w/2 {
			t.Errorf("snackbar line %v should span the screen (width %v)", r, w)
		}
	})
}

func TestPhoneToastSwipeAway(t *testing.T) {
	msg := i18n.T("已复制")
	t.Run("ios swipes up", func(t *testing.T) {
		_, tt, r, _, _ := phoneToast(t, mobile.Cupertino, env.Note{Title: msg})
		x, y := r.X+r.W/2, r.Y+r.H/2
		tt.FingerDown(x, y)
		for i := 1; i <= 6; i++ {
			tt.FingerMove(x, y-float32(i)*12)
			tt.Frame()
		}
		tt.FingerUp(x, y-72)
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		if tt.HasText(msg) {
			t.Fatal("a toast swiped up should close")
		}
	})
	t.Run("android swipes sideways", func(t *testing.T) {
		_, tt, r, _, _ := phoneToast(t, mobile.Material, env.Note{Title: msg})
		x, y := r.X+r.W/2, r.Y+r.H/2
		tt.FingerDown(x, y)
		for i := 1; i <= 6; i++ {
			tt.FingerMove(x+float32(i)*15, y)
			tt.Frame()
		}
		tt.FingerUp(x+90, y)
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		if tt.HasText(msg) {
			t.Fatal("a snackbar swiped aside should close")
		}
	})
	t.Run("a short swipe keeps it", func(t *testing.T) {
		_, tt, r, _, _ := phoneToast(t, mobile.Cupertino, env.Note{Title: msg})
		x, y := r.X+r.W/2, r.Y+r.H/2
		tt.FingerDown(x, y)
		tt.FingerMove(x, y-10)
		tt.Frame()
		tt.FingerUp(x, y-10)
		tt.Frame()
		if !tt.HasText(msg) {
			t.Fatal("a short swipe should leave the toast")
		}
	})
}
