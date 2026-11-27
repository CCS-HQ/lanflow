package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestGeoAutoUpdateDueAndCatchUp(t *testing.T) {
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	var now atomic.Value
	now.Store(base)

	h := newHarness(t, nil)
	h.s.d.now = func() time.Time { return now.Load().(time.Time) }

	// Seed a stale geo file from 48h ago (simulating downtime across the interval).
	if err := os.MkdirAll(h.s.workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := base.Add(-48 * time.Hour)
	geoPath := filepath.Join(h.s.workDir, "geoip.metadb")
	if err := os.WriteFile(geoPath, []byte("old-geo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(geoPath, stale, stale); err != nil {
		t.Fatal(err)
	}

	set := h.s.Settings()
	set.Geo.AutoUpdate = true
	set.Geo.IntervalHours = 24
	set.Geo.UpdatedAt = time.Time{}
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}

	// Core stopped -> no update yet.
	if h.s.maybeAutoUpdateGeo() {
		t.Fatal("must not auto-update while core is stopped")
	}

	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}

	// Overdue relative to disk mtime (48h >= 24h) -> updates at once.
	if !h.s.maybeAutoUpdateGeo() {
		t.Fatal("expected overdue catch-up geo update")
	}
	h.ctrl.mu.Lock()
	calls := h.ctrl.geoUpdates
	h.ctrl.mu.Unlock()
	if calls != 1 {
		t.Fatalf("UpdateGeo calls = %d, want 1", calls)
	}
	if got := h.s.Settings().Geo.UpdatedAt; !got.Equal(base) {
		t.Fatalf("Settings.Geo.UpdatedAt = %v, want %v", got, base)
	}

	// Within the 24h window -> skipped.
	now.Store(base.Add(23 * time.Hour))
	if h.s.maybeAutoUpdateGeo() {
		t.Fatal("must not update before interval elapses")
	}

	// With AutoUpdate disabled -> skipped even past interval.
	now.Store(base.Add(30 * time.Hour))
	set = h.s.Settings()
	set.Geo.AutoUpdate = false
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if h.s.maybeAutoUpdateGeo() {
		t.Fatal("must not update when AutoUpdate is disabled")
	}

	// Re-enabled past interval -> failure backs off, then succeeds after backoff.
	origRetry := geoRetryMin
	geoRetryMin = 10 * time.Minute
	t.Cleanup(func() { geoRetryMin = origRetry })

	set.Geo.AutoUpdate = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	h.ctrl.updateGeoFn = func() error { return errors.New("network down") }
	h.ctrl.mu.Unlock()

	if h.s.maybeAutoUpdateGeo() {
		t.Fatal("expected failure to return false")
	}
	// Immediate retry within backoff window is suppressed.
	now.Store(base.Add(30*time.Hour + 2*time.Minute))
	if h.s.maybeAutoUpdateGeo() {
		t.Fatal("expected backoff to suppress immediate retry")
	}
	h.ctrl.mu.Lock()
	if h.ctrl.geoUpdates != 2 {
		t.Fatalf("expected 1 failed attempt (total 2 calls), got %d", h.ctrl.geoUpdates)
	}
	h.ctrl.updateGeoFn = nil
	h.ctrl.mu.Unlock()

	// Once backoff elapses, retry runs and catches up.
	now.Store(base.Add(30*time.Hour + 11*time.Minute))
	if !h.s.maybeAutoUpdateGeo() {
		t.Fatal("expected retry after backoff to succeed")
	}
	h.ctrl.mu.Lock()
	calls = h.ctrl.geoUpdates
	h.ctrl.mu.Unlock()
	if calls != 3 {
		t.Fatalf("UpdateGeo calls = %d, want 3", calls)
	}
}

func TestGeoAutoUpdateLoopCatchUpOnStartAndSettingsChange(t *testing.T) {
	origDelay, origTick := geoAutoUpdateDelay, geoAutoUpdateTick
	geoAutoUpdateDelay = 5 * time.Millisecond
	geoAutoUpdateTick = time.Hour
	t.Cleanup(func() {
		geoAutoUpdateDelay = origDelay
		geoAutoUpdateTick = origTick
	})

	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
	var now atomic.Value
	now.Store(base)

	var updates atomic.Int32
	done := make(chan struct{}, 4)

	h := newHarness(t, nil)
	h.s.d.now = func() time.Time { return now.Load().(time.Time) }
	h.ctrl.mu.Lock()
	h.ctrl.updateGeoFn = func() error {
		updates.Add(1)
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	}
	h.ctrl.mu.Unlock()

	// Simulate an update recorded 30h before startup with AutoUpdate enabled.
	set := h.s.Settings()
	set.Geo.AutoUpdate = true
	set.Geo.IntervalHours = 24
	set.Geo.UpdatedAt = base.Add(-30 * time.Hour)
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.s.goBackground(h.s.geoAutoUpdateLoop)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("geoAutoUpdateLoop did not catch up after startup settle")
	}
	if got := updates.Load(); got != 1 {
		t.Fatalf("startup catch-up count = %d, want 1", got)
	}

	// Advance clock by 2h (still < 24h), then lower IntervalHours to 1h ->
	// SaveSettings wakes the loop and triggers catch-up immediately.
	now.Store(base.Add(2 * time.Hour))
	set = h.s.Settings()
	set.Geo.IntervalHours = 1
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("geoAutoUpdateLoop did not wake on shorter IntervalHours")
	}
	if got := updates.Load(); got != 2 {
		t.Fatalf("settings-triggered catch-up count = %d, want 2", got)
	}
}
