package app

import (
	"testing"

	"lanflow/internal/model"
)

func TestManualNodesWithSubscription(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	reloadsBefore := h.ctrl.reloads

	saved, err := h.s.SaveManualNode(model.ManualNode{
		Name: "my-anytls", Type: model.ProxyTypeAnyTLS, Server: "1.2.3.4", Port: 443, Password: "pw", UDP: true,
	})
	if err != nil {
		t.Fatalf("SaveManualNode: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("expected generated ID")
	}
	if h.ctrl.reloads != reloadsBefore+1 {
		t.Fatalf("reloads = %d, want %d", h.ctrl.reloads, reloadsBefore+1)
	}
	if len(h.s.ManualNodes()) != 1 {
		t.Fatalf("ManualNodes len = %d", len(h.s.ManualNodes()))
	}

	// Duplicate name should be rejected.
	if _, err := h.s.SaveManualNode(model.ManualNode{
		Name: "my-anytls", Type: model.ProxyTypeSOCKS5, Server: "1.2.3.4", Port: 1080,
	}); err == nil {
		t.Fatal("expected duplicate name error")
	}

	// Edit existing node.
	saved.Port = 8443
	edited, err := h.s.SaveManualNode(saved)
	if err != nil || edited.Port != 8443 {
		t.Fatalf("edit SaveManualNode: %v, %+v", err, edited)
	}

	// Delete node.
	if err := h.s.DeleteManualNode(saved.ID); err != nil {
		t.Fatalf("DeleteManualNode: %v", err)
	}
	if len(h.s.ManualNodes()) != 0 {
		t.Fatalf("expected empty ManualNodes after delete, got %d", len(h.s.ManualNodes()))
	}
}

func TestManualNodesStandaloneWithoutSubscription(t *testing.T) {
	h := newHarness(t, nil)
	// Remove default subscription so store starts completely empty.
	_ = h.store.SetCurrent("")
	for _, s := range h.store.List() {
		_ = h.store.Delete(s.ID)
	}
	h.s.refreshStore(TopicSubs)
	if h.s.Status().HasSubscriptions {
		t.Fatal("expected HasSubscriptions=false when empty")
	}

	n, err := h.s.SaveManualNode(model.ManualNode{
		Name: "solo-ss", Type: model.ProxyTypeSS, Server: "9.9.9.9", Port: 8388,
		Cipher: "aes-128-gcm", Password: "pw", UDP: true,
	})
	if err != nil {
		t.Fatalf("standalone SaveManualNode: %v", err)
	}
	st := h.s.Status()
	if !st.HasSubscriptions {
		t.Fatal("expected HasSubscriptions=true with manual node")
	}
	if st.Core != model.CoreRunning {
		t.Fatalf("expected core auto-started on first manual node, got %s", st.Core)
	}

	if err := h.s.DeleteManualNode(n.ID); err != nil {
		t.Fatalf("DeleteManualNode: %v", err)
	}
	st = h.s.Status()
	if st.HasSubscriptions {
		t.Fatal("expected HasSubscriptions=false after deleting last manual node")
	}
	if st.Core != model.CoreStopped {
		t.Fatalf("expected core stopped after deleting last manual node, got %s", st.Core)
	}
}
