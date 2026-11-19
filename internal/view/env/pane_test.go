package env

import (
	"testing"

	"lanflow/internal/app/fake"
)

// A pane's size is loaded once, saved when it changes and forgotten when
// it goes back to the default (0); other keys are kept.
func TestPaneSizePersists(t *testing.T) {
	api := fake.NewInstant()
	run := func(job func() error, _ string) {
		if err := job(); err != nil {
			t.Fatal(err)
		}
	}
	e := &Env{API: api, Run: run}
	var w float32
	e.PaneSize(SizeConnsDetail, &w)
	if w != 0 {
		t.Fatalf("nothing stored, got %v", w)
	}
	w = 420
	e.PaneSize(SizeConnsDetail, &w)
	h := float32(300)
	e.PaneSize(SizeStatsChart, &h) // first call: loads, nothing stored yet
	h = 260
	e.PaneSize(SizeStatsChart, &h)
	if got := api.UIState().Sizes; got[SizeConnsDetail] != 420 || got[SizeStatsChart] != 260 {
		t.Fatalf("sizes = %v", got)
	}

	// A new window (a new Env) restores them.
	e2 := &Env{API: api, Run: run}
	var w2 float32
	e2.PaneSize(SizeConnsDetail, &w2)
	if w2 != 420 {
		t.Fatalf("restored %v, want 420", w2)
	}
	// A double click back to the default forgets the size.
	w2 = 0
	e2.PaneSize(SizeConnsDetail, &w2)
	got := api.UIState().Sizes
	if _, ok := got[SizeConnsDetail]; ok || got[SizeStatsChart] != 260 {
		t.Fatalf("after reset sizes = %v", got)
	}
}
