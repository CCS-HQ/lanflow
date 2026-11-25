package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Secrets of configuration sync (docs/config-sync.md 密钥). lanflow.yaml
// never holds a subscription link or the WebDAV password: links go into
// the system keychain under sub-<id> (Secrets: the login Keychain on
// macOS, the Credential Manager on Windows) and the file says
// keychain:sub-<id>; the WebDAV password already lives there
// (backup.go). profiles.json keeps its own copy of the links, as before.
//
// Another machine that reads a reference its keychain does not have
// gets the subscription without a link (Subscription.NeedsURL) and the
// user enters it again; nothing fails. The items are not marked
// kSecAttrSynchronizable: iCloud Keychain only syncs items of the data
// protection keychain, which needs an app signed with a team's
// keychain-access-groups entitlement (this app is not), and MyGo's
// Secrets module has no way to set the attribute.

// errNeedsURL is what updating a subscription without its link says.
var errNeedsURL = i18n.Error("这个订阅来自配置同步，本机钥匙串里没有它的链接。请在「编辑」中重新输入订阅链接。")

// ---- one secret of this machine ----

// secretRetry is how long a failed keychain read (the user refused access,
// or the keychain is locked) is remembered: automatic work does not ask
// again before it has passed. Something the user starts asks right away.
const secretRetry = 5 * time.Minute

// secretSlot is one small secret of this machine (the WebDAV password,
// the feedback token): in the system keychain, or — where the system has
// none — obfuscated in a 0600 file of the data directory. It is read once
// and cached.
//
// The keychain may show a prompt and block for as long as the user takes,
// so mu is never held across the read: the cheap reads the UI makes on
// every frame (peekSecret) only look at the cache. A failed read is
// remembered (err, failedAt) instead of being tried again by whoever asks
// next; without that the read's own TopicSettings made the UI ask again
// in a loop.
type secretSlot struct {
	once      sync.Once
	key, file string // keychain key; file name in the data directory

	mu       sync.Mutex
	value    string
	loaded   bool
	kind     string        // model.PasswordStoreKeychain / PasswordStoreFile, "" until known
	asked    bool          // peekSecret started the lazy read of this run
	reading  chan struct{} // closed when the read in flight ends, nil when none
	err      error         // why the last read failed
	failedAt time.Time
}

func (sl *secretSlot) init(key, file string) *secretSlot {
	sl.once.Do(func() { sl.key, sl.file = key, file })
	return sl
}

// fetchSecret reads the slot's secret from where it lives. No lock held.
func (s *Service) fetchSecret(sl *secretSlot) (value, kind string, err error) {
	if st := s.secretStore(); st != nil {
		v, err := st.Get(sl.key)
		switch {
		case err == nil:
			return string(v), model.PasswordStoreKeychain, nil
		case errors.Is(err, ErrSecretNotFound):
			return "", model.PasswordStoreKeychain, nil
		case errors.Is(err, errors.ErrUnsupported):
			s.alog.Warnf("%s: no system keychain (%v); kept obfuscated in %s", sl.key, err, sl.file)
		default:
			return "", "", fmt.Errorf(i18n.T("读取钥匙串失败：%v"), err)
		}
	}
	data, err := os.ReadFile(filepath.Join(s.opt.DataDir, sl.file))
	if err != nil {
		if os.IsNotExist(err) {
			return "", model.PasswordStoreFile, nil
		}
		return "", "", fmt.Errorf(i18n.T("读取密码文件失败：%v"), err)
	}
	return deobfuscate(strings.TrimSpace(string(data))), model.PasswordStoreFile, nil
}

// readSecret returns the slot's secret, reading it when this run has not
// yet. A read that failed less than secretRetry ago is answered with its
// error unless retry is set (the user asked for something that needs the
// secret). Callers that arrive while a read is in flight wait for it and
// share its answer.
func (s *Service) readSecret(sl *secretSlot, retry bool) (string, error) {
	for {
		sl.mu.Lock()
		switch {
		case sl.loaded:
			v := sl.value
			sl.mu.Unlock()
			return v, nil
		case sl.reading != nil:
			ch := sl.reading
			sl.mu.Unlock()
			<-ch
			retry = false // that read's answer is this caller's too
			continue
		case sl.err != nil && !retry && s.d.now().Sub(sl.failedAt) < secretRetry:
			err := sl.err
			sl.mu.Unlock()
			return "", err
		}
		ch := make(chan struct{})
		sl.reading = ch
		sl.mu.Unlock()

		value, kind, err := s.fetchSecret(sl)

		sl.mu.Lock()
		sl.reading = nil
		if err == nil {
			sl.value, sl.kind, sl.loaded, sl.err = value, kind, true, nil
		} else {
			sl.err, sl.failedAt = err, s.d.now()
		}
		sl.mu.Unlock()
		close(ch)
		if err != nil {
			s.alog.Warnf("%s: %v", sl.key, err)
		}
		return value, err
	}
}

