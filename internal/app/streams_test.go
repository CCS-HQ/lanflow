package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/model"
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

type countRulesCtrl struct {
	ctrlClient
	calls atomic.Int32
}

func (c *countRulesCtrl) Rules(ctx context.Context) ([]controller.Rule, error) {
	c.calls.Add(1)
	return c.ctrlClient.Rules(ctx)
}

// TestMetricsDoNotEmitTopicStatus: high-frequency traffic, memory and
// connection updates emit TopicTraffic / TopicMetrics / TopicConns without
// triggering TopicStatus.
func TestMetricsDoNotEmitTopicStatus(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.s.Shutdown)

	var mu sync.Mutex
	seen := map[Topic]int{}
	off := h.s.OnChange(func(tp Topic) {
		mu.Lock()
		seen[tp]++
		mu.Unlock()
	})
	defer off()

	waitUntil := func(check func() bool) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := check()
			mu.Unlock()
			if ok {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}

	// Wait past any startup coalesce window, then clear recorded topics.
	time.Sleep(coalesceWindow + 40*time.Millisecond)
	mu.Lock()
	clear(seen)
	mu.Unlock()

	h.s.onTraffic(controller.Traffic{Up: 120, Down: 340})
	h.s.onMemory(controller.Memory{InUse: 48 << 20})
	h.s.onConnections(controller.Connections{
		UploadTotal:   100,
		DownloadTotal: 200,
		Memory:        50 << 20,
		Connections:   []controller.Connection{{ID: "c1"}},
	})

	if !waitUntil(func() bool {
		return seen[TopicTraffic] >= 1 && seen[TopicMetrics] >= 1 && seen[TopicConns] >= 1
	}) {
		mu.Lock()
		t.Fatalf("missing expected metric topics: %+v", seen)
		mu.Unlock()
	}
	time.Sleep(coalesceWindow + 40*time.Millisecond)
	mu.Lock()
	statusCount := seen[TopicStatus]
	clear(seen)
	mu.Unlock()
	if statusCount != 0 {
		t.Fatalf("metric updates emitted TopicStatus %d times, want 0", statusCount)
	}

	// Unchanged connection metrics emit TopicConns without TopicMetrics or TopicStatus.
	h.s.onConnections(controller.Connections{
		UploadTotal:   100,
		DownloadTotal: 200,
		Memory:        50 << 20,
		Connections:   []controller.Connection{{ID: "c1"}},
	})
	if !waitUntil(func() bool { return seen[TopicConns] >= 1 }) {
		t.Fatal("TopicConns not emitted")
	}
	time.Sleep(coalesceWindow + 40*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if seen[TopicMetrics] != 0 || seen[TopicStatus] != 0 {
		t.Fatalf("unchanged metrics emitted %+v", seen)
	}
}

// TestStatusCountsWithoutRefreshingRules: Status() reports GroupCount,
// RuleCount and OverrideCount without triggering ctrl.Rules() refreshes.
func TestStatusCountsWithoutRefreshingRules(t *testing.T) {
	h := newHarness(t, nil)
	t.Cleanup(h.s.Shutdown)

	h.ctrl.mu.Lock()
	h.ctrl.proxies = map[string]controller.Proxy{
		"Proxy":  {Type: "Selector", All: []string{"n1"}},
		"Auto":   {Type: "URLTest", All: []string{"n1"}},
		"Hidden": {Type: "Selector", Hidden: true, All: []string{"n1"}},
		"GLOBAL": {Type: "Selector", All: []string{"Proxy", "Auto"}},
		"n1":     {Type: "Shadowsocks"},
	}
	h.ctrl.rules = []controller.Rule{
		{Index: 0, Type: "DOMAIN", Payload: "a.com", Proxy: "Proxy"},
		{Index: 1, Type: "DOMAIN", Payload: "b.com", Proxy: "Proxy"},
		{Index: 2, Type: "MATCH", Proxy: "Proxy"},
	}
	h.ctrl.mu.Unlock()

	if _, err := h.s.SaveOverrideEntry(model.Override{Name: "ov1", Enabled: true, Global: true, Text: "log-level: info\n"}); err != nil {
		t.Fatal(err)
	}
	cc := &countRulesCtrl{ctrlClient: h.ctrl}
	h.s.d.ctrl = cc
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	cc.calls.Store(0)
	h.s.mu.Lock()
	h.s.state.rulesAt = time.Now().Add(-time.Minute)
	h.s.mu.Unlock()

	st := h.s.Status()
	if st.GroupCount != 2 || st.RuleCount != 3 || st.OverrideCount != 1 {
		t.Fatalf("counts = (groups=%d rules=%d overrides=%d), want (2, 3, 1)", st.GroupCount, st.RuleCount, st.OverrideCount)
	}
	time.Sleep(30 * time.Millisecond)
	if cc.calls.Load() != 0 {
		t.Fatalf("Status() triggered %d ctrl.Rules() calls", cc.calls.Load())
	}
}
