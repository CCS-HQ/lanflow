package fake

import (
	"strings"
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// An aggregate subscription shows what it generated: its profile in
// 查看配置, its groups and nodes once in use; another subscription brings
// the sample proxies back.
func TestAggregateShowsGeneratedProfile(t *testing.T) {
	f := NewInstant()
	spec := profile.DefaultAggregateSpec()
	spec.Sources = []model.AggregateSource{{Kind: model.AggSourceLinks, Links: "- {name: 我的节点, type: ss, server: 1.2.3.4, port: 8388, cipher: aes-128-gcm, password: p}"}}
	sub, err := f.SaveAggregate(model.Subscription{Name: "自己的"}, spec, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := f.SubscriptionRaw(sub.ID)
	if !strings.Contains(raw, "我的节点") || !strings.Contains(raw, "proxy-groups:") {
		t.Fatalf("raw = %s", raw)
	}
	px := f.Proxies()
	if px.MainGroup != "🚀 节点选择" || px.Proxies["我的节点"].Type != "Shadowsocks" {
		t.Fatalf("proxies = %+v", px)
	}
	if _, ok := px.Proxies["东京 03"]; ok {
		t.Fatal("sample nodes kept")
	}
	if err := f.UseSubscription("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Proxies().Proxies["东京 03"]; !ok {
		t.Fatal("sample proxies not back")
	}
}
