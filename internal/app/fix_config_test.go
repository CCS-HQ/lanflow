package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/syncfile"
)

// ---- the keychain: never under a lock the UI takes, never in a loop ----

// gatedSecrets is a keychain whose reads wait at a gate (a prompt the
// user has not answered) and can fail.
type gatedSecrets struct {
	mu     sync.Mutex
	m      map[string]string
	gate   chan struct{} // reads wait until it is closed
	getErr error         // what reads answer instead of the value
	setErr error
	gets   map[string]int
	sets   map[string]int
}

func newGatedSecrets() *gatedSecrets {
	return &gatedSecrets{m: map[string]string{}, gate: make(chan struct{}), gets: map[string]int{}, sets: map[string]int{}}
}

func (g *gatedSecrets) Get(key string) ([]byte, error) {
	g.mu.Lock()
	g.gets[key]++
	gate := g.gate
	g.mu.Unlock()
	<-gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.getErr != nil {
		return nil, g.getErr
	}
	v, ok := g.m[key]
	if !ok {
		return nil, ErrSecretNotFound
	}
	return []byte(v), nil
}

func (g *gatedSecrets) Set(key string, value []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sets[key]++
	if g.setErr != nil {
		return g.setErr
	}
	g.m[key] = string(value)
	return nil
}

func (g *gatedSecrets) Delete(key string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.m, key)
	return nil
}

func (g *gatedSecrets) count(m map[string]int, key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return m[key]
}

func (g *gatedSecrets) fail(get, set error) {
	g.mu.Lock()
	g.getErr, g.setErr = get, set
	g.mu.Unlock()
}

// within runs fn and fails when it has not returned in a second: the
// call blocked on something it must not wait for.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s blocked", what)
	}
}

// TestSecretReadsDoNotBlockTheUIOrLoop: while the keychain shows its
// prompt the cheap reads return at once, and a refused read is remembered
// instead of being asked again by every refresh.
func TestSecretReadsDoNotBlockTheUIOrLoop(t *testing.T) {
	h := newHarness(t, nil)
	ks := newGatedSecrets()
	ks.fail(errors.New("user refused access"), nil)
	h.s.cs.secrets = ks

	within(t, "WebDAVInfo", func() { h.s.WebDAVInfo() })
	within(t, "FeedbackInfo", func() { h.s.FeedbackInfo() })
	if !waitFor(func() bool {
		return ks.count(ks.gets, webdavSecretKey) == 1 && ks.count(ks.gets, feedbackTokenKey) == 1
	}) {
		t.Fatal("the lazy reads did not start")
	}
	// the prompt is still up: every refresh returns, none asks again
	for i := 0; i < 5; i++ {
		within(t, "WebDAVInfo during the prompt", func() { h.s.WebDAVInfo() })
		within(t, "FeedbackInfo during the prompt", func() { h.s.FeedbackInfo() })
		within(t, "ConfigSyncStatus during the prompt", func() { h.s.ConfigSyncStatus() })
	}
	close(ks.gate) // the user refuses
	failed := func(sl *secretSlot) bool {
		sl.mu.Lock()
		defer sl.mu.Unlock()
		return sl.err != nil && sl.reading == nil
	}
	if !waitFor(func() bool { return failed(h.s.webdavSlot()) && failed(h.s.tokenSlot()) }) {
		t.Fatal("the refused reads were not recorded")
	}
	// The read's own TopicSettings refreshes the views, which ask again:
	// that must not start another read.
	for i := 0; i < 20; i++ {
		h.s.WebDAVInfo()
		h.s.FeedbackInfo()
		h.s.em.emit(TopicSettings)
	}
	time.Sleep(50 * time.Millisecond)
	if n, m := ks.count(ks.gets, webdavSecretKey), ks.count(ks.gets, feedbackTokenKey); n != 1 || m != 1 {
		t.Fatalf("refused reads were repeated: webdav %d, token %d", n, m)
	}
	// automatic work gets the remembered answer, without asking
	if _, err := h.s.loadPassword(); err == nil {
		t.Fatal("loadPassword after a refusal: no error")
	}
	if _, err := h.s.readSecret(h.s.tokenSlot(), false); err == nil {
		t.Fatal("token after a refusal: no error")
	}
	if n := ks.count(ks.gets, webdavSecretKey); n != 1 {
		t.Fatalf("automatic work asked the keychain again (%d reads)", n)
	}
	// what the user starts asks again, and works once access is given
	if err := h.s.SetWebDAVPassword("pw"); err == nil {
		t.Fatal("setting the password while the keychain refuses: no error")
	}
	if n := ks.count(ks.gets, webdavSecretKey); n != 2 {
		t.Fatalf("a user's action did not ask again (%d reads)", n)
	}
	ks.fail(nil, nil)
	if err := h.s.SetWebDAVPassword("pw"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetFeedbackToken(" tok "); err != nil {
		t.Fatal(err)
	}
	info := h.s.WebDAVInfo()
	if !info.HasPassword || info.PasswordStore != model.PasswordStoreKeychain || !h.s.FeedbackInfo().TokenSet {
		t.Fatalf("info = %+v", info)
	}
	// the service's injected store is used, not the file fallback
	ks.mu.Lock()
	pw, tok := ks.m[webdavSecretKey], ks.m[feedbackTokenKey]
	ks.mu.Unlock()
	if pw != "pw" || tok != "tok" {
		t.Fatalf("keychain: password %q, token %q", pw, tok)
	}
	for _, f := range []string{webdavSecretFile, feedbackTokenFile} {
		if _, err := os.Stat(filepath.Join(h.dir, f)); !os.IsNotExist(err) {
			t.Fatalf("%s written beside a working keychain: %v", f, err)
		}
	}
}

// TestSecretSlotSharesOneRead: callers arriving during a read wait for
// it and share its answer.
func TestSecretSlotSharesOneRead(t *testing.T) {
	h := newHarness(t, nil)
	ks := newGatedSecrets()
	ks.m[webdavSecretKey] = "s3cret"
	h.s.cs.secrets = ks
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = h.s.readSecret(h.s.webdavSlot(), i%2 == 0)
		}(i)
	}
	if !waitFor(func() bool { return ks.count(ks.gets, webdavSecretKey) == 1 }) {
		t.Fatal("no read started")
	}
	time.Sleep(20 * time.Millisecond)
	close(ks.gate)
	wg.Wait()
	for i, v := range got {
		if v != "s3cret" {
			t.Fatalf("caller %d got %q", i, v)
		}
	}
	if n := ks.count(ks.gets, webdavSecretKey); n != 1 {
		t.Fatalf("%d keychain reads for one secret", n)
	}
}