// peekSecret is the cheap read for the UI: whether a secret is set and
// where it is kept, from the cache. The first call of a run starts the
// read in the background; it lands with TopicSettings. It never blocks on
// the keychain and never starts a second read by itself.
func (s *Service) peekSecret(sl *secretSlot) (has bool, kind string) {
	sl.mu.Lock()
	start := !sl.loaded && !sl.asked
	sl.asked = true
	has, kind = sl.value != "", sl.kind
	sl.mu.Unlock()
	if start {
		s.goBackground(func() {
			_, _ = s.readSecret(sl, false)
			s.em.emit(TopicSettings)
		})
	}
	if kind == "" {
		kind = model.PasswordStoreFile
		if s.secretStore() != nil {
			kind = model.PasswordStoreKeychain
		}
	}
	return has, kind
}

// writeSecret stores value in the slot ("" removes it). It is a user's
// action, so a remembered read failure is tried again first: where the
// secret lives must be known before it is written.
func (s *Service) writeSecret(sl *secretSlot, value string) error {
	if _, err := s.readSecret(sl, true); err != nil {
		return err
	}
	sl.mu.Lock()
	kind := sl.kind
	sl.mu.Unlock()
	path := filepath.Join(s.opt.DataDir, sl.file)
	st := s.secretStore()
	switch {
	case kind == model.PasswordStoreKeychain && st != nil:
		var err error
		if value == "" {
			if err = st.Delete(sl.key); errors.Is(err, ErrSecretNotFound) {
				err = nil
			}
		} else {
			err = st.Set(sl.key, []byte(value))
		}
		if err != nil {
			return fmt.Errorf(i18n.T("写入钥匙串失败：%v"), err)
		}
	case value == "":
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf(i18n.T("删除密码文件失败：%v"), err)
		}
	default:
		if err := writeFileAtomic(path, []byte(obfuscate(value)+"\n"), 0o600); err != nil {
			return fmt.Errorf(i18n.T("保存密码文件失败：%v"), err)
		}
	}
	sl.mu.Lock()
	sl.value, sl.loaded, sl.err = value, true, nil
	if sl.kind == "" {
		sl.kind = model.PasswordStoreFile
	}
	sl.mu.Unlock()
	return nil
}

// ---- subscription links ----

// linkRetry is how long a link the keychain refused to take is left
// alone: the sync file is rewritten often, and each write would otherwise
// ask the keychain again for every subscription.
const linkRetry = 10 * time.Minute

// linkSecrets remembers what this run put into the keychain, so the
// file can be rewritten often without writing the keychain each time,
// and what the keychain refused.
type linkSecrets struct {
	mu     sync.Mutex
	stored map[string]string     // key → value written
	failed map[string]linkFailed // key → the value that could not be written
}

type linkFailed struct {
	value string
	at    time.Time
}

// storeLinkSecrets puts the link of every URL subscription in subs into
// the keychain. It returns the first error (no keychain, or the user
// refused access): the file is still written with references, and this
// machine keeps working from profiles.json. A link that failed is not
// tried (or reported) again for linkRetry unless it changed.
func (s *Service) storeLinkSecrets(subs []model.Subscription) error {
	ks := s.secretStore()
	ls := &s.cs.links
	now := s.d.now()
	type item struct{ key, url string }
	var todo []item
	ls.mu.Lock()
	if ls.stored == nil {
		ls.stored = map[string]string{}
	}
	if ls.failed == nil {
		ls.failed = map[string]linkFailed{}
	}
	for _, sub := range subs {
		if sub.Source != urlSubscriptionK || sub.URL == "" {
			continue
		}
		key := syncfile.SubscriptionKey(sub.ID)
		if ls.stored[key] == sub.URL {
			continue
		}
		if f, ok := ls.failed[key]; ok && f.value == sub.URL && now.Sub(f.at) < linkRetry {
			continue
		}
		todo = append(todo, item{key, sub.URL})
	}
	ls.mu.Unlock()

	// the keychain may block on a prompt: no lock held
	var first error
	for _, it := range todo {
		err := errors.ErrUnsupported
		if ks != nil {
			err = ks.Set(it.key, []byte(it.url))
		}
		ls.mu.Lock()
		if err != nil {
			ls.failed[it.key] = linkFailed{value: it.url, at: now}
			if first == nil {
				first = err
			}
		} else {
			ls.stored[it.key] = it.url
			delete(ls.failed, it.key)
		}
		ls.mu.Unlock()
	}
	return first
}

