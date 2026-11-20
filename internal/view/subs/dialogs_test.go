package subs

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// A stored interval the server chose (e.g. 8 h) shows as the nearest
// offered choice, never as 关闭; only ≤ 0 is 关闭 (docs/design.md:345).
func TestAutoIndexSnapsToNearestChoice(t *testing.T) {
	cases := map[int]int{0: 0, -1: 0, 1: 1, 6: 1, 8: 1, 9: 1, 10: 2, 12: 2, 18: 2, 19: 3, 24: 3, 72: 3}
	for hours, want := range cases {
		if got := autoIndex(hours); got != want {
			t.Errorf("autoIndex(%d) = %d (%s), want %d (%s)", hours, got, autoLabels[got], want, autoLabels[want])
		}
	}
	// Saving the dialog round-trips a snapped value to an offered one.
	if autoHours[autoIndex(8)] != 6 {
		t.Fatalf("8 h saves as %d", autoHours[autoIndex(8)])
	}
}

// TestImportCancelAndLateResult: while the download runs 取消 does not
// hide the dialog (the import would still land), and the result of an
// import whose dialog was replaced does not touch the new dialog.
func TestImportCancelAndLateResult(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	var held []func()
	p.SetAsync(func(fn func()) { held = append(held, fn) }, nil)
	tt := ui.NewTester(p.View, 800, 600)
	p.imp = importDialog{open: true, use: true, url: "https://example.com/sub", name: "迟到的"}
	tt.Frame()
	p.doImport()
	tt.Frame()
	if len(held) != 1 || p.imp.phase != phaseLoading {
		t.Fatalf("import not started: %d jobs, phase %d", len(held), p.imp.phase)
	}
	// 取消 is disabled: clicking it leaves the dialog open and loading.
	_ = tt.Click("取消")
	tt.Frame()
	if !p.imp.open || p.imp.phase != phaseLoading {
		t.Fatalf("取消 closed a running import: open %v phase %d", p.imp.open, p.imp.phase)
	}
	// A deep link opens a new dialog meanwhile; the user starts typing.
	p.imp = importDialog{open: true, use: true, url: "https://other.example/typing"}
	tt.Frame()
	n := len(api.Subscriptions())
	held[0]() // the first import lands
	tt.Frame()
	tt.Advance(time.Second)
	if !p.imp.open || p.imp.phase != phaseIdle || p.imp.url != "https://other.example/typing" {
		t.Fatalf("the late result changed the new dialog: %+v", p.imp)
	}
	if len(api.Subscriptions()) != n+1 || len(*toasts) == 0 {
		t.Fatalf("the import itself: %d subscriptions (were %d), toasts %q", len(api.Subscriptions()), n, *toasts)
	}
	// The new dialog's own import still ends in it.
	p.doImport()
	held[1]()
	tt.Frame()
	if p.imp.phase != phaseDone {
		t.Fatalf("own import: phase %d", p.imp.phase)
	}
}