// TestLinkSecretFailureIsRemembered: a link the keychain refused is not
// written again with every rewrite of the sync file.
func TestLinkSecretFailureIsRemembered(t *testing.T) {
	h := newHarness(t, nil)
	ks := newGatedSecrets()
	close(ks.gate)
	ks.fail(nil, errors.New("keychain locked"))
	h.s.cs.secrets = ks
	subs := h.s.d.store.List()
	key := syncfile.SubscriptionKey(subs[0].ID)
	if err := h.s.storeLinkSecrets(subs); err == nil {
		t.Fatal("the first failure is not reported")
	}
	for i := 0; i < 5; i++ {
		if err := h.s.storeLinkSecrets(subs); err != nil {
			t.Fatalf("a remembered failure reported again: %v", err)
		}
	}
	if n := ks.count(ks.sets, key); n != 1 {
		t.Fatalf("%d keychain writes for one refused link", n)
	}
	// a changed link is another secret: tried at once
	subs[0].URL = "https://example.com/other"
	_ = h.s.storeLinkSecrets(subs)
	if n := ks.count(ks.sets, key); n != 2 {
		t.Fatalf("a changed link was not tried (%d writes)", n)
	}
	// and after the pause the old failure is tried again
	h.s.cs.links.mu.Lock()
	f := h.s.cs.links.failed[key]
	f.at = f.at.Add(-2 * linkRetry)
	h.s.cs.links.failed[key] = f
	h.s.cs.links.mu.Unlock()
	ks.fail(nil, nil)
	if err := h.s.storeLinkSecrets(subs); err != nil {
		t.Fatal(err)
	}
	if ks.m[key] != "https://example.com/other" {
		t.Fatalf("keychain = %v", ks.m)
	}
}

// ---- configuration sync: a refused version is never written over ----

func syncFileBytes(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, syncfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// retryNow makes the held version's retry due.
func retryNow(h *harness) {
	h.s.cs.mu.Lock()
	if !h.s.cs.held.retryAt.IsZero() {
		h.s.cs.held.retryAt = time.Now().Add(-time.Second)
	}
	h.s.cs.mu.Unlock()
}

func localChange(t *testing.T, h *harness, payload string) {
	t.Helper()
	if _, err := h.s.SaveCustomRule(model.CustomRule{Type: "DOMAIN", Payload: payload, Target: "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	markDirty(h)
}

// TestSyncRefusedVersionIsKeptAndRetried: a version refused for a reason
// of this machine (its port is taken here) stays in the folder whatever
// changes locally, and loads once the reason is gone.
func TestSyncRefusedVersionIsKeptAndRetried(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, syncfile.FileName)
	h.taken[7999] = true
	ext := writeExternal(t, dir, func(d *syncfile.Doc) {
		d.Settings.MixedPort = 7999
		d.Settings.LogLevel = "warning"
		d.Subscriptions = append(d.Subscriptions, syncfile.Subscription{ID: "other001", Name: "另一台的订阅", URL: "https://b.example/sub"})
	})
	step(h)
	st := h.s.ConfigSyncStatus()
	if st.State != model.SyncStateError || !strings.Contains(st.Error, "7999") {
		t.Fatalf("status = %+v", st)
	}
	if h.s.knownHash(path) == syncfile.Hash(ext) {
		t.Fatal("a refused version was recorded as this machine's own")
	}
	held, ok := h.s.syncHeld(path)
	if !ok || held.retryAt.IsZero() || held.foreign {
		t.Fatalf("held = %+v, %v", held, ok)
	}

	// local changes, again and again: the other device's file stays
	for i := 0; i < 3; i++ {
		localChange(t, h, fmt.Sprintf("local%d.example", i))
		set := h.s.Settings()
		set.AllowLAN = !set.AllowLAN
		if err := h.s.SaveSettings(set); err != nil {
			t.Fatal(err)
		}
		markDirty(h)
		step(h)
		if got := syncFileBytes(t, dir); !bytes.Equal(got, ext) {
			t.Fatalf("the refused version was written over:\n%s", got)
		}
	}
	if h.s.Settings().MixedPort == 7999 || len(h.store.List()) != 1 {
		t.Fatal("a refused version was partly loaded")
	}
	// the same content is not loaded every second…
	validated := len(h.core.validated)
	step(h)
	if len(h.core.validated) != validated {
		t.Fatal("the refused version was tried again before its retry was due")
	}
	// …but again when its time comes, and then it loads
	delete(h.taken, 7999)
	retryNow(h)
	step(h)
	st = h.s.ConfigSyncStatus()
	if st.State != model.SyncStateOK || st.Error != "" || h.s.Settings().MixedPort != 7999 || h.s.Settings().LogLevel != "warning" {
		t.Fatalf("not loaded on retry: %+v", st)
	}
	if _, ok := h.store.Get("other001"); !ok {
		t.Fatal("the other device's subscription was not loaded")
	}
	if _, held := h.s.syncHeld(path); held {
		t.Fatal("still held after loading")
	}
	// from here on local changes are written again
	localChange(t, h, "after.example")
	step(h)
	if doc := readSync(t, dir); len(doc.CustomRules) != 1 || doc.CustomRules[0].Payload != "after.example" || len(doc.Subscriptions) != 2 {
		t.Fatalf("local change not written after the load: %+v", doc.CustomRules)
	}
}

// TestSyncRetryBacksOff: every failed retry waits longer.
func TestSyncRetryBacksOff(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, syncfile.FileName)
	h.taken[7999] = true
	writeExternal(t, dir, func(d *syncfile.Doc) { d.Settings.MixedPort = 7999 })
	var waits []time.Duration
	for i := 0; i < 8; i++ {
		before := time.Now()
		retryNow(h)
		step(h)
		held, ok := h.s.syncHeld(path)
		if !ok {
			t.Fatalf("round %d: not held", i)
		}
		waits = append(waits, held.retryAt.Sub(before).Round(time.Minute))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if waits[i] != w*time.Minute {
			t.Fatalf("waits = %v, want %v minutes", waits, want)
		}
	}
	// one notice for the same refusal, however often it is retried
	n := 0
	for _, nt := range h.s.TakeNotices() {
		if nt.Kind == "error" && strings.Contains(nt.Detail, "7999") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d notices for one refusal", n)
	}
}

// TestSyncInvalidVersionIsKept: a file that is simply wrong is not tried
// again, and not written over either.
func TestSyncInvalidVersionIsKept(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, syncfile.FileName)
	bad := []byte(strings.Replace(string(syncFileBytes(t, dir)), "mixedPort: 7890", "mixedPort: lots", 1))
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	step(h)
	held, ok := h.s.syncHeld(path)
	if !ok || !held.retryAt.IsZero() {
		t.Fatalf("held = %+v, %v", held, ok)
	}
	localChange(t, h, "local.example")
	step(h)
	step(h)
	if got := syncFileBytes(t, dir); !bytes.Equal(got, bad) {
		t.Fatalf("the invalid version was written over:\n%s", got)
	}
	if st := h.s.ConfigSyncStatus(); st.State != model.SyncStateError || st.ErrorLine == 0 {
		t.Fatalf("status = %+v", st)
	}
	// rewriting the file needs the user's word…
	if err := h.s.SetSyncPassphrase(sealPass); err == nil {
		t.Fatal("a passphrase was set over a held file")
	}
	if got := syncFileBytes(t, dir); !bytes.Equal(got, bad) {
		t.Fatal("setting a passphrase wrote over the held version")
	}
	// …which keeps a copy and then writes this machine's configuration
	if err := h.s.SetConfigSync(model.SyncOff, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); !errors.Is(err, ErrConfigSyncChoice) {
		t.Fatalf("turning sync on beside the refused file: %v", err)
	}
	if err := h.s.SetConfigSync(model.SyncFolder, dir, model.SyncUseLocal); err != nil {
		t.Fatal(err)
	}
	if doc := readSync(t, dir); len(doc.CustomRules) != 1 || doc.CustomRules[0].Payload != "local.example" {
		t.Fatalf("this machine's configuration not written: %+v", doc)
	}
	kept, _ := filepath.Glob(filepath.Join(h.dir, syncBackupDir, "*"))
	found := false
	for _, k := range kept {
		if data, _ := os.ReadFile(k); bytes.Equal(data, bad) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no copy of the replaced file in %v", kept)
	}
}

