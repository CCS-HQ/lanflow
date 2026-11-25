package remote

import (
	"slices"
	"strings"
	"testing"

	"lanflow/internal/app"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
)

func TestTopicReadsCoverage(t *testing.T) {
	m := newMirror()
	for _, meth := range rpc.Manifest() {
		if meth.Agent || meth.Error || len(meth.Results) == 0 {
			continue
		}
		if meth.Name == "OnChange" || m.serves(meth.Name) || strings.HasSuffix(meth.Name, "Since") {
			continue
		}
		if !listedReads[meth.Name] && !staticReads[meth.Name] && !uncachedReads[meth.Name] {
			t.Errorf("cacheable read %q is missing from topicReads, staticReads, or uncachedReads", meth.Name)
		}
	}

	for topic := range topicReads {
		if !slices.Contains(allTopics, topic) {
			t.Errorf("topic %q in topicReads is missing from allTopics", topic)
		}
	}

	if slices.Contains(topicReads[app.TopicStatus], "Proxies") {
		t.Fatal("TopicStatus must not invalidate Proxies")
	}
	if slices.Contains(topicReads[app.TopicStatus], "NetInfo") {
		t.Fatal("TopicStatus must not invalidate NetInfo")
	}
	for _, want := range []string{"CaptureStatus", "CaptureFlows"} {
		if !slices.Contains(topicReads[app.TopicCapture], want) {
			t.Fatalf("TopicCapture missing %q", want)
		}
	}
}

func TestReadCachePerMethodGeneration(t *testing.T) {
	c := newReadCache()

	// Unrelated high-frequency invalidations must not discard an in-flight Rules read.
	gen := c.generation("Rules")
	c.invalidate(app.TopicTraffic)
	c.invalidate(app.TopicMetrics)
	c.invalidate(app.TopicConns)
	c.invalidate(app.TopicLogs)
	wantRules := []model.Rule{{Index: 0, Type: "MATCH", Proxy: "DIRECT"}}
	c.putAt(gen, "Rules", nil, []any{&wantRules})

	var gotRules []model.Rule
	if !c.get("Rules", nil, []any{&gotRules}) || len(gotRules) != 1 || gotRules[0].Proxy != "DIRECT" {
		t.Fatalf("Rules read was dropped by unrelated invalidation: got=%+v", gotRules)
	}

	// Mutating the returned slice must not mutate the cached copy.
	gotRules[0].Proxy = "MUTATED"
	var gotRules2 []model.Rule
	if !c.get("Rules", nil, []any{&gotRules2}) || gotRules2[0].Proxy != "DIRECT" {
		t.Fatalf("cached slice shared backing array: %+v", gotRules2)
	}

	// In-flight Rules read must be rejected when TopicRules, clear(), or reset() happens.
	gen = c.generation("Rules")
	c.invalidate(app.TopicRules)
	c.putAt(gen, "Rules", nil, []any{&wantRules})
	if c.get("Rules", nil, []any{&gotRules}) {
		t.Fatal("stale Rules put succeeded after TopicRules")
	}

	gen = c.generation("Rules")
	c.clear()
	c.putAt(gen, "Rules", nil, []any{&wantRules})
	if c.get("Rules", nil, []any{&gotRules}) {
		t.Fatal("stale Rules put succeeded after clear")
	}

	gen = c.generation("Rules")
	c.reset()
	c.putAt(gen, "Rules", nil, []any{&wantRules})
	if c.get("Rules", nil, []any{&gotRules}) {
		t.Fatal("stale Rules put succeeded after reset")
	}

	// Static reads survive clear() and unrelated topics, only dropped by drop()/reset().
	sGen := c.generation("LookupIP")
	c.clear()
	c.invalidate(app.TopicTraffic)
	wantEP := model.EndpointInfo{IP: "1.1.1.1"}
	c.putAt(sGen, "LookupIP", []any{"1.1.1.1"}, []any{&wantEP})
	var gotEP model.EndpointInfo
	if !c.get("LookupIP", []any{"1.1.1.1"}, []any{&gotEP}) || gotEP.IP != "1.1.1.1" {
		t.Fatalf("static read LookupIP lost after clear/TopicTraffic: %+v", gotEP)
	}
}

func TestReadCacheLookupIPBounded(t *testing.T) {
	c := newReadCache()
	c.lookupCap = 3

	putIP := func(ip string) {
		gen := c.generation("LookupIP")
		ep := model.EndpointInfo{IP: ip}
		c.putAt(gen, "LookupIP", []any{ip}, []any{&ep})
	}
	hasIP := func(ip string) bool {
		var ep model.EndpointInfo
		return c.get("LookupIP", []any{ip}, []any{&ep}) && ep.IP == ip
	}

	putIP("1.1.1.1")
	putIP("2.2.2.2")
	putIP("3.3.3.3")
	// Touch 1.1.1.1 so 2.2.2.2 becomes LRU.
	if !hasIP("1.1.1.1") {
		t.Fatal("expected 1.1.1.1 cached")
	}
	putIP("4.4.4.4")

	if hasIP("2.2.2.2") {
		t.Fatal("expected 2.2.2.2 to be evicted as LRU")
	}
	for _, ip := range []string{"1.1.1.1", "3.3.3.3", "4.4.4.4"} {
		if !hasIP(ip) {
			t.Fatalf("expected %s to remain cached", ip)
		}
	}
	if c.lookupList.Len() != 3 {
		t.Fatalf("lookupList len = %d, want 3", c.lookupList.Len())
	}

	// clear() keeps static LookupIP entries; reset() drops them.
	c.clear()
	if !hasIP("1.1.1.1") {
		t.Fatal("clear() should preserve static LookupIP entries")
	}
	c.reset()
	if hasIP("1.1.1.1") || c.lookupList.Len() != 0 || len(c.lookupElems) != 0 {
		t.Fatal("reset() should clear LookupIP LRU state")
	}
}
