package app

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

const aggTestYAML = `proxies:
  - {name: "香港 01", type: ss, server: hk.example.com, port: 8388, password: p, cipher: aes-128-gcm}
  - {name: "日本 02", type: ss, server: jp.example.com, port: 8388, password: p, cipher: aes-128-gcm}
  - {name: "剩余流量：1 GB", type: ss, server: x.example.com, port: 1, password: p, cipher: aes-128-gcm}
`

func aggTestLinks() string {
	user := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret"))
	return "ss://" + user + "@sg.example.com:8388#新加坡 01\n"
}

func aggTestSpec() model.AggregateSpec {
	spec := profile.DefaultAggregateSpec()
	spec.Sources = []model.AggregateSource{
		{Kind: model.AggSourceURL, URL: "https://a.example.com/sub", Prefix: "A "},
		{Kind: model.AggSourceLinks, Links: aggTestLinks()},
	}
	return spec
}

func aggHarness(t *testing.T) *harness {
	expire := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	return newHarness(t, func(h *harness) {
		h.prof.raw = map[string]profile.Fetched{
			"https://a.example.com/sub": {Data: []byte(aggTestYAML), Download: 3 << 30, Upload: 1 << 30, Total: 100 << 30, Expire: expire},
			"https://b.example.com/sub": {Data: []byte(base64.StdEncoding.EncodeToString([]byte(aggTestLinks()))), Total: 50 << 30},
		}
	})
}