// TestSyncForeignFileIsNeverWritten: a file in a newer format, or sealed
// in a way this build does not know, is not written over — not by a
// local change, and not on request either.
func TestSyncForeignFileIsNeverWritten(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"newer format": func(s string) string { return strings.Replace(s, "format: 1", "format: 2", 1) },
		"unknown kdf": func(s string) string {
			return strings.Replace(s, "format: 1", "format: 1\nencryption:\n  kdf: scrypt-next\n  salt: AAAAAAAAAAAAAAAAAAAAAA==\n  check: enc:v2:AAAA", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, dir := syncHarness(t)
			if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, syncfile.FileName)
			foreign := []byte(edit(string(syncFileBytes(t, dir))))
			if !syncfile.Foreign(foreign) {
				t.Fatalf("not recognised as foreign:\n%s", foreign)
			}
			if err := os.WriteFile(path, foreign, 0o600); err != nil {
				t.Fatal(err)
			}
			step(h)
			held, ok := h.s.syncHeld(path)
			if !ok || !held.foreign {
				t.Fatalf("held = %+v, %v", held, ok)
			}
			if st := h.s.ConfigSyncStatus(); st.State != model.SyncStateError {
				t.Fatalf("status = %+v", st)
			}
			for i := 0; i < 3; i++ {
				localChange(t, h, fmt.Sprintf("l%d.example", i))
				step(h)
			}
			if err := h.s.SetSyncPassphrase(""); err == nil {
				t.Fatal("turning sealing off over a foreign file: no error")
			}
			if err := h.s.SetConfigSync(model.SyncOff, "", ""); err != nil {
				t.Fatal(err)
			}
			if err := h.s.SetConfigSync(model.SyncFolder, dir, model.SyncUseLocal); !errors.Is(err, errSyncForeign) {
				t.Fatalf("overwriting a foreign file on request: %v", err)
			}
			if got := syncFileBytes(t, dir); !bytes.Equal(got, foreign) {
				t.Fatalf("the foreign file was written over:\n%s", got)
			}
			if h.s.Settings().Sync.Mode != model.SyncOff {
				t.Fatal("sync turned on although the choice was refused")
			}
		})
	}
}

// TestSyncLoadIsAllOrNothing: when a step after the checks fails, what
// was already stored is put back, and "not loaded" is true.
func TestSyncLoadIsAllOrNothing(t *testing.T) {
	for name, edit := range map[string]func(d *syncfile.Doc){
		// the store alone changes: the reload after storing fails
		"store only": func(d *syncfile.Doc) {},
		// settings that need a reload: SaveSettings fails and reverts
		"with settings": func(d *syncfile.Doc) {
			d.Settings.DNS.RespectRules = !d.Settings.DNS.RespectRules
			d.Settings.LogLevel = "error"
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, dir := syncHarness(t)
			if err := h.s.StartCore(); err != nil {
				t.Fatal(err)
			}
			// what this machine has before: one rule, one override the
			// subscription selects
			if _, err := h.s.SaveCustomRule(model.CustomRule{Type: "DOMAIN", Payload: "mine.example", Target: "DIRECT"}); err != nil {
				t.Fatal(err)
			}
			mine, err := h.s.SaveOverrideEntry(model.Override{Name: "本机", Enabled: true, Text: "ipv6: true\n"})
			if err != nil {
				t.Fatal(err)
			}
			sub := h.s.Subscriptions()[0]
			sub.Overrides = []string{mine.ID}
			if err := h.s.EditSubscription(sub); err != nil {
				t.Fatal(err)
			}
			if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
				t.Fatal(err)
			}
			before := h.s.snapshotLocal()

			h.ctrl.mu.Lock()
			h.ctrl.reloadFn = func(string) error { return errors.New("reload refused") }
			h.ctrl.mu.Unlock()
			ext := writeExternal(t, dir, func(d *syncfile.Doc) {
				d.CustomRules = []syncfile.Rule{{Type: "DOMAIN", Payload: "theirs.example", Target: "DIRECT"}}
				d.Overrides = []syncfile.Override{{ID: "theirs1", Name: "对方", Enabled: true, Global: true, Text: "ipv6: false\n"}}
				d.Subscriptions[0].Name = "改了名"
				d.Subscriptions[0].Overrides = nil
				d.Subscriptions = append(d.Subscriptions, syncfile.Subscription{ID: "other001", Name: "另一台的订阅", URL: "https://b.example/sub"})
				edit(d)
			})
			step(h)

			st := h.s.ConfigSyncStatus()
			if st.State != model.SyncStateError || !strings.Contains(st.Error, "reload refused") {
				t.Fatalf("status = %+v", st)
			}
			after := h.s.snapshotLocal()
			if !rulesEqual(before.rules, after.rules) || !reflect.DeepEqual(before.ovs, after.ovs) {
				t.Fatalf("store not put back:\nrules %+v\noverrides %+v", after.rules, after.ovs)
			}
			if len(after.subs) != 1 || !subEqual(before.subs[0], after.subs[0]) {
				t.Fatalf("subscriptions not put back: %+v", after.subs)
			}
			if !reflect.DeepEqual(before.set.DNS, after.set.DNS) || before.set.LogLevel != after.set.LogLevel {
				t.Fatalf("settings not put back: %+v", after.set)
			}
			// the mixed state is not written to the file a second later
			markDirty(h)
			step(h)
			if got := syncFileBytes(t, dir); !bytes.Equal(got, ext) {
				t.Fatalf("the file was written over after a failed load:\n%s", got)
			}
			if held, ok := h.s.syncHeld(filepath.Join(dir, syncfile.FileName)); !ok || held.retryAt.IsZero() {
				t.Fatalf("a failure of this machine is not retried: %+v", held)
			}

			// the reason gone, the retry loads all of it
			h.ctrl.mu.Lock()
			h.ctrl.reloadFn = nil
			h.ctrl.mu.Unlock()
			retryNow(h)
			step(h)
			if st := h.s.ConfigSyncStatus(); st.State != model.SyncStateOK {
				t.Fatalf("retry: %+v", st)
			}
			got := h.s.snapshotLocal()
			if len(got.rules) != 1 || got.rules[0].Payload != "theirs.example" || len(got.ovs) != 1 || got.ovs[0].ID != "theirs1" || len(got.subs) != 2 {
				t.Fatalf("retry did not load everything: %+v", got)
			}
		})
	}
}

