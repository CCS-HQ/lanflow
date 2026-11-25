package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"lanflow/internal/controller"
	"lanflow/internal/model"
)

// chainCtrl makes the fake core show chain group name in 流媒体 (and
// nothing else of the chains), as the composed config would.
func chainCtrl(h *harness, name string) {
	h.ctrl.mu.Lock()
	defer h.ctrl.mu.Unlock()
	g := h.ctrl.proxies["流媒体"]
	all := []string{"节点选择", "香港 01"}
	if name != "" {
		all = append(all, name)
		h.ctrl.proxies[name] = controller.Proxy{Name: name, Type: "Selector", Now: "LANFLOW-CHAIN-x-2", All: []string{"LANFLOW-CHAIN-x-2"}}
	}
	g.All = all
	if !slices.Contains(all, g.Now) {
		g.Now = all[0] // the core falls back to the first member
	}
	h.ctrl.proxies["流媒体"] = g
}

func TestSetProxyChainsRenameAndDelete(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.ctrl.reloadFn = func(string) error {
		cs := h.store.ProxyChains()
		name := ""
		if len(cs) > 0 && cs[0].Enabled {
			name = cs[0].Name
		}
		chainCtrl(h, name)
		return nil
	}

	saved, err := h.s.SetProxyChains([]model.ProxyChain{{Name: " 中转 ", Hops: []string{"香港 01", "东京 03"}, Enabled: true}})
	if err != nil || len(saved) != 1 || saved[0].ID == "" || saved[0].Name != "中转" {
		t.Fatalf("SetProxyChains: %v %+v", err, saved)
	}
	if got := h.s.ProxyChains(); len(got) != 1 || got[0].ID != saved[0].ID {
		t.Fatalf("ProxyChains: %+v", got)
	}
	if in := h.prof.inputs[len(h.prof.inputs)-1]; len(in.ProxyChains) != 1 {
		t.Fatal("chains not composed")
	}
	if _, err := h.store.SetCustomRules([]model.CustomRule{{ID: "r1", Type: "DOMAIN-SUFFIX", Payload: "a.com", Target: "中转"}}); err != nil {
		t.Fatal(err)
	}
	h.s.refreshStore(TopicRules)
	if err := h.s.SelectProxy("流媒体", "香港 01"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SelectProxy("流媒体", "中转"); err != nil {
		t.Fatal(err)
	}

	// rename: the rule and the selection follow
	ch := saved[0]
	ch.Name = "新名"
	if _, err := h.s.SetProxyChains([]model.ProxyChain{ch}); err != nil {
		t.Fatal(err)
	}
	if r := h.store.CustomRules(); r[0].Target != "新名" {
		t.Fatalf("rule target: %+v", r)
	}
	if now := h.ctrl.proxies["流媒体"].Now; now != "新名" {
		t.Fatalf("流媒体 selects %q after the rename", now)
	}

	// deleting a chain a rule targets is refused
	if _, err := h.s.SetProxyChains(nil); err == nil || !strings.Contains(err.Error(), "a.com") {
		t.Fatalf("delete of a rule target: %v", err)
	}
	if len(h.s.ProxyChains()) != 1 {
		t.Fatal("chain deleted anyway")
	}

	// without the rule: the group goes back to what it had before
	if _, err := h.store.SetCustomRules(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.SetProxyChains([]model.ProxyChain{}); err != nil {
		t.Fatal(err)
	}
	if now := h.ctrl.proxies["流媒体"].Now; now != "香港 01" {
		t.Fatalf("流媒体 selects %q after the delete, want the previous 香港 01", now)
	}
	if ns := h.s.TakeNotices(); !slices.ContainsFunc(ns, func(n Notice) bool { return strings.Contains(n.Detail, "香港 01") }) {
		t.Fatalf("no notice: %+v", ns)
	}
}

func TestSetProxyChainsValidation(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]model.ProxyChain{
		{{Name: "X", Hops: []string{"香港 01"}}},
		{{Name: "X", Hops: []string{"香港 01", "香港 01"}}},
		{{Name: "LANFLOW-X", Hops: []string{"香港 01", "东京 03"}}},
		{{Name: "X", Hops: []string{"香港 01", "LANFLOW-MITM-1"}}},
		{{Name: "X", Hops: []string{"香港 01", "东京 03"}}, {Name: "X", Hops: []string{"东京 03", "香港 01"}}},
		{{Name: "节点选择", Hops: []string{"香港 01", "东京 03"}}},
		{{Name: "链式代理", Hops: []string{"香港 01", "东京 03"}}},
		{{ID: "../x", Name: "X", Hops: []string{"香港 01", "东京 03"}}},
	} {
		if _, err := h.s.SetProxyChains(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if len(h.s.ProxyChains()) != 0 {
		t.Fatal("stored")
	}
	// a reload the core refuses puts everything back
	h.ctrl.reloadFn = func(string) error { return errors.New("boom") }
	if _, err := h.s.SetProxyChains([]model.ProxyChain{{Name: "X", Hops: []string{"香港 01", "东京 03"}, Enabled: true}}); err == nil {
		t.Fatal("failed reload reported success")
	}
	if len(h.store.ProxyChains()) != 0 {
		t.Fatal("chain kept after a failed reload")
	}
}
