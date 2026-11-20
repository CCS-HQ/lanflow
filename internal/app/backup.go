package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// WebDAV backup and restore (Settings.WebDAV). A backup is the settings
// export with the subscriptions included (lanflow.yaml, settingsio.go)
// uploaded as lanflow-backup-YYYYMMDD-HHMMSS.yaml into the remote
// directory (backups of earlier versions end in .json and still restore,
// ImportSettings reads both); restore
// downloads one and imports it through ImportSettings, so every part is
// validated like a manual import. The password lives in the system
// keychain through Secrets (wired by main) and, where the system has
// none, obfuscated in a 0600 file of the data directory.

const (
	backupPrefix     = "lanflow-backup-"
	backupSuffix     = ".yaml"
	backupSuffixJSON = ".json" // backups of earlier versions
	backupTimeLayout = "20060102-150405"
	webdavSecretKey  = "webdav-password"
	webdavSecretFile = "webdav.secret"
	autoBackupRetry  = time.Hour // wait between failed automatic attempts
)

// SecretStore keeps small secrets in the system's credential store.
// main sets Secrets to MyGo's keychain module; nil (tests, or a build
// without it) means the file fallback. Get returns ErrSecretNotFound for
// a key never set, and an error for which errors.Is(err,
// errors.ErrUnsupported) holds where the system has no store.
type SecretStore interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// Secrets is the credential store the service uses; see SecretStore.
var Secrets SecretStore

// ErrSecretNotFound is what SecretStore.Get returns for a missing key.
var ErrSecretNotFound = errors.New("secret not found")

// ---- password storage ----

func (s *Service) webdavSecretPath() string { return filepath.Join(s.opt.DataDir, webdavSecretFile) }

// webdavSlot is where the password is kept (configsecrets.go).
func (s *Service) webdavSlot() *secretSlot {
	return s.cs.webdavPW.init(webdavSecretKey, webdavSecretFile)
}

// loadPassword returns the password, reading it once: from the keychain,
// or the file fallback when the system has no keychain. It is for
// automatic work: a read the keychain refused a moment ago is not tried
// again (secretRetry).
func (s *Service) loadPassword() (string, error) {
	return s.readSecret(s.webdavSlot(), false)
}

// SetWebDAVPassword implements API.SetWebDAVPassword: "" removes it.
func (s *Service) SetWebDAVPassword(pw string) (err error) {
	defer func() { s.logOp("set webdav password", err) }()
	if err := s.writeSecret(s.webdavSlot(), pw); err != nil {
		return err
	}
	s.confirmWebDAVOrigin() // entered on this machine, for the server set here
	s.em.emit(TopicSettings)
	return nil
}

// obfuscate hides pw from a casual look at the file: a random nonce and
// a SHA-256 keystream over it. It is not encryption (the key is in the
// file), which the settings UI says.
func obfuscate(pw string) string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(append(nonce, xorStream(nonce, []byte(pw))...))
}

// deobfuscate reverses obfuscate; "" for anything unreadable.
func deobfuscate(s string) string {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) < 16 {
		return ""
	}
	return string(xorStream(raw[:16], raw[16:]))
}

func xorStream(nonce, data []byte) []byte {
	out := make([]byte, len(data))
	var block [32]byte
	for i := range data {
		if i%32 == 0 {
			block = sha256.Sum256(append(append([]byte("lanflow-webdav:"), nonce...), byte(i/32)))
		}
		out[i] = data[i] ^ block[i%32]
	}
	return out
}

// ---- API ----