// TestSyncDropsSubscriptionsLast: a subscription the file no longer has
// is still there when the load fails.
func TestSyncDropsSubscriptionsLast(t *testing.T) {
	h, ks, dir := syncHarness(t)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	extra, _ := h.store.Put(model.Subscription{ID: "extra001", Name: "第二个", Source: urlSubscriptionK, URL: "https://c.example/sub"})
	_ = h.store.SetContent(extra.ID, []byte("proxies: []\n"))
	h.s.refreshStore(TopicSubs)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	h.ctrl.mu.Lock()
	h.ctrl.reloadFn = func(string) error { return errors.New("reload refused") }
	h.ctrl.mu.Unlock()
	writeExternal(t, dir, func(d *syncfile.Doc) {
		var keep []syncfile.Subscription
		for _, s := range d.Subscriptions {
			if s.ID != extra.ID {
				keep = append(keep, s)
			}
		}
		d.Subscriptions = keep
		d.CustomRules = []syncfile.Rule{{Type: "DOMAIN", Payload: "x.example", Target: "DIRECT"}}
	})
	step(h)
	if _, ok := h.store.Get(extra.ID); !ok {
		t.Fatal("a subscription was deleted by a load that failed")
	}
	if data, err := h.store.Content(extra.ID); err != nil || len(data) == 0 {
		t.Fatalf("its content is gone: %v", err)
	}
	if ks.get(syncfile.SubscriptionKey(extra.ID)) == "" {
		t.Fatal("its keychain link is gone")
	}
	h.ctrl.mu.Lock()
	h.ctrl.reloadFn = nil
	h.ctrl.mu.Unlock()
	retryNow(h)
	step(h)
	if _, ok := h.store.Get(extra.ID); ok {
		t.Fatal("the subscription the file dropped is still there after loading")
	}
}

// TestSettingsWarningCountsAsApplied: SaveSettings' "saved, but the
// system proxy did not follow" is told apart from a refusal.
func TestSettingsWarningCountsAsApplied(t *testing.T) {
	var w *settingsWarning
	err := error(&settingsWarning{errors.New("x")})
	if !errors.As(fmt.Errorf("wrapped: %w", err), &w) {
		t.Fatal("settingsWarning not found through a wrap")
	}
	yes := func(err error) bool { r, _ := retryable(err); return r }
	if yes(errors.New("plain")) || !yes(&retryError{err: errors.New("x")}) || !yes(fmt.Errorf("w: %w", &retryError{err: errors.New("x")})) {
		t.Fatal("retryable")
	}
	// a document error keeps its line through the retry wrapper
	var de *docError
	if !errors.As(error(&retryError{err: &docError{"settings", errors.New("port")}}), &de) || de.path != "settings" {
		t.Fatal("docError lost in retryError")
	}
}

// TestSyncReadsAnUnchangedFileOnce: a file that has not changed (and is
// old enough for its time to be trusted) is not read every second.
func TestSyncReadsAnUnchangedFileOnce(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, syncfile.FileName)
	readAt := func() time.Time {
		h.s.cs.mu.Lock()
		defer h.s.cs.mu.Unlock()
		return h.s.cs.readAt
	}
	step(h)
	if !readAt().IsZero() {
		t.Fatal("a file written a moment ago was trusted by its time")
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	step(h) // the new time is a change: read once more
	first := readAt()
	if first.IsZero() {
		t.Fatal("an old unchanged file is still read every step")
	}
	step(h)
	step(h)
	if !readAt().Equal(first) {
		t.Fatal("the unchanged file was read again")
	}
	// local changes are still written, and an external change still loads
	localChange(t, h, "w.example")
	step(h)
	if doc := readSync(t, dir); len(doc.CustomRules) != 1 {
		t.Fatal("local change not written")
	}
	writeExternal(t, dir, func(d *syncfile.Doc) { d.Settings.LogLevel = "debug" })
	step(h)
	if h.s.Settings().LogLevel != "debug" {
		t.Fatal("external change not loaded")
	}
	// the periodic full check
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	step(h)
	h.s.cs.mu.Lock()
	h.s.cs.readAt = h.s.cs.readAt.Add(-2 * syncRecheck)
	stale := h.s.cs.readAt
	h.s.cs.mu.Unlock()
	step(h)
	if !readAt().After(stale) {
		t.Fatal("no full check after syncRecheck")
	}
}

// ---- sealing ----

// TestSealLockedDeviceCannotTurnOff: a device that cannot open the
// file's secrets cannot turn sealing off (and take every other device's
// sealed links out of the file).
func TestSealLockedDeviceCannotTurnOff(t *testing.T) {
	_, _, dir := sealedDevice(t)
	b, _ := secondDevice(t, dir)
	if st := b.s.ConfigSyncStatus(); !st.Encrypted || !st.Locked {
		t.Fatalf("device B: %+v", st)
	}
	before := syncBytes(t, dir)
	if err := b.s.SetSyncPassphrase(""); !errors.Is(err, ErrSyncLocked) {
		t.Fatalf("turning sealing off while locked: %v", err)
	}
	if err := b.s.SetSyncPassphrase(sealPass2); !errors.Is(err, ErrSyncLocked) {
		t.Fatalf("changing the passphrase while locked: %v", err)
	}
	markDirty(b)
	step(b)
	after := syncBytes(t, dir)
	if doc := readSync(t, dir); doc.Encryption == nil {
		t.Fatalf("the file lost its encryption block:\n%s", after)
	}
	if n := strings.Count(before, syncfile.SealedPrefix); n < 3 || n != strings.Count(after, syncfile.SealedPrefix) {
		t.Fatalf("sealed values changed:\n%s\n---\n%s", before, after)
	}
	// unlocked, it may
	if err := b.s.UnlockSync(sealPass); err != nil {
		t.Fatal(err)
	}
	if err := b.s.SetSyncPassphrase(""); err != nil {
		t.Fatal(err)
	}
	if doc := readSync(t, dir); doc.Encryption != nil {
		t.Fatal("still sealed after an unlocked device turned it off")
	}
	if subURL(b, "cafe0001") != sealLink {
		t.Fatal("the link was lost when sealing was turned off")
	}
}

