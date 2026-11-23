package app

import (
	"sync"
	"testing"

	"lanflow/internal/model"
)

// Settings saved as a patch (UpdateSettings): the change is merged into
// the settings stored at that moment, under the lock every save takes, so
// writers that change different options never undo each other.

// TestUpdateSettingsConcurrentWritersBothLand: several writers read the
// same snapshot, each changes an option of its own and all save at once.
// With whole-settings saves the last one stored put the others back.
func TestUpdateSettingsConcurrentWritersBothLand(t *testing.T) {
	h := newHarness(t, nil)
	base := h.s.Settings()
	edits := []func(*model.Settings){
		func(s *model.Settings) { s.LogLevel = "debug" },
		func(s *model.Settings) { s.TrayNode = true },
		func(s *model.Settings) { s.DelayTimeoutMs = 3210 },
		func(s *model.Settings) { s.DNS.TakeOver = !base.DNS.TakeOver },
		func(s *model.Settings) { s.DNS.Nameserver = []string{"https://dns.example/dns-query"} }, // same section, another option
		func(s *model.Settings) {
			s.Sites = append(s.Sites, model.Site{Name: "example", URL: "https://example.org"})
		},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(edits))
	for i, edit := range edits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := model.CloneSettings(base)
			edit(&next)
			errs[i] = h.s.UpdateSettings(base, next)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got := h.s.Settings()
	if got.LogLevel != "debug" || !got.TrayNode || got.DelayTimeoutMs != 3210 || got.DNS.TakeOver == base.DNS.TakeOver ||
		len(got.DNS.Nameserver) != 1 || got.DNS.Nameserver[0] != "https://dns.example/dns-query" || len(got.Sites) != len(base.Sites)+1 {
		t.Fatalf("a concurrent change was lost: log level %q, tray node %v, delay timeout %d, DNS take over %v, nameservers %v, %d sites (had %d)",
			got.LogLevel, got.TrayNode, got.DelayTimeoutMs, got.DNS.TakeOver, got.DNS.Nameserver, len(got.Sites), len(base.Sites))
	}
	saved, err := loadSettings(h.dir)
	if err != nil || saved.LogLevel != "debug" || !saved.TrayNode || saved.DelayTimeoutMs != 3210 {
		t.Fatalf("on disk: %+v (%v)", saved, err)
	}
	// EditSettings is the same through the API.
	if err := EditSettings(h.s, func(s *model.Settings) { s.TrayMode = true }); err != nil || !h.s.Settings().TrayMode || !h.s.Settings().TrayNode {
		t.Fatalf("EditSettings: %v, tray mode %v, tray node %v", err, h.s.Settings().TrayMode, h.s.Settings().TrayNode)
	}
}

// TestUpdateSettingsBesideWholeSaves: a patch made from a snapshot that a
// whole SaveSettings (an import, the configuration sync) has overtaken
// changes its own option only, and is applied to the core like a save:
// the port is hot-patched, the mode the other save set stays in the core.
func TestUpdateSettingsBesideWholeSaves(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stopMainProbe(h)
	stale := h.s.Settings() // the patch writer's snapshot

	whole := h.s.Settings()
	whole.Mode = model.ModeGlobal
	whole.Bypass = append(whole.Bypass, "*.corp.example")
	if err := h.s.SaveSettings(whole); err != nil {
		t.Fatal(err)
	}

	next := stale
	next.MixedPort = 7777
	if err := h.s.UpdateSettings(stale, next); err != nil {
		t.Fatal(err)
	}
	got := h.s.Settings()
	if got.MixedPort != 7777 || got.Mode != model.ModeGlobal || len(got.Bypass) != len(whole.Bypass) {
		t.Fatalf("after the patch: port %d, mode %s, %d bypass entries (want 7777, global, %d)", got.MixedPort, got.Mode, len(got.Bypass), len(whole.Bypass))
	}
	h.ctrl.mu.Lock()
	last := h.ctrl.patches[len(h.ctrl.patches)-1]
	h.ctrl.mu.Unlock()
	if last.MixedPort == nil || *last.MixedPort != 7777 || last.Mode != nil {
		t.Fatalf("the core was patched with %+v, want the port alone", last)
	}
	if st := h.s.Status(); st.MixedPort != 7777 || st.Mode != model.ModeGlobal {
		t.Fatalf("status: port %d, mode %s", st.MixedPort, st.Mode)
	}

	// The other way round: a whole save still replaces everything, the
	// patched port included. That is what it is for (ImportSettings).
	if err := h.s.SaveSettings(stale); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Settings(); got.MixedPort != stale.MixedPort || got.Mode != stale.Mode {
		t.Fatalf("a whole save kept port %d, mode %s", got.MixedPort, got.Mode)
	}
}

