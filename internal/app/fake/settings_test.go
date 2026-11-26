package fake

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// Repeated renders must keep the files' timestamp, even across a minute
// boundary. synctest advances time without waiting on the wall clock.
func TestGeoDataInfoStableUntilUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := NewInstant()
		before := f.GeoDataInfo()
		if before.Files == 0 || before.UpdatedAt.IsZero() {
			t.Fatalf("missing sample database metadata: %+v", before)
		}
		time.Sleep(time.Minute)
		if got := f.GeoDataInfo(); got != before {
			t.Fatalf("reading after a minute changed the files' timestamp: before=%+v, after=%+v", before, got)
		}
		if err := f.UpdateGeoData(); err != nil {
			t.Fatal(err)
		}
		updated := f.GeoDataInfo()
		if !updated.UpdatedAt.Equal(time.Now()) || updated.Files != before.Files || !updated.UpdatedAt.Equal(f.Settings().Geo.UpdatedAt) {
			t.Fatalf("manual update did not update the files and settings together: %+v", updated)
		}
		time.Sleep(time.Minute)
		if got := f.GeoDataInfo(); got != updated {
			t.Fatalf("the downloaded files' timestamp drifted: before=%+v, after=%+v", updated, got)
		}
	})
}

// TestUpdateSettingsMerges: the fake saves a patch like the service does
// (the views' tests rely on it): writers that read the same settings and
// change different options all land, a whole SaveSettings replaces, and a
// refused value stores nothing.
func TestUpdateSettingsMerges(t *testing.T) {
	f := NewInstant()
	base := f.Settings()
	edits := []func(*model.Settings){
		func(s *model.Settings) { s.LogLevel = "debug" },
		func(s *model.Settings) { s.TrayNode = true },
		func(s *model.Settings) { s.DNS.TakeOver = !base.DNS.TakeOver },
		func(s *model.Settings) {
			s.Sites = append(s.Sites, model.Site{Name: "example", URL: "https://example.org"})
		},
	}
	var wg sync.WaitGroup
	for _, edit := range edits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := model.CloneSettings(base)
			edit(&next)
			if err := f.UpdateSettings(base, next); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got := f.Settings()
	if got.LogLevel != "debug" || !got.TrayNode || got.DNS.TakeOver == base.DNS.TakeOver || len(got.Sites) != len(base.Sites)+1 {
		t.Fatalf("a change was lost: log level %q, tray node %v, DNS take over %v, %d sites (had %d)",
			got.LogLevel, got.TrayNode, got.DNS.TakeOver, len(got.Sites), len(base.Sites))
	}

	// A patch beside a whole save.
	whole := f.Settings()
	whole.Mode = model.ModeGlobal
	if err := f.SaveSettings(whole); err != nil {
		t.Fatal(err)
	}
	if err := app.EditSettings(f, func(s *model.Settings) { s.MixedPort = 7777 }); err != nil {
		t.Fatal(err)
	}
	next := base // older than both
	next.TrayMode = true
	if err := f.UpdateSettings(base, next); err != nil {
		t.Fatal(err)
	}
	if got := f.Settings(); got.Mode != model.ModeGlobal || got.MixedPort != 7777 || !got.TrayMode || f.Status().MixedPort != 7777 {
		t.Fatalf("mode %s, port %d (status %d), tray mode %v", got.Mode, got.MixedPort, f.Status().MixedPort, got.TrayMode)
	}

	// The port the fake refuses: nothing of the patch is stored.
	next = f.Settings()
	next.MixedPort, next.TrayGraph = 7891, true
	if err := f.UpdateSettings(f.Settings(), next); err == nil {
		t.Fatal("port 7891 was accepted")
	}
	if got := f.Settings(); got.MixedPort != 7777 || got.TrayGraph {
		t.Fatalf("a refused patch was stored: port %d, tray graph %v", got.MixedPort, got.TrayGraph)
	}
}
