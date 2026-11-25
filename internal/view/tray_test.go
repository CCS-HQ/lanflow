package view

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/model"
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
