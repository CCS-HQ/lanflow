package rpc

import (
	"testing"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// Aggregate source links never reach an agent, and what it sends back
// without them keeps the stored ones.
func TestAggregateLinksRedactedAndKept(t *testing.T) {
	f := fake.NewInstant()
	spec := model.AggregateSpec{Sources: []model.AggregateSource{
		{Kind: model.AggSourceURL, URL: "https://sub.example.com/?token=secret"},
		{Kind: model.AggSourceLinks, Links: "trojan://pw@x.example.com:443#X"},
		{Kind: model.AggSourceYAML, YAML: "proxies:\n  - {name: Y, type: ss, server: y.example.com, port: 1, password: secret, cipher: aes-128-gcm}\n"},
	}}
	sub, err := f.SaveAggregate(model.Subscription{Name: "聚合"}, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := f.AggregateSpec(sub.ID)

	red := redactAggregate(stored)
	for _, src := range red.Sources {
		if src.URL != "" || src.Links != "" || src.YAML != "" {
			t.Fatalf("link left in %+v", src)
		}
	}
	if stored.Sources[0].URL == "" {
		t.Fatal("redaction changed the stored spec")
	}
	got := RedactValue(stored).(map[string]any)["sources"].([]any)[0].(map[string]any)
	if _, ok := got["url"]; ok {
		t.Fatalf("RedactValue kept the url: %v", got)
	}

	red.Include = "HK"
	red.Sources = append(red.Sources, model.AggregateSource{Kind: model.AggSourceURL, URL: "https://new.example.com"})
	back := keepAggregateLinks(f, sub, red)
	if back.Sources[0].URL != stored.Sources[0].URL || back.Sources[1].Links != stored.Sources[1].Links || back.Sources[2].YAML != stored.Sources[2].YAML {
		t.Fatalf("links not restored: %+v", back.Sources)
	}
	if back.Sources[3].URL != "https://new.example.com" || back.Include != "HK" {
		t.Fatalf("agent's changes lost: %+v", back)
	}
	if out := keepAggregateLinks(f, model.Subscription{}, red); out.Sources[0].URL != "" {
		t.Fatal("links filled for a new subscription")
	}
}
