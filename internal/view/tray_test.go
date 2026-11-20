package view

import (
	"testing"
	"time"
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
