package app

import (
	"testing"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// TestBackgroundSlowsConnectionStream: the connections stream opens at
// connInterval; SetBackground(true) reopens it at connIntervalHidden and
// SetBackground(false) brings it back, each once per change.
func TestBackgroundSlowsConnectionStream(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	intervals := func() []time.Duration {
		h.ctrl.mu.Lock()
		defer h.ctrl.mu.Unlock()
		return append([]time.Duration(nil), h.ctrl.connIntervals...)
	}
	wait := func(n int) []time.Duration {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if got := intervals(); len(got) >= n {
				return got
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("stream opened %d times, want %d", len(intervals()), n)
		return nil
	}
	if got := wait(1); got[0] != connInterval {
		t.Fatalf("first stream interval %v, want %v", got[0], connInterval)
	}
	h.s.SetBackground(true)
	h.s.SetBackground(true) // no change: no restart
	if got := wait(2); got[1] != connIntervalHidden {
		t.Fatalf("hidden stream interval %v, want %v", got[1], connIntervalHidden)
	}
	h.s.SetBackground(false)
	if got := wait(3); got[2] != connInterval || len(got) != 3 {
		t.Fatalf("stream intervals %v", got)
	}
	time.Sleep(20 * time.Millisecond)
	if got := intervals(); len(got) != 3 {
		t.Errorf("an unchanged background restarted the stream: %v", got)
	}
	h.s.Shutdown()
}

func TestCaptureReturnFoldingAndTrafficDedup(t *testing.T) {
	t0 := time.Now().Add(-5 * time.Second)
	raw := []controller.Connection{
		// Normal connection: up=100, down=200
		{ID: "norm", Upload: 100, Download: 200, Start: t0, Chains: []string{"Proxy"},
			Metadata: controller.Metadata{Host: "other.com", DestinationPort: "443", InboundName: "mixed"}},
		// Client -> capture agent leg: up=300, down=400
		{ID: "cap-leg", Upload: 300, Download: 400, Start: t0, Chains: []string{profile.CaptureOutboundPrefix + "abcd"},
			Metadata: controller.Metadata{Host: "example.com", DestinationPort: "443", DestinationIP: "93.184.216.34", InboundName: "mixed"}},
		// Return leg from capture agent back into mihomo: up=300, down=400 (must be folded & excluded from totals)
		{ID: "cap-ret", Upload: 300, Download: 400, Start: t0.Add(50 * time.Millisecond), Chains: []string{"JP-01", "StreamGroup"},
			Metadata: controller.Metadata{Host: "example.com", DestinationPort: "443", DestinationIP: "93.184.216.34", InboundName: profile.CaptureUpListener}},
	}

	h := newHarness(t, nil)
	h.s.onConnections(controller.Connections{
		UploadTotal:   700,
		DownloadTotal: 1000,
		Connections:   raw,
	})

	conns := h.s.Connections()
	if len(conns) != 3 {
		t.Fatalf("expected 3 connections (raw snapshot retains CaptureReturn), got %d: %+v", len(conns), conns)
	}
	var leg, ret model.Connection
	for _, c := range conns {
		if c.ID == "cap-ret" {
			ret = c
		}
		if c.ID == "cap-leg" {
			leg = c
		}
	}
	if !ret.CaptureReturn {
		t.Fatalf("expected cap-ret CaptureReturn=true: %+v", ret)
	}
	if !leg.CaptureLeg || leg.CaptureReturn || len(leg.CaptureReturnChains) != 2 || leg.CaptureReturnChains[0] != "StreamGroup" || leg.CaptureReturnChains[1] != "JP-01" {
		t.Fatalf("CaptureReturnChains not paired onto CaptureLeg: %+v", leg)
	}
	st := h.s.Status()
	// Upload: 700 - 300 = 400; Download: 1000 - 400 = 600.
	if st.TotalUp != 400 || st.TotalDown != 600 || st.Connections != 2 {
		t.Fatalf("totals not deduplicated: %+v", st)
	}

	// Next snapshot where return leg briefly closes first should preserve remembered CaptureReturnChains on cap-leg
	// and keep deduplicated totals via innerClosedUp/innerClosedDown.
	h.s.onConnections(controller.Connections{
		UploadTotal:   700,
		DownloadTotal: 1000,
		Connections:   raw[:2],
	})
	for _, c := range h.s.Connections() {
		if c.ID == "cap-leg" && (len(c.CaptureReturnChains) != 2 || c.CaptureReturnChains[0] != "StreamGroup" || c.CaptureReturnChains[1] != "JP-01") {
			t.Fatalf("remembered CaptureReturnChains lost: %+v", c)
		}
	}
	st = h.s.Status()
	if st.TotalUp != 400 || st.TotalDown != 600 || st.Connections != 2 {
		t.Fatalf("totals changed after CaptureReturn closed: %+v", st)
	}
}