// TestSyncUseLocalKeepsSealing: choosing this machine's configuration
// over a sealed file is not a way to turn the passphrase off.
func TestSyncUseLocalKeepsSealing(t *testing.T) {
	_, _, dir := sealedDevice(t)
	b, _, _ := syncHarness(t)
	if err := b.s.SetConfigSync(model.SyncFolder, dir, model.SyncUseLocal); err != nil {
		t.Fatal(err)
	}
	doc := readSync(t, dir)
	if doc.Encryption == nil {
		t.Fatal("choosing this machine's configuration turned sealing off")
	}
	if st := b.s.ConfigSyncStatus(); !st.Encrypted || !st.Locked {
		t.Fatalf("status = %+v", st)
	}
}

// ---- WebDAV ----

func TestBackupNameValidation(t *testing.T) {
	ok := []string{
		"lanflow-backup-20260102-150405.yaml",
		"lanflow-backup-20260102-150405.json",
		backupName(time.Now()),
	}
	bad := []string{
		"", "lanflow-backup-.yaml", "backup.yaml",
		"lanflow-backup-../../../../abc.yaml", // 15 characters between prefix and suffix
		"lanflow-backup-..%2f..%2f..%2f.yaml",
		`lanflow-backup-..\..\..\..\ab.yaml`,
		"lanflow-backup-20260102/150405.yaml",
		"lanflow-backup-2026010x-150405.yaml",
		"lanflow-backup-20261302-150405.yaml", // month 13
		"lanflow-backup-20260102-150405.yaml/..",
		"../lanflow-backup-20260102-150405.yaml",
		"lanflow-backup-20260102-150405.yml",
		" lanflow-backup-20260102-150405.yaml",
	}
	for _, n := range ok {
		if !isBackupName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range bad {
		if isBackupName(n) {
			t.Errorf("%q accepted", n)
		}
	}
	// the API refuses them before anything is downloaded, written or deleted
	h, dav := webdavHarness(t)
	for _, n := range bad {
		if _, err := h.s.RestoreBackup(n); err == nil {
			t.Errorf("RestoreBackup(%q) went ahead", n)
		}
		if err := h.s.DeleteBackup(n); err == nil {
			t.Errorf("DeleteBackup(%q) went ahead", n)
		}
	}
	if n := len(dav.names()); n != 0 {
		t.Fatalf("server touched: %v", dav.names())
	}
}

func TestWebDAVPlainHTTPOnlyOnLocalNetworks(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://dav.example.com/dav":   true,
		"http://127.0.0.1:8080/dav":     true,
		"http://localhost/dav":          true,
		"http://[::1]:8080/":            true,
		"http://192.168.1.2:5005/":      true,
		"http://10.0.0.8/":              true,
		"http://100.100.1.2/":           true, // overlay network
		"http://nas/dav":                true,
		"http://nas.local:5005/":        true,
		"http://nas.lan/":               true,
		"http://box.tail1234.ts.net/":   true,
		"http://dav.example.com/dav":    false,
		"http://8.8.8.8/":               false,
		"http://[2001:db8::1]/":         false,
		"http://local.example.com/":     false,
		"http://evil.example/?x=.local": false,
		"ftp://nas/":                    false,
		"nas/dav":                       false,
	} {
		_, err := newWebDAVClient(raw, "u", "p", http.DefaultClient)
		if (err == nil) != ok {
			t.Errorf("newWebDAVClient(%q): %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestWebDAVOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"":                               "",
		"  ":                             "",
		"not a url":                      "",
		"https://Dav.Example.com/a/b":    "https://dav.example.com",
		"HTTPS://dav.example.com:8443/x": "https://dav.example.com:8443",
		"http://127.0.0.1:8080/dav/":     "http://127.0.0.1:8080",
	} {
		if got := webdavOrigin(raw); got != want {
			t.Errorf("webdavOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestAutoBackupWaitsForConfirmedServer: a WebDAV server that arrived
// through sync or an import gets no automatic backup (the password and
// every subscription link) until the user confirms it on this machine.
func TestAutoBackupWaitsForConfirmedServer(t *testing.T) {
	h, dav := webdavHarness(t) // the server the user set up here
	set := h.s.Settings()
	set.WebDAV.AutoBackup = model.WebDAVDaily
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	// another server, named by a sync file
	other := newDAVFake("alice", "s3cret")
	var otherHits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherHits++
		mu.Unlock()
		other.ServeHTTP(w, r)
	}))
	defer srv.Close()
	hits := func() int { mu.Lock(); defer mu.Unlock(); return otherHits }
	remote := h.s.Settings()
	remote.WebDAV.URL = srv.URL + "/dav"
	if err := h.s.saveSettingsFrom(remote, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		h.s.webdavTick()
	}
	time.Sleep(100 * time.Millisecond)
	if hits() != 0 || len(dav.names()) != 0 {
		t.Fatalf("an automatic backup went out before the server was confirmed (%d requests)", hits())
	}
	info := h.s.WebDAVInfo()
	if info.LastError != errWebDAVUnconfirmed.Error() {
		t.Fatalf("WebDAVInfo does not say why: %+v", info)
	}
	n := 0
	for _, nt := range h.s.TakeNotices() {
		if nt.Detail == errWebDAVUnconfirmed.Error() {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d notices for one unconfirmed server", n)
	}
	// a restart does not forget which server was confirmed
	o := &h.s.cs.origin
	o.mu.Lock()
	o.loaded, o.known, o.origin, o.notified = false, false, "", ""
	o.mu.Unlock()
	h.s.webdavTick()
	time.Sleep(50 * time.Millisecond)
	if hits() != 0 {
		t.Fatal("the confirmation did not survive a restart")
	}
	// the user tests the connection: confirmed, and backups resume
	if err := h.s.TestWebDAV(); err != nil {
		t.Fatal(err)
	}
	if got := h.s.WebDAVInfo().LastError; got != "" {
		t.Fatalf("LastError after confirming: %q", got)
	}
	h.s.webdavTick()
	if !waitFor(func() bool { return len(other.names()) == 1 }) {
		t.Fatalf("no automatic backup after the confirmation: %v", other.names())
	}
}

// TestLocalWebDAVChangeNeedsNoConfirmation: an address the user types on
// this machine is confirmed by typing it.
func TestLocalWebDAVChangeNeedsNoConfirmation(t *testing.T) {
	h, _ := webdavHarness(t)
	other := newDAVFake("alice", "s3cret")
	srv := httptest.NewServer(other)
	defer srv.Close()
	set := h.s.Settings()
	set.WebDAV.URL = srv.URL + "/dav"
	set.WebDAV.AutoBackup = model.WebDAVDaily
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.webdavTick()
	if !waitFor(func() bool { return len(other.names()) == 1 }) {
		t.Fatal("no automatic backup to the server the user set here")
	}
}

// ---- subscriptions and overrides: nothing stale is written back ----

// TestUpdateKeepsEditsMadeDuringDownload: the record read before a
// download is not written back whole after it.
func TestUpdateKeepsEditsMadeDuringDownload(t *testing.T) {
	h := newHarness(t, nil)
	ov, err := h.s.SaveOverrideEntry(model.Override{Name: "选中的", Enabled: true, Text: "ipv6: true\n"})
	if err != nil {
		t.Fatal(err)
	}
	stale := h.s.Subscriptions()[0] // what UpdateSubscription read before downloading
	stale.Updating = false

	// while the download runs, the user edits the subscription
	edited := stale
	edited.Name = "新名字"
	edited.UserAgent = "clash-verge/v2"
	edited.AutoUpdateHours = 12
	edited.Overrides = []string{ov.ID}
	if err := h.s.EditSubscription(edited); err != nil {
		t.Fatal(err)
	}
	h.prof.mu.Lock()
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [{name: n1}]\n"), Upload: 1, Download: 2, Total: 100, UpdateInterval: 6}
	h.prof.mu.Unlock()
	if err := h.s.updateOne(stale); err != nil {
		t.Fatal(err)
	}
	got, _ := h.store.Get(stale.ID)
	if got.Name != "新名字" || got.UserAgent != "clash-verge/v2" || got.AutoUpdateHours != 12 || !equalStrings(got.Overrides, []string{ov.ID}) {
		t.Fatalf("the edit was undone by the update: %+v", got)
	}
	if got.Total != 100 || got.Download != 2 || got.UpdatedAt.IsZero() || got.LastError != "" {
		t.Fatalf("the download's result is missing: %+v", got)
	}
	if data, _ := h.store.Content(stale.ID); !strings.Contains(string(data), "n1") {
		t.Fatalf("content = %s", data)
	}

	// a failed download writes its error, and nothing else, onto the edit
	h.prof.mu.Lock()
	h.prof.fetchErr = errors.New("HTTP 502")
	h.prof.mu.Unlock()
	if err := h.s.updateOne(stale); err == nil {
		t.Fatal("no error")
	}
	got, _ = h.store.Get(stale.ID)
	if got.Name != "新名字" || !strings.Contains(got.LastError, "502") {
		t.Fatalf("after a failed update: %+v", got)
	}

	// the link changed meanwhile: what was downloaded from the old one is dropped
	h.prof.mu.Lock()
	h.prof.fetchErr = nil
	h.prof.fetched = profile.Fetched{Data: []byte("proxies: [{name: old-link}]\n")}
	h.prof.mu.Unlock()
	relinked := got
	relinked.URL = "https://example.com/another"
	if err := h.s.EditSubscription(relinked); err != nil {
		t.Fatal(err)
	}
	if err := h.s.updateOne(stale); !errors.Is(err, errSubChanged) {
		t.Fatalf("update from the old link: %v", err)
	}
	if data, _ := h.store.Content(stale.ID); strings.Contains(string(data), "old-link") {
		t.Fatal("content of the old link stored under the new one")
	}
	if got, _ := h.store.Get(stale.ID); got.URL != "https://example.com/another" {
		t.Fatalf("link rolled back: %+v", got)
	}

	// deleted meanwhile: not brought back
	gone, _ := h.store.Put(model.Subscription{Name: "要删的", Source: urlSubscriptionK, URL: "https://example.com/gone"})
	if err := h.store.Delete(gone.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.s.updateOne(gone); err == nil {
		t.Fatal("updating a deleted subscription: no error")
	}
	if _, ok := h.store.Get(gone.ID); ok {
		t.Fatal("a deleted subscription came back with its update")
	}
}

// TestOverrideDownloadDoesNotHoldOpMu: while a URL override downloads,
// operations that need opMu go through, and an edit made meanwhile stays.
func TestOverrideDownloadDoesNotHoldOpMu(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 8)
	var body atomicText
	body.set("log-level: warning\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-gate
		_, _ = w.Write([]byte(body.get()))
	}))
	defer srv.Close()
	release := func() { gate <- struct{}{} }
	h := newHarness(t, nil)

	type saved struct {
		o   model.Override
		err error
	}
	res := make(chan saved, 1)
	go func() {
		o, err := h.s.SaveOverrideEntry(model.Override{Name: "remote", Enabled: true, Global: true, Source: model.OverrideURL, URL: srv.URL + "/ov.yaml"})
		res <- saved{o, err}
	}()
	<-started
	within(t, "SaveSettings during an override download", func() {
		set := h.s.Settings()
		set.LogLevel = "debug"
		if err := h.s.SaveSettings(set); err != nil {
			t.Error(err)
		}
	})
	release()
	r := <-res
	if r.err != nil || r.o.Text != "log-level: warning\n" {
		t.Fatalf("saved = %+v, %v", r.o, r.err)
	}

	// update: the download runs unlocked, an edit made meanwhile stays
	body.set("log-level: error\n")
	done := make(chan error, 1)
	go func() { done <- h.s.UpdateOverrideEntry(r.o.ID) }()
	<-started
	within(t, "editing the entry during its update", func() {
		o := r.o
		o.Name, o.Enabled = "renamed", false
		if _, err := h.s.SaveOverrideEntry(o); err != nil {
			t.Error(err)
		}
	})
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := h.store.GetOverride(r.o.ID)
	if got.Name != "renamed" || got.Enabled || got.Text != "log-level: error\n" {
		t.Fatalf("after the update: %+v", got)
	}

	// the link changed during the download: the text of the old link is dropped
	go func() { done <- h.s.UpdateOverrideEntry(r.o.ID) }()
	<-started
	o := got
	o.URL = srv.URL + "/other.yaml" // Text is set: no download for this save
	if _, err := h.s.SaveOverrideEntry(o); err != nil {
		t.Fatal(err)
	}
	body.set("log-level: silent\n")
	release()
	if err := <-done; err == nil {
		t.Fatal("update finished for a link that was replaced meanwhile")
	}
	if got, _ := h.store.GetOverride(r.o.ID); got.Text != "log-level: error\n" || got.URL != srv.URL+"/other.yaml" {
		t.Fatalf("entry after the refused update: %+v", got)
	}
}

// ---- feedback: what a report leaves out ----

// settingsLists returns the Go field paths of every slice and map in t.
func settingsLists(t reflect.Type, path string, out *[]string) {
	switch t.Kind() {
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			p := t.Field(i).Name
			if path != "" {
				p = path + "." + p
			}
			settingsLists(t.Field(i).Type, p, out)
		}
	case reflect.Slice, reflect.Map, reflect.Array:
		*out = append(*out, path)
	case reflect.Pointer:
		settingsLists(t.Elem(), path, out)
	}
}

// droppedPath reports whether dropPrivateJSON drops the field at path
// (itself, or a parent of it).
func droppedPath(path string) bool {
	parts := strings.Split(path, ".")
	parent := ""
	for _, p := range parts {
		if privateUnder(parent, p) {
			return true
		}
		parent = p
	}
	return false
}

// TestFeedbackSettingsListsClassified: every list of model.Settings is
// either dropped from a report or named in reportKeptLists with a
// reason. A new list field fails here until someone decides.
func TestFeedbackSettingsListsClassified(t *testing.T) {
	var lists []string
	settingsLists(reflect.TypeOf(model.Settings{}), "", &lists)
	if len(lists) < 20 {
		t.Fatalf("found only %d lists in model.Settings", len(lists))
	}
	seen := map[string]bool{}
	for _, p := range lists {
		seen[p] = true
		_, kept := reportKeptLists[p]
		switch dropped := droppedPath(p); {
		case dropped && kept:
			t.Errorf("%s is in reportKeptLists but dropped by privateKeys", p)
		case !dropped && !kept:
			t.Errorf("%s is a list in model.Settings that a feedback report would carry: add it to privateKeys (feedback_redact.go) or, with a reason, to reportKeptLists", p)
		}
	}
	for p := range reportKeptLists {
		if !seen[p] {
			t.Errorf("reportKeptLists names %s, which is not a list in model.Settings", p)
		}
	}
}

// TestFeedbackSettingsDropPrivateValues: filled with marked values, the
// settings a report carries hold none of the private ones.
func TestFeedbackSettingsDropPrivateValues(t *testing.T) {
	set := defaultSettings()
	set.DNS.Policy = []model.KV{{Key: "+.corp.secret-domain.example", Value: "10.9.8.7"}}
	set.DNS.Hosts = []model.KV{{Key: "intranet.secret-domain.example", Value: "10.1.1.1"}}
	set.DNS.FakeIPFilter = []string{"*.secret-domain.example"}
	set.Bypass = []string{"secret-domain.example"}
	set.Sniffer.SkipDomain = []string{"skip.secret-domain.example"}
	set.Advanced.TUNRouteAddress = []string{"secret-route/24"}
	set.Advanced.TUNRouteExcludeAddress = []string{"secret-exclude/24"}
	set.Advanced.LANAllowedIPs = []string{"secret-lan/24"}
	set.Advanced.LANDisallowedIPs = []string{"secret-nolan/24"}
	set.Advanced.Authentication = []string{"user:secret-pass"}
	set.Advanced.SkipAuthPrefixes = []string{"secret-skip/8"}
	set.Advanced.DNSFallbackFilter.Domain = []string{"+.secret-domain.example"}
	set.Advanced.DNSFallbackFilter.IPCIDR = []string{"secret-cidr/8"}
	set.SSIDRules = []model.SSIDRule{{SSID: "secret-wifi"}}
	set.Sync = model.ConfigSyncSettings{Mode: model.SyncFolder, Dir: "/Users/secret-user/Sync"}
	set.CorePath = "/Users/secret-user/bin/mihomo"
	set.WebDAV = model.WebDAVSettings{URL: "https://dav.secret-domain.example/x", User: "secret-user"}
	set.ATrust.CorpDomains = []string{"secret-domain.example"}
	set.DNS.Nameserver = []string{"https://kept-resolver.example/dns-query"}
	data, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := string(dropPrivateJSON(data))
	if strings.Contains(out, "secret-") {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "secret-") {
				t.Errorf("a report would carry: %s", strings.TrimSpace(line))
			}
		}
	}
	if !strings.Contains(out, "kept-resolver.example") || !strings.Contains(out, `"`+model.SyncFolder+`"`) {
		t.Fatalf("what a report keeps is gone:\n%s", out)
	}
	if !strings.Contains(out, "omitted: 1 items") {
		t.Fatalf("no item counts:\n%s", out)
	}

	// the runtime config's spellings of the same lists
	y := "dns:\n  nameserver-policy:\n    '+.secret-domain.example': 10.0.0.1\n  proxy-server-nameserver-policy:\n    'secret-domain.example': 10.0.0.2\n" +
		"  fallback-filter:\n    geoip: true\n    domain: ['+.secret-domain.example']\n    ipcidr: ['secret-cidr/8']\n  nameserver: [kept-resolver.example]\n" +
		"lan-allowed-ips: ['secret-lan/24']\nskip-auth-prefixes: ['secret-skip/8']\ntun:\n  route-address: ['secret-route/24']\n  route-exclude-address: ['secret-exclude/24']\n" +
		"proxy-groups:\n  - {name: g, type: select, domain: kept-group-field}\n"
	yo := string(dropPrivateYAML([]byte(y)))
	if strings.Contains(yo, "secret-") {
		t.Errorf("runtime config keeps private lists:\n%s", yo)
	}
	if !strings.Contains(yo, "kept-resolver.example") || !strings.Contains(yo, "kept-group-field") || !strings.Contains(yo, "geoip: true") {
		t.Errorf("runtime config lost what it should keep:\n%s", yo)
	}
}

