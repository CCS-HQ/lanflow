package app

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

const (
	sealPass  = "plum-orbit-candle-42"
	sealPass2 = "second passphrase 7"
	sealLink  = "https://a.example/sub?token=AAA111"
	sealDAV   = "dav-pa55word"
)

// sealedDevice is device A: sync on in dir, a subscription with a token
// link and a WebDAV password, secrets sealed with sealPass.
func sealedDevice(t *testing.T) (*harness, *fakeSecrets, string) {
	t.Helper()
	h, ks, dir := syncHarness(t)
	if _, err := h.store.Put(model.Subscription{ID: "cafe0001", Name: "加密的订阅", Source: urlSubscriptionK, URL: sealLink}); err != nil {
		t.Fatal(err)
	}
	h.s.refreshStore(TopicSubs)
	if err := h.s.SetWebDAVPassword(sealDAV); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetSyncPassphrase(sealPass); err != nil {
		t.Fatal(err)
	}
	return h, ks, dir
}

// secondDevice is device B: its own data, the same sync folder, the
// file's configuration taken.
func secondDevice(t *testing.T, dir string) (*harness, *fakeSecrets) {
	t.Helper()
	h, ks, _ := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, model.SyncUseFile); err != nil {
		t.Fatal(err)
	}
	return h, ks
}

// markDirty marks a local change (the change events reach the sync loop
// asynchronously).
func markDirty(h *harness) {
	h.s.cs.mu.Lock()
	h.s.cs.dirty = true
	h.s.cs.mu.Unlock()
}

func subURL(h *harness, id string) string {
	s, _ := h.store.Get(id)
	return s.URL
}

