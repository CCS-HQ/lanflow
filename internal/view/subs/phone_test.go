package subs

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// TestPhonePullRefresh: a pull to refresh (Refreshing set by the finger)
// runs 全部更新, and the indicator goes away once it is done, not before.
func TestPhonePullRefresh(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	e.Phone = true
	var pending []func()
	run := e.Run
	e.Run = func(job func() error, ok string) {
		pending = append(pending, func() { run(job, ok) })
	}
	p := New(e)
	tt := ui.NewTester(p.View, 390, 844)
	tt.Frame()
	if !tt.HasText("我的机场") {
		t.Fatalf("no cards: %q", tt.Texts())
	}
	// What a pull does (the finger sets Refreshing, PullToRefresh fires).
	p.refresh.Refreshing = true
	p.updateAll()
	tt.Frame()
	if !p.refresh.Refreshing || len(pending) != 1 {
		t.Fatalf("while updating: refreshing %v, %d jobs", p.refresh.Refreshing, len(pending))
	}
	pending[0]()
	tt.Frame()
	if p.refresh.Refreshing {
		t.Fatal("全部更新 done: still refreshing")
	}
	// (The fake's 备用机场 times out: the outcome is that error.)
	if len(*toasts) != 1 {
		t.Fatalf("toasts %v", *toasts)
	}
}