// ---- copySettings ----

// fillValue sets every field reachable from v to a non-zero value made
// from *n, with one element in every slice and map.
func fillValue(v reflect.Value, n *int) {
	*n++
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Unix(int64(1700000000+*n), 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fillValue(v.Field(i), n)
			}
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < s.Len(); i++ {
			fillValue(s.Index(i), n)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillValue(k, n)
		e := reflect.New(v.Type().Elem()).Elem()
		fillValue(e, n)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillValue(p.Elem(), n)
		v.Set(p)
	case reflect.String:
		v.SetString(fmt.Sprintf("v%d", *n))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(*n%100 + 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(*n%100 + 1))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(*n) + 0.5)
	}
}

// scribble overwrites, in place, everything v shares with another value:
// the elements of its slices, the entries of its maps, what its pointers
// point at. A deep copy's original does not notice.
func scribble(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			scribble(v.Field(i))
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			scribble(v.Index(i))
			if v.Index(i).CanSet() {
				v.Index(i).Set(reflect.Zero(v.Type().Elem()))
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			scribble(e) // reaches the arrays its slices share
			v.SetMapIndex(k, reflect.Zero(v.Type().Elem()))
		}
	case reflect.Pointer:
		if !v.IsNil() {
			scribble(v.Elem())
			if v.Elem().CanSet() {
				v.Elem().Set(reflect.Zero(v.Type().Elem()))
			}
		}
	}
}

