package view

import (
	"bytes"
	"image/png"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/trayart"
)

// TestDelayRebuildWait: node delays alone rebuild the tray menu at most
// every trayDelayGap.
func TestDelayRebuildWait(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		built time.Time
		want  time.Duration
	}{
		{"never built", time.Time{}, 0},
		{"just built", now, trayDelayGap},
		{"half way", now.Add(-trayDelayGap / 2), trayDelayGap / 2},
		{"gap passed", now.Add(-trayDelayGap), 0},
		{"long ago", now.Add(-time.Hour), 0},
		{"clock went back", now.Add(time.Hour), 0},
	} {
		if got := delayRebuildWait(tc.built, now); got != tc.want {
			t.Errorf("%s: wait %v, want %v", tc.name, got, tc.want)
		}
	}
}

type countTrayAPI struct {
	*fake.Fake
	proxies, subs, captureStatus atomic.Int32
}

func (a *countTrayAPI) Proxies() model.ProxySnapshot {
	a.proxies.Add(1)
	return a.Fake.Proxies()
}
func (a *countTrayAPI) Subscriptions() []model.Subscription {
	a.subs.Add(1)
	return a.Fake.Subscriptions()
}
func (a *countTrayAPI) CaptureStatus() model.CaptureStatus {
	a.captureStatus.Add(1)
	return a.Fake.CaptureStatus()
}

type testTrayHost struct{ api app.API }

func (h testTrayHost) hostAPI() app.API             { return h.api }
func (h testTrayHost) mac() bool                    { return false }
func (h testTrayHost) runJob(func() error, string)  {}
func (h testTrayHost) note(string)                  {}
func (h testTrayHost) failed(string)                {}
func (h testTrayHost) showWindow(string)            {}
func (h testTrayHost) toggleWindow()                {}
func (h testTrayHost) setTUN(bool)                  {}
func (h testTrayHost) live() bool                   { return false }
func (h testTrayHost) quit()                        {}
func (h testTrayHost) extraItems() []*mygo.MenuItem { return nil }

func TestTrayTrafficAndMetricsSkipProxiesClone(t *testing.T) {
	inner := fake.NewInstant()
	api := &countTrayAPI{Fake: inner}
	tr := &tray{h: testTrayHost{api: api}}
	tr.refresh(true)

	api.proxies.Store(0)
	api.subs.Store(0)
	api.captureStatus.Store(0)

	tr.changed(app.TopicTraffic)
	tr.changed(app.TopicMetrics)
	tr.changed(app.TopicCapture) // CaptureStatus().Enabled unchanged -> no refresh
	if api.proxies.Load() != 0 || api.subs.Load() != 0 {
		t.Fatalf("high-frequency topics cloned Proxies=%d Subscriptions=%d", api.proxies.Load(), api.subs.Load())
	}
	if api.captureStatus.Load() != 1 {
		t.Fatalf("expected 1 CaptureStatus check on TopicCapture only, got %d", api.captureStatus.Load())
	}

	// When decryption toggles on, TopicCapture triggers a tray refresh.
	if err := inner.SaveCaptureSettings(model.CaptureSettings{Domains: []string{"example.com"}, Acknowledged: true}); err != nil {
		t.Fatal(err)
	}
	if err := inner.SetCapture(true); err != nil {
		t.Fatal(err)
	}
	tr.changed(app.TopicCapture)
	if api.proxies.Load() != 1 {
		t.Fatalf("CaptureStatus change should refresh tray once, got Proxies=%d", api.proxies.Load())
	}
}

func TestTrayStatusMenuItemAndColoredDots(t *testing.T) {
	for _, tc := range []struct {
		state model.CoreState
		dark  bool
		want  string
	}{
		{model.CoreRunning, false, "green"},
		{model.CoreRunning, true, "green"},
		{model.CoreStarting, false, "orange"},
		{model.CoreError, false, "red"},
		{model.CoreMissing, true, "red"},
		{model.CoreStopped, false, "ring"},
	} {
		raw := trayart.StatusDotPNG(tc.state, tc.dark)
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("StatusDotPNG(%v, %v): %v", tc.state, tc.dark, err)
		}
		if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
			t.Fatalf("StatusDotPNG(%v, %v) size = %v, want 32x32", tc.state, tc.dark, img.Bounds())
		}
		r, g, b, a := img.At(15, 15).RGBA()
		r8, g8, b8, a8 := r>>8, g>>8, b>>8, a>>8
		switch tc.want {
		case "green":
			if a8 != 255 || !(g8 > r8 && g8 > b8) {
				t.Fatalf("%v/%v center RGBA=(%d,%d,%d,%d), want solid green", tc.state, tc.dark, r8, g8, b8, a8)
			}
		case "orange":
			if a8 != 255 || !(r8 > g8 && g8 > b8) {
				t.Fatalf("%v/%v center RGBA=(%d,%d,%d,%d), want solid orange", tc.state, tc.dark, r8, g8, b8, a8)
			}
		case "red":
			if a8 != 255 || !(r8 > g8 && r8 > b8) {
				t.Fatalf("%v/%v center RGBA=(%d,%d,%d,%d), want solid red", tc.state, tc.dark, r8, g8, b8, a8)
			}
		case "ring":
			if a8 != 0 {
				t.Fatalf("%v/%v center alpha=%d, want hollow ring center (0)", tc.state, tc.dark, a8)
			}
			_, _, _, ra := img.At(15, 10).RGBA()
			if ra>>8 != 255 {
				t.Fatalf("%v/%v ring perimeter alpha=%d, want 255", tc.state, tc.dark, ra>>8)
			}
		}
	}

	api := fake.NewInstant()
	if err := api.StartCore(); err != nil {
		t.Fatal(err)
	}
	tr := &tray{h: testTrayHost{api: api}}
	tr.refresh(true)
	tr.mu.Lock()
	menu := tr.menu
	tr.mu.Unlock()
	if menu == nil {
		t.Fatal("expected tray menu to be built")
	}
	it := menu.ItemByID("status")
	if it == nil {
		t.Fatal("missing status menu item")
	}
	if it.Disabled {
		t.Fatal("status menu item must be enabled so the colored dot is not dimmed gray")
	}
	if it.Icon == nil {
		t.Fatal("status menu item must have a colored dot Icon")
	}
	if got := it.Label; got == statusLine(api.Status()) || got != statusMenuLabel(api.Status()) {
		t.Fatalf("status menu label = %q, want %q without monochrome dot prefix", got, statusMenuLabel(api.Status()))
	}
}