// WebDAVInfo implements API.WebDAVInfo (cheap read: the password comes
// from the cache, read in the background the first time; peekSecret).
func (s *Service) WebDAVInfo() model.WebDAVInfo {
	s.mu.Lock()
	last := s.state.settings.WebDAV.LastBackupAt
	s.mu.Unlock()
	has, kind := s.peekSecret(s.webdavSlot())
	st := feat2(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	return model.WebDAVInfo{
		PasswordStore: kind,
		HasPassword:   has,
		Busy:          st.busy,
		LastBackupAt:  last,
		LastError:     st.lastError,
	}
}

// webdavDir returns the remote directory of set.
func webdavDir(set model.WebDAVSettings) string {
	d := strings.Trim(strings.TrimSpace(set.Dir), "/")
	if d == "" {
		d = model.WebDAVDefaultDir
	}
	return d
}

// webdavClient builds the client for the current settings. user says the
// user asked for the operation on this machine: the password is then read
// even after a recent refusal (secretRetry), and the server set here
// counts as confirmed for automatic backups (confirmWebDAVOrigin).
func (s *Service) webdavClient(user bool) (*webdavClient, model.WebDAVSettings, error) {
	s.mu.Lock()
	set := s.state.settings.WebDAV
	running := s.state.st.Core == model.CoreRunning
	port := s.state.settings.MixedPort
	s.mu.Unlock()
	if strings.TrimSpace(set.URL) == "" {
		return nil, set, errors.New(i18n.T("请先填写 WebDAV 服务器地址。"))
	}
	if _, err := newWebDAVClient(set.URL, set.User, "", nil); err != nil {
		return nil, set, err // before the keychain is asked for anything
	}
	pw, err := s.readSecret(s.webdavSlot(), user)
	if err != nil {
		return nil, set, err
	}
	// The server may only be reachable through the proxy (or only
	// directly); go through the running core like the other downloads,
	// which honours the rules, and directly without one.
	hc := s.d.httpDirect(webdavTimeout)
	if running {
		hc = s.d.httpVia(port, webdavTimeout)
	}
	c, err := newWebDAVClient(set.URL, set.User, pw, hc)
	if err != nil {
		return nil, set, err
	}
	if user {
		s.confirmWebDAVOrigin()
	}
	return c, set, nil
}

// ---- the server the password belongs to ----

// The WebDAV address, user and schedule travel with configuration sync
// and with imported files; the password stays on this machine (or comes
// sealed). Whoever can write the sync file could therefore point the
// address at a server of their own and turn the automatic backup on, and
// this machine would send them the password and a backup with every
// subscription link. So the password is tied to the server it was set up
// for: the scheme and host the user last confirmed on this machine, by
// saving the address here, entering the password, or running a test,
// backup, listing or restore by hand. While the address in the settings
// names another server (it arrived through sync or an import), automatic
// backups wait; WebDAVInfo.LastError says so.

const webdavOriginFile = "webdav.origin" // the confirmed scheme://host, in the data directory

// webdavOriginState caches webdavOriginFile.
type webdavOriginState struct {
	mu       sync.Mutex
	loaded   bool
	known    bool   // the file exists: an origin ("" = none) was confirmed
	origin   string // the confirmed origin
	notified string // the unconfirmed origin a notice was shown for
}

// webdavOrigin is the scheme://host[:port] of a WebDAV address, "" when
// it is empty or invalid.
func webdavOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func (s *Service) webdavOriginPath() string { return filepath.Join(s.opt.DataDir, webdavOriginFile) }

// confirmedOriginLocked reads the confirmed origin once; o.mu held.
func (s *Service) confirmedOriginLocked(o *webdavOriginState) {
	if o.loaded {
		return
	}
	o.loaded = true
	if data, err := os.ReadFile(s.webdavOriginPath()); err == nil {
		o.known, o.origin = true, strings.TrimSpace(string(data))
	}
}

// setWebDAVOrigin records origin as confirmed on this machine.
func (s *Service) setWebDAVOrigin(origin string) {
	o := &s.cs.origin
	o.mu.Lock()
	s.confirmedOriginLocked(o)
	same := o.known && o.origin == origin
	o.known, o.origin, o.notified = true, origin, ""
	o.mu.Unlock()
	if same {
		return
	}
	if err := writeFileAtomic(s.webdavOriginPath(), []byte(origin+"\n"), 0o600); err != nil {
		s.alog.Warnf("webdav: remember the confirmed server: %v", err)
	}
	st := feat2(s)
	st.mu.Lock()
	if st.lastError == errWebDAVUnconfirmed.Error() {
		st.lastError = ""
	}
	st.mu.Unlock()
}

// confirmWebDAVOrigin records the server of the current settings as the
// one the user confirmed here.
func (s *Service) confirmWebDAVOrigin() {
	s.mu.Lock()
	raw := s.state.settings.WebDAV.URL
	s.mu.Unlock()
	s.setWebDAVOrigin(webdavOrigin(raw))
}

// adoptWebDAVOrigin records the current server when none was recorded
// yet: settings from before this record existed are the user's own. It
// runs before settings from a sync file or an import are applied, so what
// they bring is compared with what was here.
func (s *Service) adoptWebDAVOrigin() {
	o := &s.cs.origin
	o.mu.Lock()
	s.confirmedOriginLocked(o)
	known := o.known
	o.mu.Unlock()
	if !known {
		s.confirmWebDAVOrigin()
	}
}

// webdavOriginConfirmed reports whether set's server is the confirmed one.
func (s *Service) webdavOriginConfirmed(set model.WebDAVSettings) bool {
	s.adoptWebDAVOrigin()
	o := &s.cs.origin
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.origin == webdavOrigin(set.URL)
}

var errWebDAVUnconfirmed = i18n.Error("WebDAV 服务器地址是通过配置同步或导入更改的，自动备份已暂停。请在「设置 › 备份」中确认地址无误后，点一次「测试连接」或「立即备份」，自动备份即恢复。")

// begin marks the backup state busy; it returns false when another
// operation runs.
func (s *Service) webdavBegin() (func(), bool) {
	st := feat2(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.busy {
		return nil, false
	}
	st.busy = true
	s.em.emit(TopicSettings)
	return func() {
		st.mu.Lock()
		st.busy = false
		st.mu.Unlock()
		s.em.emit(TopicSettings)
	}, true
}

var errWebDAVBusy = i18n.Error("正在进行其他备份操作，请稍候。")

// TestWebDAV implements API.TestWebDAV: connects, signs in and makes sure
// the remote directory exists (creating it).
func (s *Service) TestWebDAV() (err error) {
	defer func() { s.logOp("test webdav", err) }()
	c, set, err := s.webdavClient(true)
	if err != nil {
		return err
	}
	done, ok := s.webdavBegin()
	if !ok {
		return errWebDAVBusy
	}
	defer done()
	ctx, cancel := s.webdavCtx()
	defer cancel()
	if err := c.mkcol(ctx, webdavDir(set)); err != nil {
		return err
	}
	found, err := c.stat(ctx, webdavDir(set)+"/")
	if err != nil {
		return err
	}
	if !found {
		return errors.New(i18n.T("目录创建后仍不可见，服务器可能不支持 WebDAV。"))
	}
	return nil
}

func (s *Service) webdavCtx() (context.Context, context.CancelFunc) {
	return s.ctrlCtx(nil, 5*time.Minute)
}

// backupDocument is a backup: the export with the subscription links.
func (s *Service) backupDocument() ([]byte, error) {
	return s.encodeExport(true), nil
}

// backupName is the file name of a backup taken at t.
func backupName(t time.Time) string {
	return backupPrefix + t.Format(backupTimeLayout) + backupSuffix
}

// isBackupName reports whether name is one of ours (.yaml, or .json
// from earlier versions).
func isBackupName(name string) bool {
	return backupStem(name) != ""
}

// backupStem returns the time part of a backup name, "" for other names.
// The time part must be a time: RestoreBackup and DeleteBackup take the
// name from the caller (ctl and MCP reach them too) and build a local
// path and a server path from it, so nothing like "../" may pass.
func backupStem(name string) string {
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return ""
	}
	for _, suf := range []string{backupSuffix, backupSuffixJSON} {
		stem, ok := strings.CutSuffix(name, suf)
		if !ok {
			continue
		}
		if stem, ok = strings.CutPrefix(stem, backupPrefix); !ok || len(stem) != len(backupTimeLayout) {
			continue
		}
		if _, err := time.Parse(backupTimeLayout, stem); err == nil {
			return stem
		}
	}
	return ""
}

// BackupNow implements API.BackupNow.
func (s *Service) BackupNow() error { return s.backup(false) }

// backup uploads one backup and prunes old ones; auto marks the
// scheduler's runs for the log and the error memory.
func (s *Service) backup(auto bool) (err error) {
	what := "backup"
	if auto {
		what = "automatic backup"
	}
	defer func() {
		s.logOp(what, err)
		if auto {
			st := feat2(s)
			st.mu.Lock()
			st.lastError = ""
			if err != nil {
				st.lastError = err.Error()
			}
			st.mu.Unlock()
		}
	}()
	c, set, err := s.webdavClient(!auto)
	if err != nil {
		return err
	}
	done, ok := s.webdavBegin()
	if !ok {
		return errWebDAVBusy
	}
	defer done()
	data, err := s.backupDocument()
	if err != nil {
		return err
	}
	ctx, cancel := s.webdavCtx()
	defer cancel()
	dir := webdavDir(set)
	if err := c.mkcol(ctx, dir); err != nil {
		return err
	}
	name := backupName(s.d.now())
	if err := c.put(ctx, dir+"/"+name, data); err != nil {
		return fmt.Errorf(i18n.T("上传备份失败：%v"), err)
	}
	s.alog.Infof("webdav: uploaded %s (%d bytes)", name, len(data))

	serr := s.updateSettings(func(set *model.Settings) { set.WebDAV.LastBackupAt = s.d.now() })
	s.em.emit(TopicSettings)
	if serr != nil {
		s.alog.Errorf("webdav: remember backup time: %v", serr)
	}

	if set.Keep > 0 {
		if n, err := s.prune(ctx, c, dir, set.Keep); err != nil {
			s.alog.Warnf("webdav: pruning old backups: %v", err)
		} else if n > 0 {
			s.alog.Infof("webdav: pruned %d old backup(s), keeping %d", n, set.Keep)
		}
	}
	return nil
}

// prune deletes the backups beyond the newest keep; it returns how many.
func (s *Service) prune(ctx context.Context, c *webdavClient, dir string, keep int) (int, error) {
	entries, err := c.list(ctx, dir)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, e := range entries {
		if !e.Dir && isBackupName(e.Name) {
			names = append(names, e.Name)
		}
	}
	sort.SliceStable(names, func(i, j int) bool { return backupStem(names[i]) > backupStem(names[j]) }) // newest first (the name carries the time)
	n := 0
	for _, name := range names[min(keep, len(names)):] {
		if err := c.delete(ctx, dir+"/"+name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ListBackups implements API.ListBackups: newest first.
func (s *Service) ListBackups() (out []model.WebDAVBackup, err error) {
	defer func() { s.logOp("list backups", err) }()
	c, set, err := s.webdavClient(true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.webdavCtx()
	defer cancel()
	dir := webdavDir(set)
	found, err := c.stat(ctx, dir+"/")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil // nothing backed up yet
	}
	entries, err := c.list(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Dir || !isBackupName(e.Name) {
			continue
		}
		b := model.WebDAVBackup{Name: e.Name, Size: e.Size, Modified: e.Modified}
		if t, err := time.ParseInLocation(backupTimeLayout, backupStem(e.Name), time.Local); err == nil {
			b.Modified = t
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return backupStem(out[i].Name) > backupStem(out[j].Name) })
	return out, nil
}

// RestoreBackup implements API.RestoreBackup: download, then import
// like a file the user picked.
func (s *Service) RestoreBackup(name string) (summary string, err error) {
	defer func() { s.logOp("restore backup "+name, err) }()
	if !isBackupName(name) {
		return "", errors.New(i18n.T("不是澜流的备份文件名。"))
	}
	c, set, err := s.webdavClient(true)
	if err != nil {
		return "", err
	}
	done, ok := s.webdavBegin()
	if !ok {
		return "", errWebDAVBusy
	}
	defer done()
	ctx, cancel := s.webdavCtx()
	defer cancel()
	data, err := c.get(ctx, webdavDir(set)+"/"+name)
	if err != nil {
		return "", err
	}
	dir := s.opt.CacheDir
	if dir == "" {
		dir = s.opt.DataDir
	}
	tmp := filepath.Join(dir, "restore-"+name)
	if err := writeFileAtomic(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf(i18n.T("保存下载内容失败：%v"), err)
	}
	defer os.Remove(tmp)
	return s.ImportSettings(tmp)
}

// DeleteBackup implements API.DeleteBackup.
func (s *Service) DeleteBackup(name string) (err error) {
	defer func() { s.logOp("delete backup "+name, err) }()
	if !isBackupName(name) {
		return errors.New(i18n.T("不是澜流的备份文件名。"))
	}
	c, set, err := s.webdavClient(true)
	if err != nil {
		return err
	}
	ctx, cancel := s.webdavCtx()
	defer cancel()
	return c.delete(ctx, webdavDir(set)+"/"+name)
}

// ---- scheduler ----

// autoBackupInterval returns the interval of an AutoBackup value, 0 = off.
func autoBackupInterval(v string) time.Duration {
	switch v {
	case model.WebDAVDaily:
		return 24 * time.Hour
	case model.WebDAVWeekly:
		return 7 * 24 * time.Hour
	}
	return 0
}

// autoBackupDue reports whether an automatic backup should run at now.
func autoBackupDue(set model.WebDAVSettings, lastAttempt, now time.Time) bool {
	iv := autoBackupInterval(set.AutoBackup)
	if iv == 0 || strings.TrimSpace(set.URL) == "" {
		return false
	}
	if !lastAttempt.IsZero() && now.Sub(lastAttempt) < autoBackupRetry {
		return false
	}
	return set.LastBackupAt.IsZero() || now.Sub(set.LastBackupAt) >= iv
}

// webdavTick runs from the system watcher (watch.go): it starts an
// automatic backup in the background when one is due. A server that
// arrived through sync or an import waits for the user's confirmation
// first (errWebDAVUnconfirmed).
func (s *Service) webdavTick() {
	s.mu.Lock()
	set := s.state.settings.WebDAV
	s.mu.Unlock()
	confirmed := s.webdavOriginConfirmed(set)
	st := feat2(s)
	st.mu.Lock()
	due := !st.busy && autoBackupDue(set, st.lastAttempt, s.d.now())
	if due && confirmed {
		st.lastAttempt = s.d.now()
	}
	if due && !confirmed {
		st.lastError = errWebDAVUnconfirmed.Error()
	}
	st.mu.Unlock()
	if !due {
		return
	}
	if !confirmed {
		origin := webdavOrigin(set.URL)
		o := &s.cs.origin
		o.mu.Lock()
		first := o.notified != origin
		o.notified = origin
		o.mu.Unlock()
		if first {
			s.alog.Warnf("webdav: the server changed to %s through sync or an import; automatic backups wait for a confirmation on this machine", origin)
			s.notify(Notice{Kind: "error", Title: i18n.T("自动备份已暂停"), Detail: errWebDAVUnconfirmed.Error()})
			s.em.emit(TopicSettings)
		}
		return
	}
	s.alog.Infof("webdav: automatic %s backup due", set.AutoBackup)
	s.goBackground(func() {
		if err := s.backup(true); err != nil {
			s.notify(Notice{Kind: "error", Title: i18n.T("自动备份到 WebDAV 失败"), Detail: err.Error()})
		}
	})
}