func filledSettings() model.Settings {
	var set model.Settings
	n := 0
	fillValue(reflect.ValueOf(&set).Elem(), &n)
	return set
}

// TestCopySettingsIsDeep: changing a copy never reaches the original.
// copySettings is written by hand, field by field; a slice or map field
// added to model.Settings (or to a struct inside it) without a line
// there shares its backing array outside Service.mu, and fails here.
func TestCopySettingsIsDeep(t *testing.T) {
	orig := filledSettings()
	want := filledSettings()
	if !reflect.DeepEqual(orig, want) {
		t.Fatal("fillValue is not deterministic")
	}
	// the check has teeth: a plain assignment is caught
	shallowOf := filledSettings()
	shallow := shallowOf
	scribble(reflect.ValueOf(&shallow).Elem())
	if reflect.DeepEqual(shallowOf, want) {
		t.Fatal("scribble does not reach what a shallow copy shares")
	}
	cp := copySettings(orig)
	if !reflect.DeepEqual(cp, orig) {
		t.Fatal("the copy differs from the original")
	}
	scribble(reflect.ValueOf(&cp).Elem())
	if reflect.DeepEqual(orig, want) {
		return
	}
	// name the fields that are shared
	var lists []string
	settingsLists(reflect.TypeOf(model.Settings{}), "", &lists)
	for _, p := range lists {
		a, b := reflect.ValueOf(orig), reflect.ValueOf(want)
		for _, name := range strings.Split(p, ".") {
			a, b = a.FieldByName(name), b.FieldByName(name)
		}
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			t.Errorf("copySettings shares %s with the original", p)
		}
	}
	if !t.Failed() {
		t.Error("copySettings shares something with the original (a list inside a list element)")
	}
}