// forgetLinkSecret removes a deleted subscription's link from the
// keychain (best effort; only for links this machine stored or synced).
func (s *Service) forgetLinkSecret(id string) {
	ks := s.secretStore()
	if ks == nil {
		return
	}
	key := syncfile.SubscriptionKey(id)
	ls := &s.cs.links
	ls.mu.Lock()
	delete(ls.stored, key)
	delete(ls.failed, key)
	ls.mu.Unlock()
	if err := ks.Delete(key); err != nil && !errors.Is(err, ErrSecretNotFound) {
		s.alog.Warnf("config sync: keychain delete %s: %v", key, err)
	}
}

// resolveLink returns the link a document's subscription entry stands
// for: the link itself, the keychain's value for a reference, or — when
// this machine's keychain lacks it — the link this machine already has
// for the same subscription (local). "" means the user must enter it.
func (s *Service) resolveLink(ref, local string) string {
	if ref == "" {
		return local
	}
	if _, ok := syncfile.KeychainKey(ref); !ok {
		return ref // a plain link (written by a person or an agent)
	}
	if v := s.resolveRef(ref); v != "" {
		// remember it: the keychain has it, no need to write it back
		ls := &s.cs.links
		ls.mu.Lock()
		if ls.stored == nil {
			ls.stored = map[string]string{}
		}
		key, _ := syncfile.KeychainKey(ref)
		ls.stored[key] = v
		ls.mu.Unlock()
		return v
	}
	return local
}

// ---- manual node credentials ----

// storeManualNodeSecrets stores every non-empty credential field of nodes
// into the keychain under node-<id>-<field>.
func (s *Service) storeManualNodeSecrets(nodes []model.ManualNode) error {
	var items []struct{ key, val string }
	for i := range nodes {
		n := &nodes[i]
		for _, f := range syncfile.ManualNodeSecretFields {
			p := modelSecretPtr(n, f)
			if p == nil || *p == "" {
				continue
			}
			items = append(items, struct{ key, val string }{
				key: syncfile.ManualNodeSecretKey(n.ID, f),
				val: *p,
			})
		}
	}
	return s.storeLinkItems(items)
}

func (s *Service) storeLinkItems(items []struct{ key, val string }) error {
	ks := s.secretStore()
	ls := &s.cs.links
	now := s.d.now()
	var todo []struct{ key, val string }
	ls.mu.Lock()
	if ls.stored == nil {
		ls.stored = map[string]string{}
	}
	if ls.failed == nil {
		ls.failed = map[string]linkFailed{}
	}
	for _, it := range items {
		if ls.stored[it.key] == it.val {
			continue
		}
		if f, ok := ls.failed[it.key]; ok && f.value == it.val && now.Sub(f.at) < linkRetry {
			continue
		}
		todo = append(todo, it)
	}
	ls.mu.Unlock()

	var first error
	for _, it := range todo {
		err := errors.ErrUnsupported
		if ks != nil {
			err = ks.Set(it.key, []byte(it.val))
		}
		ls.mu.Lock()
		if err != nil {
			ls.failed[it.key] = linkFailed{value: it.val, at: now}
			if first == nil {
				first = err
			}
		} else {
			ls.stored[it.key] = it.val
			delete(ls.failed, it.key)
		}
		ls.mu.Unlock()
	}
	return first
}

// forgetManualNodeSecret removes a manual node credential field from the keychain.
func (s *Service) forgetManualNodeSecret(id, field string) {
	s.forgetKeychainKey(syncfile.ManualNodeSecretKey(id, field))
}

// forgetManualNodeSecrets removes all credential fields of manual node id from the keychain.
func (s *Service) forgetManualNodeSecrets(id string) {
	for _, f := range syncfile.ManualNodeSecretFields {
		s.forgetManualNodeSecret(id, f)
	}
}

func (s *Service) forgetKeychainKey(key string) {
	ks := s.secretStore()
	if ks == nil {
		return
	}
	ls := &s.cs.links
	ls.mu.Lock()
	delete(ls.stored, key)
	delete(ls.failed, key)
	ls.mu.Unlock()
	if err := ks.Delete(key); err != nil && !errors.Is(err, ErrSecretNotFound) {
		s.alog.Warnf("config sync: keychain delete %s: %v", key, err)
	}
}

// resolveManualSecret resolves a manual node credential field from a sync
// document: "" stays "" (explicitly unset/cleared), a plain value is used
// directly, and a keychain reference resolves from the keychain or falls back
// to local. missing is true when ref is a keychain reference that neither the
// keychain nor local has.
func (s *Service) resolveManualSecret(ref, local string) (val string, missing bool) {
	if ref == "" {
		return "", false
	}
	key, ok := syncfile.KeychainKey(ref)
	if !ok {
		return ref, false
	}
	if v := s.resolveRef(ref); v != "" {
		ls := &s.cs.links
		ls.mu.Lock()
		if ls.stored == nil {
			ls.stored = map[string]string{}
		}
		ls.stored[key] = v
		ls.mu.Unlock()
		return v, false
	}
	if local != "" {
		return local, false
	}
	return "", true
}