func syncBytes(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, syncfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSealEnableWritesSealedFile(t *testing.T) {
	t.Parallel()
	h, ks, dir := sealedDevice(t)
	text := syncBytes(t, dir)
	doc := readSync(t, dir)
	if doc.Encryption == nil || doc.Encryption.KDF != syncfile.KDFArgon2id {
		t.Fatalf("no encryption block:\n%s", text)
	}
	if strings.Contains(text, "AAA111") || strings.Contains(text, sealDAV) || strings.Contains(text, sealPass) || strings.Contains(text, ": keychain:") {
		t.Fatalf("a secret or reference in the sealed file:\n%s", text)
	}
	vals := syncfile.SealedValues(doc)
	if len(vals) != 3 { // the harness's subscription, cafe0001, the WebDAV password
		t.Fatalf("sealed values = %v", vals)
	}
	// the keychain has the derived key with its salt, never the passphrase
	raw := ks.get(syncKeySecret)
	var sk storedKey
	if json.Unmarshal([]byte(raw), &sk) != nil || sk.Salt != doc.Encryption.Salt || strings.Contains(raw, sealPass) {
		t.Fatalf("sync-key = %q", raw)
	}
	st := h.s.ConfigSyncStatus()
	if !st.Encrypted || st.Locked || st.PassphraseChanged {
		t.Fatalf("status = %+v", st)
	}
	// stable: an unchanged configuration is not sealed (and written) again
	h.s.cs.mu.Lock()
	h.s.cs.dirty = true
	h.s.cs.mu.Unlock()
	step(h)
	if syncBytes(t, dir) != text {
		t.Fatal("unchanged configuration rewritten with new ciphertexts")
	}
	// a changed link is sealed again; the other value stays as it was
	sub, _ := h.store.Get("cafe0001")
	sub.URL = "https://a.example/sub?token=BBB222"
	if err := h.s.EditSubscription(sub); err != nil {
		t.Fatal(err)
	}
	markDirty(h)
	step(h)
	after := readSync(t, dir)
	av := syncfile.SealedValues(after)
	if av[syncfile.SubscriptionPath("cafe0001")] == vals[syncfile.SubscriptionPath("cafe0001")] || av[syncfile.WebDAVPasswordPath] != vals[syncfile.WebDAVPasswordPath] {
		t.Fatalf("resealing: before %v after %v", vals, av)
	}
}

func TestSealRestartKeepsEncryption(t *testing.T) {
	t.Parallel()
	h, _, dir := sealedDevice(t)
	text := syncBytes(t, dir)
	// a new run: nothing primed, the loop starts dirty and the file is its own
	h.s.resetSeal()
	h.s.cs.seal.keyRead = false
	h.s.cs.mu.Lock()
	h.s.cs.dirty, h.s.cs.canon = true, nil
	h.s.cs.mu.Unlock()
	step(h)
	if got := syncBytes(t, dir); got != text {
		t.Fatalf("restart rewrote the sealed file:\n%s", got)
	}
	if st := h.s.ConfigSyncStatus(); !st.Encrypted || st.Locked {
		t.Fatalf("status after restart = %+v", st)
	}
}

func TestSealNewDeviceUnlocks(t *testing.T) {
	t.Parallel()
	_, _, dir := sealedDevice(t)
	b, bks := secondDevice(t, dir)
	st := b.s.ConfigSyncStatus()
	if !st.Encrypted || !st.Locked || st.PassphraseChanged || st.LockedSecrets != 3 {
		t.Fatalf("new device status = %+v", st)
	}
	if subURL(b, "cafe0001") != "" {
		t.Fatal("link known before unlocking")
	}
	// a locked device writing the file keeps the sealed values
	before := syncfile.SealedValues(readSync(t, dir))
	set := b.s.Settings()
	set.LogLevel = "warning"
	if err := b.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	markDirty(b)
	step(b)
	if got := readSync(t, dir); got.Settings.LogLevel != "warning" || syncfile.SealedValues(got)[syncfile.SubscriptionPath("cafe0001")] != before[syncfile.SubscriptionPath("cafe0001")] || got.Encryption == nil {
		t.Fatalf("locked write dropped sealed values: %+v", syncfile.SealedValues(got))
	}

	if err := b.s.UnlockSync("not the passphrase"); !errors.Is(err, syncfile.ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if bks.get(syncKeySecret) != "" || !b.s.ConfigSyncStatus().Locked {
		t.Fatal("a wrong passphrase changed something")
	}
	if err := b.s.UnlockSync(sealPass); err != nil {
		t.Fatal(err)
	}
	if got := subURL(b, "cafe0001"); got != sealLink {
		t.Fatalf("link after unlocking = %q", got)
	}
	if pw, _ := b.s.loadPassword(); pw != sealDAV {
		t.Fatalf("WebDAV password after unlocking = %q", pw)
	}
	if st := b.s.ConfigSyncStatus(); st.Locked || st.LockedSecrets != 0 || len(st.MissingSecrets) != 0 || st.WebDAVPasswordMissing {
		t.Fatalf("status after unlocking = %+v", st)
	}
	step(b)
	if bks.get(syncfile.SubscriptionKey("cafe0001")) != sealLink || bks.get(syncKeySecret) == "" {
		t.Fatal("unsealed link or derived key not in this device's keychain")
	}
	// later starts need no passphrase: a fresh read of the keychain opens it
	b.s.resetSeal()
	b.s.cs.seal.keyRead = false
	step(b)
	if st := b.s.ConfigSyncStatus(); !st.Encrypted || st.Locked {
		t.Fatalf("status after restart = %+v", st)
	}
}

func TestSealChangePassphraseOtherDevice(t *testing.T) {
	t.Parallel()
	a, _, dir := sealedDevice(t)
	b, _ := secondDevice(t, dir)
	if err := b.s.UnlockSync(sealPass); err != nil {
		t.Fatal(err)
	}
	step(b)
	old := readSync(t, dir)

	if err := a.s.SetSyncPassphrase(sealPass2); err != nil {
		t.Fatal(err)
	}
	now := readSync(t, dir)
	if now.Encryption.Salt == old.Encryption.Salt {
		t.Fatal("salt not changed with the passphrase")
	}
	ov, nv := syncfile.SealedValues(old), syncfile.SealedValues(now)
	if len(nv) != 3 || nv[syncfile.SubscriptionPath("cafe0001")] == ov[syncfile.SubscriptionPath("cafe0001")] {
		t.Fatalf("not sealed again: %v", nv)
	}
	if _, err := now.Encryption.Unlock(sealPass); !errors.Is(err, syncfile.ErrWrongPassphrase) {
		t.Fatal("the old passphrase still opens the file")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the rewrite left files behind: %v", entries)
	}

	step(b)
	st := b.s.ConfigSyncStatus()
	if !st.Locked || !st.PassphraseChanged {
		t.Fatalf("other device status = %+v", st)
	}
	if subURL(b, "cafe0001") != sealLink {
		t.Fatal("the other device lost its link")
	}
	if err := b.s.UnlockSync(sealPass); !errors.Is(err, syncfile.ErrWrongPassphrase) {
		t.Fatalf("old passphrase: %v", err)
	}
	if err := b.s.UnlockSync(sealPass2); err != nil {
		t.Fatal(err)
	}
	if st := b.s.ConfigSyncStatus(); st.Locked || st.PassphraseChanged {
		t.Fatalf("after the new passphrase: %+v", st)
	}

	// a locked device cannot change the passphrase
	if err := b.s.ForgetSyncPassphrase(); err != nil {
		t.Fatal(err)
	}
	if st := b.s.ConfigSyncStatus(); !st.Locked || st.PassphraseChanged {
		t.Fatalf("after forgetting: %+v", st)
	}
	if subURL(b, "cafe0001") != sealLink {
		t.Fatal("forgetting the passphrase dropped the link")
	}
	if err := b.s.SetSyncPassphrase("yet another one"); !errors.Is(err, ErrSyncLocked) {
		t.Fatalf("change while locked: %v", err)
	}
}

func TestSealTurnOffAndOn(t *testing.T) {
	t.Parallel()
	h, ks, dir := sealedDevice(t)
	if err := h.s.SetSyncPassphrase(""); err != nil {
		t.Fatal(err)
	}
	doc := readSync(t, dir)
	if doc.Encryption != nil || doc.Subscriptions[len(doc.Subscriptions)-1].URL != syncfile.SubscriptionRef("cafe0001") || doc.WebDAVPassword != syncfile.WebDAVPasswordRef {
		t.Fatalf("off: not keychain references:\n%s", syncBytes(t, dir))
	}
	if len(syncfile.SealedValues(doc)) != 0 || ks.get(syncKeySecret) != "" {
		t.Fatal("sealed values or the key left after turning it off")
	}
	if st := h.s.ConfigSyncStatus(); st.Encrypted || st.Locked {
		t.Fatalf("status = %+v", st)
	}
	if err := h.s.SetSyncPassphrase(sealPass); err != nil {
		t.Fatal(err)
	}
	text := syncBytes(t, dir)
	if strings.Contains(text, ": keychain:") || len(syncfile.SealedValues(readSync(t, dir))) != 3 {
		t.Fatalf("on: references not replaced:\n%s", text)
	}
	if err := h.s.SetSyncPassphrase("short"); !errors.Is(err, syncfile.ErrShortPassphrase) {
		t.Fatalf("short passphrase: %v", err)
	}
}

// TestSealMovedValueRefused: a sealed value copied onto another
// subscription does not open there; the subscription keeps its own link.
func TestSealMovedValueRefused(t *testing.T) {
	t.Parallel()
	h, _, dir := sealedDevice(t)
	if _, err := h.store.Put(model.Subscription{ID: "cafe0002", Name: "第二个", Source: urlSubscriptionK, URL: "https://b.example/sub?token=CCC"}); err != nil {
		t.Fatal(err)
	}
	h.s.refreshStore(TopicSubs)
	markDirty(h)
	step(h)
	if len(syncfile.SealedValues(readSync(t, dir))) != 4 {
		t.Fatal("second subscription not written")
	}
	writeExternal(t, dir, func(d *syncfile.Doc) {
		var first string
		for _, s := range d.Subscriptions {
			if s.ID == "cafe0001" {
				first = s.URL
			}
		}
		for i, s := range d.Subscriptions {
			if s.ID == "cafe0002" {
				d.Subscriptions[i].URL = first
			}
		}
		d.Settings.LogLevel = "error"
	})
	step(h)
	if h.s.Settings().LogLevel != "error" {
		t.Fatal("the rest of the change was not loaded")
	}
	if got := subURL(h, "cafe0002"); got != "https://b.example/sub?token=CCC" {
		t.Fatalf("moved value accepted: %q", got)
	}
}

// TestSealSecretsStayOutOfLogs: neither the links, the passphrase nor
// the sealed values reach Lanflow's log or the diagnostics bundle.
func TestSealSecretsStayOutOfLogs(t *testing.T) {
	t.Parallel()
	a, _, dir := sealedDevice(t)
	b, _ := secondDevice(t, dir)
	_ = b.s.UnlockSync("wrong passphrase!")
	if err := b.s.UnlockSync(sealPass); err != nil {
		t.Fatal(err)
	}
	if err := a.s.SetSyncPassphrase(sealPass2); err != nil {
		t.Fatal(err)
	}
	step(b)
	var sealed []string
	for _, v := range syncfile.SealedValues(readSync(t, dir)) {
		sealed = append(sealed, strings.TrimPrefix(v, syncfile.SealedPrefix))
	}
	doc := readSync(t, dir)
	sealed = append(sealed, strings.TrimPrefix(doc.Encryption.Check, syncfile.SealedPrefix))
	forbidden := append([]string{"AAA111", sealDAV, sealPass, sealPass2, "wrong passphrase!"}, sealed...)

	for _, h := range []*harness{a, b} {
		zipPath := filepath.Join(t.TempDir(), "diag.zip")
		if err := h.s.ExportDiagnostics(zipPath); err != nil {
			t.Fatal(err)
		}
		texts := map[string]string{}
		zr, err := zip.OpenReader(zipPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			texts["diag:"+f.Name] = string(data)
		}
		zr.Close()
		logData, _ := os.ReadFile(h.s.alog.Path())
		texts["lanflow.log"] = string(logData)
		for _, e := range h.s.AppLogs() {
			texts["ring"] += e.Payload + "\n"
		}
		st, _ := json.Marshal(h.s.ConfigSyncStatus())
		texts["status"] = string(st)
		for name, text := range texts {
			for _, f := range forbidden {
				if strings.Contains(text, f) {
					t.Errorf("%s contains %q", name, f)
				}
			}
		}
		if !strings.Contains(texts["lanflow.log"], "unlock sync") && h == b {
			t.Error("unlock not logged at all")
		}
	}
}

func TestSealManualNodesRoundTripAndLockedDevice(t *testing.T) {
	t.Parallel()
	a, aks, dir := syncHarness(t)
	n1, err := a.s.SaveManualNode(model.ManualNode{
		Name: "seal-tg", Type: model.ProxyTypeTrojanGo, Server: "tg.example.com", Port: 443,
		Password: "node-pw-secret", SSOptsEnabled: true, SSOptsMethod: "aes-128-gcm", SSOptsPassword: "node-ss-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := a.s.SaveManualNode(model.ManualNode{
		Name: "seal-hy2", Type: model.ProxyTypeHysteria2, Server: "hy2.example.com", Port: 8443,
		Password: "hy2-pw-secret", Obfs: "salamander", ObfsPassword: "hy2-obfs-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.SetProxyChains([]model.ProxyChain{{
		Name: "加密链路", Hops: []string{"香港 01", n1.Name}, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := a.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.s.SetSyncPassphrase(sealPass); err != nil {
		t.Fatal(err)
	}

	text := syncBytes(t, dir)
	for _, secret := range []string{"node-pw-secret", "node-ss-secret", "hy2-pw-secret", "hy2-obfs-secret", ": keychain:"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret or keychain ref %q leaked into sealed file:\n%s", secret, text)
		}
	}
	vals := syncfile.SealedValues(readSync(t, dir))
	if len(vals) != 5 { // 1 default subscription + 4 manual node secrets
		t.Fatalf("sealed values = %v", vals)
	}

	// Unchanged sync step does not rewrite ciphertexts.
	markDirty(a)
	step(a)
	if syncBytes(t, dir) != text {
		t.Fatal("unchanged sealed manual nodes rewritten")
	}

	// Device B enables sync while locked: loads cleanly, skips incomplete nodes in compose, preserves sealed values on rewrite.
	b, bks := secondDevice(t, dir)
	st := b.s.ConfigSyncStatus()
	if !st.Encrypted || !st.Locked || st.LockedSecrets != 5 {
		t.Fatalf("locked device status = %+v", st)
	}
	if bNodes := b.s.ManualNodes(); len(bNodes) != 2 || bNodes[0].Password != "" || bNodes[1].ObfsPassword != "" {
		t.Fatalf("locked device must not have plaintext node secrets: %+v", bNodes)
	}
	if in, _, err := b.s.buildInput(composeArgs{}); err != nil || len(in.ManualNodes) != 0 {
		t.Fatalf("locked device must omit incomplete manual nodes from composition: %+v %v", in.ManualNodes, err)
	}

	// Locked write on device B preserves sealed manual node values.
	before := syncfile.SealedValues(readSync(t, dir))
	bset := b.s.Settings()
	bset.LogLevel = "warning"
	if err := b.s.SaveSettings(bset); err != nil {
		t.Fatal(err)
	}
	markDirty(b)
	step(b)
	after := syncfile.SealedValues(readSync(t, dir))
	if after[syncfile.ManualNodeSecretPath(n1.ID, "password")] != before[syncfile.ManualNodeSecretPath(n1.ID, "password")] {
		t.Fatal("locked device dropped sealed manual node secret on rewrite")
	}

	// Unlock device B: secrets, runtime compose, and keychain are populated.
	if err := b.s.UnlockSync(sealPass); err != nil {
		t.Fatal(err)
	}
	bNodes := b.s.ManualNodes()
	if len(bNodes) != 2 || bNodes[0].Password != "node-pw-secret" || bNodes[0].SSOptsPassword != "node-ss-secret" ||
		bNodes[1].Password != "hy2-pw-secret" || bNodes[1].ObfsPassword != "hy2-obfs-secret" {
		t.Fatalf("unlocked manual nodes = %+v", bNodes)
	}
	if in, _, err := b.s.buildInput(composeArgs{}); err != nil || len(in.ManualNodes) != 2 {
		t.Fatalf("unlocked buildInput = %+v %v", in.ManualNodes, err)
	}
	step(b)
	if bks.get(syncfile.ManualNodeSecretKey(n1.ID, "password")) != "node-pw-secret" {
		t.Fatal("unlocked node secret not stored in keychain")
	}

	// Sync device B's LogLevel change onto A before A modifies n2.
	step(a)

	// Clearing optional ObfsPassword on unlocked device A clears it in the sealed file and keychain.
	n2.ObfsPassword = ""
	if _, err := a.s.SaveManualNode(n2); err != nil {
		t.Fatal(err)
	}
	step(a)
	if doc := readSync(t, dir); doc.ManualNodes[1].ObfsPassword != "" {
		t.Fatalf("cleared optional secret kept in sealed file: %q", doc.ManualNodes[1].ObfsPassword)
	}
	if aks.get(syncfile.ManualNodeSecretKey(n2.ID, "obfsPassword")) != "" {
		t.Fatal("cleared optional secret kept in keychain")
	}

	// Changing passphrase re-seals manual node secrets and keeps secrets out of logs & diagnostics.
	if err := a.s.SetSyncPassphrase(sealPass2); err != nil {
		t.Fatal(err)
	}
	doc := readSync(t, dir)
	var sealed []string
	for _, v := range syncfile.SealedValues(doc) {
		sealed = append(sealed, strings.TrimPrefix(v, syncfile.SealedPrefix))
	}
	forbidden := append([]string{"node-pw-secret", "node-ss-secret", "hy2-pw-secret", "hy2-obfs-secret", sealPass, sealPass2}, sealed...)
	zipPath := filepath.Join(t.TempDir(), "diag.zip")
	if err := a.s.ExportDiagnostics(zipPath); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		for _, bad := range forbidden {
			if strings.Contains(string(data), bad) {
				t.Errorf("diag:%s contains %q", f.Name, bad)
			}
		}
	}
}