// TestUpdateSettingsRefusedOrFailedKeepsOthers: a patch the service
// refuses (a taken port) stores nothing, and one the core does not take
// is taken back alone.
func TestUpdateSettingsRefusedOrFailedKeepsOthers(t *testing.T) {
	h := newHarness(t, nil)
	ctrl := &patchFailCtrl{ctrlClient: h.s.d.ctrl}
	h.s.d.ctrl = ctrl
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	stopMainProbe(h)
	base := h.s.Settings()

	h.taken[8001] = true
	next := base
	next.MixedPort, next.TrayNode = 8001, true
	if err := h.s.UpdateSettings(base, next); err == nil {
		t.Fatal("a taken port was accepted")
	}
	if got := h.s.Settings(); got.MixedPort != base.MixedPort || got.TrayNode {
		t.Fatalf("a refused patch stored something: port %d, tray node %v", got.MixedPort, got.TrayNode)
	}

	// While the failing patch is on its way, a setting changes without
	// opMu (updateSettings, as a finished backup or the sync folder do):
	// taking the patch back must not take that with it.
	h.s.d.portInUse = func(int, bool) bool {
		_ = h.s.updateSettings(func(set *model.Settings) { set.SSIDDefaultSub = "kept" })
		return false
	}
	ctrl.fail.Store(true)
	next = base
	next.MixedPort = 8002
	if err := h.s.UpdateSettings(base, next); err == nil {
		t.Fatal("the patch succeeded although the core refused it")
	}
	got := h.s.Settings()
	if got.MixedPort != base.MixedPort || h.s.Status().MixedPort != base.MixedPort {
		t.Fatalf("the failed port stayed: settings %d, status %d", got.MixedPort, h.s.Status().MixedPort)
	}
	if got.SSIDDefaultSub != "kept" {
		t.Fatal("taking the failed patch back undid a setting stored meanwhile")
	}
	saved, err := loadSettings(h.dir)
	if err != nil || saved.MixedPort != base.MixedPort || saved.SSIDDefaultSub != "kept" {
		t.Fatalf("on disk: port %d, default subscription %q (%v)", saved.MixedPort, saved.SSIDDefaultSub, err)
	}

	// The same window on a save that works: both are stored.
	ctrl.fail.Store(false)
	h.s.d.portInUse = func(int, bool) bool {
		_ = h.s.updateSettings(func(set *model.Settings) { set.SSIDDefaultSub = "again" })
		return false
	}
	whole := h.s.Settings()
	whole.MixedPort = 8003
	if err := h.s.SaveSettings(whole); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Settings(); got.MixedPort != 8003 || got.SSIDDefaultSub != "again" {
		t.Fatalf("a save undid a setting stored while it ran: port %d, default subscription %q", got.MixedPort, got.SSIDDefaultSub)
	}
}

// TestApplySectionsKeepOtherSettings: ApplyDNS and ApplySniffer replace
// their section of the settings stored now, not of a snapshot.
func TestApplySectionsKeepOtherSettings(t *testing.T) {
	h := newHarness(t, nil)
	d := h.s.Settings().DNS
	d.TakeOver = !d.TakeOver
	if err := EditSettings(h.s, func(s *model.Settings) { s.TrayNode = true }); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ApplyDNS(d); err != nil {
		t.Fatal(err)
	}
	sn := h.s.Settings().Sniffer
	sn.Enable = !sn.Enable
	if err := h.s.ApplySniffer(sn); err != nil {
		t.Fatal(err)
	}
	if got := h.s.Settings(); got.DNS.TakeOver != d.TakeOver || got.Sniffer.Enable != sn.Enable || !got.TrayNode {
		t.Fatalf("DNS take over %v, sniffer %v, tray node %v", got.DNS.TakeOver, got.Sniffer.Enable, got.TrayNode)
	}
}