// TestSettingsClonesAreDeep: the same for the Clone methods of the
// settings' parts.
func TestSettingsClonesAreDeep(t *testing.T) {
	check := func(name string, orig, want, cp any, cpPtr reflect.Value) {
		t.Helper()
		if !reflect.DeepEqual(cp, orig) {
			t.Errorf("%s.Clone differs from the original", name)
		}
		scribble(cpPtr.Elem())
		if !reflect.DeepEqual(orig, want) {
			t.Errorf("%s.Clone shares a slice or map with the original", name)
		}
	}
	a, aw := filledSettings().Advanced, filledSettings().Advanced
	ac := a.Clone()
	check("AdvancedSettings", a, aw, ac, reflect.ValueOf(&ac))
	v, vw := filledSettings().ATrust, filledSettings().ATrust
	vc := v.Clone()
	check("ATrustConfig", v, vw, vc, reflect.ValueOf(&vc))
	u, uw := filledSettings().UI, filledSettings().UI
	uc := copyUIState(u)
	check("UIState", u, uw, uc, reflect.ValueOf(&uc))
}

// ---- 允许局域网 and a subscription's own listeners ----

// TestAllowLANReloadsWhenSubscriptionListens: PATCH /configs only moves
// the main ports, so with a subscription that brings listeners of its
// own the switch recomposes the config (which confines or frees them);
// without, it stays a hot patch.
func TestAllowLANReloadsWhenSubscriptionListens(t *testing.T) {
	toggle := func(h *harness) (reloaded, patched bool) {
		t.Helper()
		before := reloads(h)
		h.ctrl.mu.Lock()
		patches := len(h.ctrl.patches)
		h.ctrl.mu.Unlock()
		set := h.s.Settings()
		set.AllowLAN = !set.AllowLAN
		if err := h.s.SaveSettings(set); err != nil {
			t.Fatal(err)
		}
		h.ctrl.mu.Lock()
		defer h.ctrl.mu.Unlock()
		return h.ctrl.reloads > before, len(h.ctrl.patches) > patches
	}
	h := newHarness(t, nil)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if reloaded, patched := toggle(h); reloaded || !patched {
		t.Fatalf("plain subscription: reloaded %v, patched %v", reloaded, patched)
	}
	cur, _ := h.store.Current()
	if err := h.store.SetContent(cur.ID, []byte("proxies: []\nlisteners:\n  - {name: x, type: mixed, listen: 0.0.0.0, port: 7899}\n")); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} { // off again, then on
		if reloaded, _ := toggle(h); !reloaded {
			t.Fatalf("subscription with listeners, AllowLAN → %v: not recomposed", on)
		}
		h.prof.mu.Lock()
		last := h.prof.inputs[len(h.prof.inputs)-1]
		h.prof.mu.Unlock()
		if last.Managed.AllowLAN != on {
			t.Fatalf("composed with AllowLAN %v, want %v", last.Managed.AllowLAN, on)
		}
	}
}

// TestSubscriptionListenKeys: the check that decides whether switching
// 允许局域网 needs a reload reads keys, not the word: a node called
// "listen" or a dns.listen Lanflow replaces anyway must not cost a reload.
func TestSubscriptionListenKeys(t *testing.T) {
	for _, tc := range []struct {
		name, text    string
		lists, dnsKey bool
	}{
		{"plain", "proxies:\n  - {name: listen-hk, type: ss}\nrules:\n  - MATCH,DIRECT\n", false, false},
		{"dns.listen", "dns:\n  enable: true\n  listen: 0.0.0.0:1053\n", false, true},
		{"listeners", "listeners:\n  - name: x\n    type: mixed\n    port: 7899\n", true, false},
		{"tunnels", "tunnels :\n  - tcp,0.0.0.0:6553,1.1.1.1:53,DIRECT\n", true, false},
	} {
		if got := subListenersRe.MatchString(tc.text); got != tc.lists {
			t.Errorf("%s: listeners/tunnels = %v, want %v", tc.name, got, tc.lists)
		}
		if got := subDNSListenRe.MatchString(tc.text); got != tc.dnsKey {
			t.Errorf("%s: an indented listen key = %v, want %v", tc.name, got, tc.dnsKey)
		}
	}
}

// TestLimitedRetryGivesUp: a version whose composed configuration does
// not pass the check is tried a few times (a timeout, a core being
// replaced) and then waits for the user, where a failure that is plainly
// this machine's (a port in use) keeps being retried.
func TestLimitedRetryGivesUp(t *testing.T) {
	if retry, limit := retryable(&retryError{err: errors.New("mihomo -t"), limited: true}); !retry || limit != syncRetryLimited {
		t.Fatalf("limited failure: retry %v, limit %d", retry, limit)
	}
	if retry, limit := retryable(&retryError{err: &docError{"settings", errors.New("port")}}); !retry || limit != 0 {
		t.Fatalf("machine failure: retry %v, limit %d", retry, limit)
	}
	h, _, _ := syncHarness(t)
	const path, hash = "/x/lanflow.yaml", "h1"
	for i := 0; i < syncRetryLimited; i++ {
		h.s.holdExternal(path, hash, nil, true, syncRetryLimited)
		if held, _ := h.s.syncHeld(path); held.retryAt.IsZero() {
			t.Fatalf("try %d: no retry scheduled", i+1)
		}
	}
	h.s.holdExternal(path, hash, nil, true, syncRetryLimited)
	if held, ok := h.s.syncHeld(path); !ok || !held.retryAt.IsZero() {
		t.Fatalf("after %d tries the version is still retried: %+v", syncRetryLimited, held)
	}
}