func TestSaveAggregateCreatesAndUpdates(t *testing.T) {
	h := aggHarness(t)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	pv, err := h.s.PreviewAggregate(model.Subscription{}, aggTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Nodes) != 3 || pv.Nodes[0].Name != "A 香港 01" || pv.Nodes[2].Name != "新加坡 01" {
		t.Fatalf("preview nodes = %+v", pv.Nodes)
	}
	if len(h.store.List()) != 1 {
		t.Fatal("preview stored something")
	}

	sub, err := h.s.SaveAggregate(model.Subscription{Name: "我的聚合", AutoUpdateHours: 12}, aggTestSpec(), false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Source != model.SourceAggregate || sub.Name != "我的聚合" || sub.AutoUpdateHours != 12 {
		t.Fatalf("sub = %+v", sub)
	}
	if sub.Total != 100<<30 || sub.Download != 3<<30 || sub.Upload != 1<<30 || sub.Expire.Year() != 2027 || sub.LastError != "" {
		t.Fatalf("usage not taken from the link source: %+v", sub)
	}
	spec, err := h.s.AggregateSpec(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range spec.Sources {
		if src.ID == "" {
			t.Fatal("source stored without an ID")
		}
	}
	raw, _ := h.s.SubscriptionRaw(sub.ID)
	if !strings.Contains(raw, "A 香港 01") || !strings.Contains(raw, profile.AggGroupSelect) || strings.Contains(raw, "剩余流量") {
		t.Fatalf("content:\n%s", raw)
	}
	if cur, _ := h.store.Current(); cur.ID == sub.ID {
		t.Fatal("switched without use")
	}

	// Edit the current one: switch, add a source, save → reload.
	if err := h.s.UseSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	reloads := h.ctrl.reloads
	spec.Sources = append(spec.Sources, model.AggregateSource{Kind: model.AggSourceURL, URL: "https://b.example.com/sub", Prefix: "B "})
	sub.Name = "改名"
	if sub, err = h.s.SaveAggregate(sub, spec, false); err != nil {
		t.Fatal(err)
	}
	if h.ctrl.reloads != reloads+1 {
		t.Fatalf("reloads = %d, want %d", h.ctrl.reloads, reloads+1)
	}
	if sub.Name != "改名" || sub.Total != 150<<30 {
		t.Fatalf("sub after edit = %+v", sub)
	}
	raw, _ = h.s.SubscriptionRaw(sub.ID)
	if !strings.Contains(raw, "B 新加坡 01") {
		t.Fatalf("new source missing:\n%s", raw)
	}

	// Update: a source now fails → still updated, the failure is noted.
	h.prof.mu.Lock()
	delete(h.prof.raw, "https://b.example.com/sub")
	h.prof.mu.Unlock()
	if err := h.s.UpdateSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := h.store.Get(sub.ID)
	if !strings.Contains(got.LastError, "1 个来源失败") || got.Total != 100<<30 {
		t.Fatalf("after partial failure: %+v", got)
	}
	raw, _ = h.s.SubscriptionRaw(sub.ID)
	if strings.Contains(raw, "B 新加坡 01") {
		t.Fatal("the failed source's nodes stayed")
	}

	// Every source fails → the update fails and the content stays.
	h.prof.mu.Lock()
	h.prof.raw = nil
	h.prof.mu.Unlock()
	spec, _ = h.s.AggregateSpec(sub.ID)
	spec.Sources = spec.Sources[:1]
	_ = h.store.SetAggregateSpec(sub.ID, spec)
	if err := h.s.UpdateSubscription(sub.ID); err == nil || !strings.Contains(err.Error(), "聚合订阅更新失败") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := h.s.SubscriptionRaw(sub.ID); after != raw {
		t.Fatal("content replaced by a failed update")
	}
}

func TestSaveAggregateRefusals(t *testing.T) {
	h := aggHarness(t)
	if _, err := h.s.SaveAggregate(model.Subscription{}, aggTestSpec(), false); err == nil {
		t.Fatal("saved without a name")
	}
	if _, err := h.s.SaveAggregate(model.Subscription{Name: "x"}, model.AggregateSpec{}, false); err == nil {
		t.Fatal("saved without sources")
	}
	url := h.store.List()[0]
	if _, err := h.s.SaveAggregate(url, aggTestSpec(), false); err == nil || !strings.Contains(err.Error(), "不是聚合订阅") {
		t.Fatalf("a link subscription was overwritten: %v", err)
	}
	if _, err := h.s.AggregateSpec(url.ID); err == nil {
		t.Fatal("spec of a link subscription")
	}
	spec := aggTestSpec()
	spec.Sources = []model.AggregateSource{{Kind: model.AggSourceURL, URL: "https://missing.example.com"}}
	if _, err := h.s.SaveAggregate(model.Subscription{Name: "x"}, spec, false); err == nil || !strings.Contains(err.Error(), "没有可用的节点") {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.store.List()); n != 1 {
		t.Fatalf("a failed save left %d subscriptions", n)
	}
}

func TestAggregateFromSubscriptions(t *testing.T) {
	h := aggHarness(t)
	src := h.store.List()[0]
	_ = h.store.SetContent(src.ID, []byte(aggTestYAML))
	spec := profile.DefaultAggregateSpec()
	spec.Sources = []model.AggregateSource{{Kind: model.AggSourceSub, SubID: src.ID}}
	sub, err := h.s.SaveAggregate(model.Subscription{Name: "合并"}, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	// another aggregate cannot be a source
	spec.Sources = []model.AggregateSource{{Kind: model.AggSourceSub, SubID: sub.ID}, {Kind: model.AggSourceSub, SubID: "gone"}}
	pv, err := h.s.PreviewAggregate(model.Subscription{}, spec)
	if err == nil {
		t.Fatalf("preview from an aggregate and a missing subscription: %+v", pv)
	}
	if !strings.Contains(pv.Sources[0].Error, "不能引用另一个聚合订阅") || !strings.Contains(pv.Sources[1].Error, "已被删除") {
		t.Fatalf("reports = %+v", pv.Sources)
	}
}

func TestUpdateAllIncludesAggregatesLast(t *testing.T) {
	h := aggHarness(t)
	h.prof.fetched = profile.Fetched{Data: []byte(aggTestYAML)}
	sub, err := h.s.SaveAggregate(model.Subscription{Name: "聚合"}, aggTestSpec(), false)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.store.Get(sub.ID)
	time.Sleep(10 * time.Millisecond)
	if err := h.s.UpdateAllSubscriptions(); err != nil {
		t.Fatal(err)
	}
	after, _ := h.store.Get(sub.ID)
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatal("aggregate not updated by 全部更新")
	}
}
