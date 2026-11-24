package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Configuration sync (docs/config-sync.md). With Settings.Sync on, the
// configuration lives in Dir/lanflow.yaml too, a folder that iCloud
// Drive, OneDrive or another program syncs between machines:
//
//   - local changes (TopicSettings / TopicSubs / TopicRules) mark the file
//     dirty; the loop rewrites it once a second at most, and only when its
//     canonical text changed;
//   - the loop polls the folder; a change that has been stable for the
//     debounce time is read, and recognised by its hash when it is the
//     file this machine wrote or already processed;
//   - another change is decoded, planned, checked with `mihomo -t` and
//     loaded (configapply.go); a refused one leaves everything as it was,
//     and the status (and a notice) say why, with the line;
//   - a refused version stays in the folder untouched (held): this machine
//     stops writing the file until that version loads (a failure that
//     depends on this machine — a port in use, an older core — is tried
//     again with a growing pause), another device replaces it, or the user
//     decides (SetConfigSync with SyncUseLocal, ApplyConfigFile). Writing
//     over it would roll the other devices back to this machine's state.
//     A file in a newer format, or sealed in a way this build does not
//     know, is never written over at all (syncfile.Foreign);
//   - an iCloud placeholder (.lanflow.yaml.icloud) asks iCloud to download
//     the file (brctl download) and waits;
//   - conflict copies ("lanflow 2.yaml") are listed for the user to pick
//     from (ResolveConfigConflict); nothing is deleted automatically.
//
// Lock order: runMu → opMu → settingsMu → mu; cs.mu is a leaf, never held
// across I/O.

const (
	syncPoll          = time.Second
	syncDebounce      = 1500 * time.Millisecond // a change must be this old (and unchanged) before it is read
	syncDownloadRetry = 15 * time.Second        // between iCloud download requests
	syncStateFile     = "sync-state.json"       // last hash per file, so a restart knows what changed meanwhile
	syncBackupDir     = "sync-backups"          // files replaced by an explicit choice

	// A refused version whose failure depends on this machine is loaded
	// again after syncRetryMin, doubling up to syncRetryMax.
	syncRetryMin = time.Minute
	syncRetryMax = time.Hour
	// A version whose composed configuration did not pass the check is
	// loaded again this many times (1, 2 and 4 minutes later: enough for a
	// timeout or a core being replaced), then held like an invalid one.
	syncRetryLimited = 3

	// The file is read and hashed only when its snapshot (size, time)
	// changed since the last read. Two guards against a change the
	// snapshot does not show: a file modified less than syncFreshFile ago
	// is read every step (file systems with coarse timestamps), and every
	// file is read again after syncRecheck.
	syncFreshFile = 3 * time.Second
	syncRecheck   = time.Minute
)

// ErrSyncHeld is what an operation that must rewrite the sync file says
// while a version this machine refused is still in the folder.
var ErrSyncHeld = i18n.Error("同步文件夹里有一个本机未能加载的版本，本机不会覆盖它。请先解决加载失败的原因，或关闭配置同步后重新开启并选择「用本机配置覆盖文件」。")

// errSyncForeign is what choosing this machine's configuration says for a
// file this build must not rewrite (syncfile.Foreign).
var errSyncForeign = i18n.Error("同步文件由更新版本的澜流写入，或使用了本机不支持的加密方式，本机不能覆盖它。请先更新本机的澜流；如确实要弃用这份文件，请先在同步文件夹中手动移走它。")

// heldFile is a version of the sync file this machine refused and that
// is still in the folder. While it is, nothing is written there.
type heldFile struct {
	path, hash string
	foreign    bool      // never to be written over (syncfile.Foreign)
	retryAt    time.Time // when to load it again; zero = the content itself is invalid
	tries      int
}

// ErrConfigSyncChoice is what SetConfigSync returns when the folder
// already has a lanflow.yaml that differs from this machine's
// configuration: the user picks model.SyncUseFile or model.SyncUseLocal.
var ErrConfigSyncChoice = i18n.Error("同步文件夹中已有不同的 lanflow.yaml：请选择使用文件中的配置，还是用本机配置覆盖文件。")

// configSyncState is the in-memory state of configuration sync.
type configSyncState struct {
	// secrets replaces Secrets for this service (tests); nil = Secrets.
	secrets SecretStore
	links   linkSecrets // links this run put into the keychain (configsecrets.go)
	// webdavPW is the WebDAV password (backup.go). It lives here, beside
	// secrets, because both go through secretStore.
	webdavPW secretSlot
	origin   webdavOriginState // the WebDAV server confirmed on this machine (backup.go)
	seal     sealState         // secrets sealed with the sync passphrase (configseal.go)

	// test hooks: home replaces the user's home directory, download the
	// iCloud download request, debounce the stable time (0 = syncDebounce).
	home     string
	download func(path string) error
	debounce time.Duration
	paused   atomic.Bool // the loop skips its steps (tests drive syncStep)

	runMu sync.Mutex // one sync step, enable or apply at a time

	mu        sync.Mutex
	loaded    bool              // persisted hashes read
	dirty     bool              // a local change waits to be written (set at start: compare once)
	hashes    map[string]string // path → hash of the content last written or processed there
	canon     []byte            // the canonical text last written or loaded
	snap      syncfile.Snapshot // the folder as last seen
	snapDir   string
	changedAt time.Time // when snap last changed
	held      heldFile  // a refused version still in the folder (path "" = none)
	// read: the snapshot whose content was last read and dealt with, so an
	// unchanged file is not read and hashed every second
	read      syncfile.Snapshot
	readDir   string
	readAt    time.Time // wall clock; zero = read the file at the next step
	askedAt   time.Time // last iCloud download request
	notified  string    // the error last shown as a notice
	conflicts string    // the conflict set last shown as a notice
	st        model.ConfigSyncStatus
	icloudDir string    // iCloudDir's cached answer
	icloudAt  time.Time // when it was looked up
}

// ---- folders ----

func (s *Service) syncHome() string {
	if s.cs.home != "" {
		return s.cs.home
	}
	h, _ := os.UserHomeDir()
	return h
}

// iCloudDir is the folder of the iCloud mode, "" when iCloud Drive is
// not available here (not macOS, or no iCloud Drive folder). The answer
// is cached for a few seconds: views read it on every frame.
func (s *Service) iCloudDir() string {
	if runtime.GOOS != "darwin" && s.cs.home == "" {
		return ""
	}
	now := time.Now()
	s.cs.mu.Lock()
	if !s.cs.icloudAt.IsZero() && now.Sub(s.cs.icloudAt) < 5*time.Second {
		d := s.cs.icloudDir
		s.cs.mu.Unlock()
		return d
	}
	s.cs.mu.Unlock()
	dir := ""
	drive := syncfile.ICloudDrive(s.syncHome())
	if fi, err := os.Stat(drive); err == nil && fi.IsDir() {
		dir = filepath.Join(drive, syncfile.ICloudSubdir)
	}
	s.cs.mu.Lock()
	s.cs.icloudDir, s.cs.icloudAt = dir, now
	s.cs.mu.Unlock()
	return dir
}

// syncDir is the folder set syncs to, "" when off or unavailable.
func (s *Service) syncDir(set model.ConfigSyncSettings) string {
	switch set.Mode {
	case model.SyncICloud:
		return s.iCloudDir()
	case model.SyncFolder:
		return set.Dir
	}
	return ""
}

// ---- persisted hashes ----

type syncPersist struct {
	Hashes map[string]string `json:"hashes"`
}

func (s *Service) syncStatePath() string { return filepath.Join(s.opt.DataDir, syncStateFile) }

func syncPathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// loadSyncHashes reads the hashes once; cs.mu held.
func (s *Service) loadSyncHashesLocked() {
	if s.cs.loaded {
		return
	}
	s.cs.loaded = true
	s.cs.hashes = map[string]string{}
	data, err := os.ReadFile(s.syncStatePath())
	if err != nil {
		return
	}
	var p syncPersist
	if json.Unmarshal(data, &p) == nil && p.Hashes != nil {
		for k, v := range p.Hashes {
			s.cs.hashes[syncPathKey(k)] = v
		}
	}
}

// rememberHash records the content last written or processed at path.
func (s *Service) rememberHash(path, hash string) {
	key := syncPathKey(path)
	s.cs.mu.Lock()
	s.loadSyncHashesLocked()
	s.cs.hashes[key] = hash
	data, _ := json.Marshal(syncPersist{Hashes: s.cs.hashes})
	s.cs.mu.Unlock()
	if err := writeFileAtomic(s.syncStatePath(), data, 0o600); err != nil {
		s.alog.Warnf("config sync: save state: %v", err)
	}
}

func (s *Service) knownHash(path string) string {
	key := syncPathKey(path)
	s.cs.mu.Lock()
	defer s.cs.mu.Unlock()
	s.loadSyncHashesLocked()
	return s.cs.hashes[key]
}

// ---- loop ----

// configSyncLoop runs the sync steps until shutdown.
func (s *Service) configSyncLoop() {
	off := s.em.add(func(t Topic) {
		switch t {
		case TopicSettings, TopicSubs, TopicRules:
			s.cs.mu.Lock()
			s.cs.dirty = true
			s.cs.mu.Unlock()
		}
	})
	defer off()
	s.cs.mu.Lock()
	s.cs.dirty = true // compare the file with this machine once at start
	s.cs.mu.Unlock()
	t := time.NewTicker(syncPoll)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			if !s.cs.paused.Load() {
				s.syncStep()
			}
		}
	}
}

// syncStep looks at the folder once: loads an external change, asks
// iCloud for a missing file, writes a local change.
func (s *Service) syncStep() {
	set := s.Settings().Sync
	dir := s.syncDir(set)
	if dir == "" {
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { *st = model.ConfigSyncStatus{} })
		return
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	path := filepath.Join(dir, syncfile.FileName)
	s.cs.mu.Lock()
	moved := s.cs.snapDir != "" && syncPathKey(s.cs.snapDir) != syncPathKey(dir)
	s.cs.mu.Unlock()
	if moved {
		s.resetSeal() // another folder: its file says whether it is sealed
	}
	now := s.d.now()
	snap := syncfile.Look(dir)

	s.cs.mu.Lock()
	if syncPathKey(s.cs.snapDir) != syncPathKey(dir) || !snap.Same(s.cs.snap) {
		s.cs.snap, s.cs.snapDir, s.cs.changedAt = snap, dir, now
	}
	debounce := s.cs.debounce
	if debounce == 0 {
		debounce = syncDebounce
	}
	stable := now.Sub(s.cs.changedAt) >= debounce
	dirty := s.cs.dirty
	s.cs.mu.Unlock()
	s.noteConflicts(snap.Conflicts)

	switch {
	case snap.Placeholder:
		s.askDownload(path)
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { st.State = model.SyncStateWaiting })
		return
	case !stable:
		return // still changing (another program writing it): look again later
	case snap.Exists:
		if !s.checkSyncFile(dir, path, snap, now) {
			return // loaded, refused, or held: nothing to write now
		}
	default:
		dirty = true // no file (deleted, or never written): write this machine's
		s.releaseHeld()
		if !s.sealPrimed() {
			s.markSealedPrimed()
		}
	}
	if dirty {
		s.writeSyncFile(dir)
	}
}

// checkSyncFile looks at the sync file's content when it may have changed
// and reports whether the file is this machine's own (written or loaded
// here), so a local change may be written over it. An unknown content is
// loaded; one this machine refused is left alone until its retry is due.
func (s *Service) checkSyncFile(dir, path string, snap syncfile.Snapshot, now time.Time) bool {
	wall := time.Now() // file times are wall-clock times
	s.cs.mu.Lock()
	unchanged := !s.cs.readAt.IsZero() && syncPathKey(s.cs.readDir) == syncPathKey(dir) && snap.Same(s.cs.read) && wall.Sub(s.cs.readAt) < syncRecheck
	held := s.cs.held
	s.cs.mu.Unlock()
	if !s.sealPrimed() {
		unchanged = false // the file's encryption block must be read before anything is written
	}
	sameHeld := held.path != "" && syncPathKey(held.path) == syncPathKey(path)
	waiting := sameHeld && (held.retryAt.IsZero() || now.Before(held.retryAt))
	if unchanged && (!sameHeld || waiting) {
		return !sameHeld
	}
	data, err := os.ReadFile(path)
	if err != nil {
		s.syncFailed(path, "", fmt.Errorf(i18n.T("读取同步文件失败：%v"), err), 0)
		return false
	}
	s.cs.mu.Lock()
	s.cs.read, s.cs.readDir, s.cs.readAt = snap, dir, wall
	if age := wall.Sub(snap.ModTime); age < syncFreshFile {
		s.cs.readAt = time.Time{} // too fresh to trust its time: read again
	}
	s.cs.mu.Unlock()
	h := syncfile.Hash(data)
	switch {
	case h == s.knownHash(path):
		// this machine's own file: learn whether it is sealed before
		// writing over it (the first step after a start)
		s.releaseHeld()
		s.ensureSealPrimed(data)
		return true
	case sameHeld && held.hash == h && waiting:
		return false // the version refused before; still not its turn
	}
	s.loadExternal(path, data, h)
	return false
}

// holdExternal records that the content hash at path was refused and
// stays in the folder: nothing is written there while it does. A
// retryable failure is scheduled to be loaded again.
func (s *Service) holdExternal(path, hash string, data []byte, retry bool, limit int) {
	now := s.d.now()
	s.cs.mu.Lock()
	h := heldFile{path: path, hash: hash, foreign: syncfile.Foreign(data)}
	if retry {
		if s.cs.held.path == path && s.cs.held.hash == hash {
			h.tries = s.cs.held.tries
		}
		if limit > 0 && h.tries >= limit {
			retry = false // tried often enough: it waits for the user now
		}
	}
	if retry {
		wait := syncRetryMax
		if h.tries < 6 {
			wait = min(syncRetryMin<<h.tries, syncRetryMax)
		}
		h.tries++
		h.retryAt = now.Add(wait)
	}
	s.cs.held = h
	s.cs.mu.Unlock()
}

// releaseHeld forgets the refused version: it loaded, was replaced, or
// the user decided.
func (s *Service) releaseHeld() {
	s.cs.mu.Lock()
	s.cs.held = heldFile{}
	s.cs.mu.Unlock()
}

// syncHeld returns the refused version in the way of writing path.
func (s *Service) syncHeld(path string) (heldFile, bool) {
	s.cs.mu.Lock()
	defer s.cs.mu.Unlock()
	return s.cs.held, s.cs.held.path == path
}

// heldNote is the line the status adds to a refusal: what this machine
// does with its own changes meanwhile.
func heldNote(h heldFile) string {
	switch {
	case h.foreign:
		return i18n.T("本机不会改写这份文件；在它被加载之前，本机的配置改动也不会写入同步文件夹。请先更新本机的澜流。")
	case !h.retryAt.IsZero():
		return i18n.T("稍后会自动重试加载。在此之前本机不会覆盖这份文件，本机的配置改动也不会写入同步文件夹；如要以本机配置为准，请关闭配置同步后重新开启并选择「用本机配置覆盖文件」。")
	}
	return i18n.T("本机不会覆盖这份文件，本机的配置改动也暂不写入同步文件夹；请修正文件，或关闭配置同步后重新开启并选择「用本机配置覆盖文件」。")
}

// loadExternal processes a content of the file this machine has not
// seen: loads it, or records why not. A loaded content's hash is
// remembered, so it is not loaded again. A refused one is held instead
// (holdExternal): it is not this machine's file, so it is never written
// over, and it is tried again after a restart — or sooner, when the
// failure was this machine's and not the content's.
func (s *Service) loadExternal(path string, data []byte, hash string) {
	s.alog.Infof("config sync: %s changed (%s)", path, hash[:12])
	doc, warns, err := syncfile.DecodeWarn(data, defaultSettings())
	if err != nil {
		line := 0
		var se *syncfile.Error
		if errors.As(err, &se) {
			line = se.Line
		}
		s.holdExternal(path, hash, data, false, 0)
		s.syncRefused(path, err, line)
		return
	}
	for _, w := range warns {
		s.alog.Warnf("config sync: %s: %v", path, w)
	}
	plain, webdav := s.unsealDoc(doc)
	res, err := s.applyDoc(plain, false)
	if err != nil {
		line := 0
		var de *docError
		if errors.As(err, &de) {
			line = syncfile.LineOf(data, de.path)
		}
		retry, limit := retryable(err)
		s.holdExternal(path, hash, data, retry, limit)
		s.syncRefused(path, err, line)
		return
	}
	s.rememberHash(path, hash)
	s.releaseHeld()
	s.primeSeal(doc)
	s.adoptWebDAVPassword(webdav)
	now := s.d.now()
	missing := s.missingSecrets(doc)
	s.cs.mu.Lock()
	s.cs.canon = syncfile.Encode(doc)
	// a canonical rewrite is due when the file is not (comments, plain
	// links, settings out of range): the writer compares texts
	s.cs.dirty = true
	s.cs.notified = ""
	s.cs.mu.Unlock()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) {
		st.State, st.Error, st.ErrorLine = model.SyncStateOK, "", 0
		st.MissingSecrets, st.WebDAVPasswordMissing = missing.subs, missing.webdav
		if res.Applied {
			st.LastAppliedAt, st.Summary = now, res.Summary
		}
	})
	if res.Applied {
		s.alog.Infof("config sync: loaded %s: %s", path, res.Summary)
		s.notify(Notice{Kind: "toast", Title: i18n.T("已加载同步的配置"), Detail: res.Summary})
	}
}

// syncRefused is syncFailed for a version of the file that was refused
// and is now held: the status also says what happens to local changes.
func (s *Service) syncRefused(path string, err error, line int) {
	h, _ := s.syncHeld(path)
	s.syncFailed(path, h.hash, fmt.Errorf("%w\n%s", err, heldNote(h)), line)
}

// syncFailed records a refused change (or an I/O failure) for the status
// and shows it once as a notice.
func (s *Service) syncFailed(path, hash string, err error, line int) {
	s.alog.Warnf("config sync: %s refused: %v", path, err)
	msg := err.Error()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) {
		st.State, st.Error, st.ErrorLine = model.SyncStateError, msg, line
	})
	s.cs.mu.Lock()
	again := s.cs.notified == msg
	s.cs.notified = msg
	s.cs.mu.Unlock()
	if !again {
		s.notify(Notice{Kind: "error", Title: i18n.T("同步文件未加载，配置保持不变"), Detail: msg})
	}
}

type missingInfo struct {
	subs   []string
	webdav bool
}

// missingSecrets lists what the file references that this machine's
// keychain lacks.
func (s *Service) missingSecrets(doc syncfile.Doc) missingInfo {
	var m missingInfo
	for _, sub := range s.d.store.List() {
		if sub.Source == urlSubscriptionK && sub.URL == "" {
			m.subs = append(m.subs, sub.Name)
		}
	}
	if doc.WebDAVPassword != "" && !s.WebDAVInfo().HasPassword && s.resolveRef(doc.WebDAVPassword) == "" {
		m.webdav = true
	}
	return m
}

// writeSyncFile writes this machine's configuration when its canonical
// text changed since the last write or load. runMu held.
func (s *Service) writeSyncFile(dir string) {
	path := filepath.Join(dir, syncfile.FileName)
	st := s.snapshotLocal()
	if err := s.storeLinkSecrets(st.subs); err != nil {
		s.alog.Warnf("config sync: keychain: %v (the file keeps keychain references; this machine keeps its links)", err)
	}
	doc := buildDoc(st, linksKeychain)
	if s.WebDAVInfo().HasPassword {
		doc.WebDAVPassword = syncfile.WebDAVPasswordRef
	}
	if s.sealOn() {
		pw := ""
		if doc.WebDAVPassword != "" {
			pw, _ = s.loadPassword()
		}
		s.sealDoc(&doc, st.subs, pw)
	}
	data := syncfile.Encode(doc)
	s.cs.mu.Lock()
	s.cs.dirty = false
	same := bytes.Equal(data, s.cs.canon) && s.cs.snap.Exists
	s.cs.mu.Unlock()
	if same {
		return
	}
	h := syncfile.Hash(data)
	if old, err := os.ReadFile(path); err == nil {
		if syncfile.Hash(old) == h {
			// already there (a restart, or another machine wrote the same)
			s.rememberHash(path, h)
			s.cs.mu.Lock()
			s.cs.canon = data
			s.cs.mu.Unlock()
			return
		}
		s.cs.mu.Lock()
		ours := bytes.Equal(old, s.cs.canon)
		s.cs.mu.Unlock()
		if !ours && syncfile.Hash(old) != s.knownHash(path) {
			// A version this machine neither wrote nor loaded: one it
			// refused, or one that arrived since the last look. It is
			// never replaced from here (another device's changes would be
			// rolled back everywhere); the next step loads it, and until
			// it is dealt with the local change waits.
			s.cs.mu.Lock()
			s.cs.dirty = true
			s.cs.readAt = time.Time{}
			s.cs.mu.Unlock()
			s.alog.Warnf("config sync: %s holds a version this machine did not load; not writing over it", path)
			return
		}
		if !ours {
			// a version this machine loaded but did not write (a
			// hand-edited file, now rewritten canonically): keep it
			if err := s.backupSyncFile(path, old); err != nil {
				s.alog.Warnf("config sync: %v", err)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.syncFailed(path, "", fmt.Errorf(i18n.T("无法创建同步文件夹：%v"), err), 0)
		return
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		s.syncFailed(path, "", fmt.Errorf(i18n.T("写入同步文件失败：%v"), err), 0)
		return
	}
	s.rememberHash(path, h)
	now := s.d.now()
	snap := syncfile.Look(dir)
	s.cs.mu.Lock()
	s.cs.canon = data
	s.cs.snap, s.cs.snapDir, s.cs.changedAt = snap, dir, time.Time{}
	s.cs.mu.Unlock()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) {
		st.LastWrittenAt = now
		if st.State != model.SyncStateError || st.Error == "" {
			st.State = model.SyncStateOK
		}
	})
	s.alog.Infof("config sync: wrote %s (%d bytes)", path, len(data))
}

// askDownload asks iCloud to download the file behind a placeholder,
// at most every syncDownloadRetry.
func (s *Service) askDownload(path string) {
	now := s.d.now()
	s.cs.mu.Lock()
	due := s.cs.askedAt.IsZero() || now.Sub(s.cs.askedAt) >= syncDownloadRetry
	if due {
		s.cs.askedAt = now
	}
	dl := s.cs.download
	s.cs.mu.Unlock()
	if !due {
		return
	}
	if dl == nil {
		dl = brctlDownload
	}
	s.alog.Infof("config sync: %s is not downloaded yet; asking iCloud", path)
	if err := dl(path); err != nil {
		s.alog.Warnf("config sync: iCloud download %s: %v", path, err)
	}
}

// brctlDownload asks iCloud Drive to download path (macOS's brctl; the
// download continues after it returns).
func brctlDownload(path string) error {
	if runtime.GOOS != "darwin" {
		return errors.ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/brctl", "download", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// noteConflicts records the conflict copies and shows a notice when a
// new set appears.
func (s *Service) noteConflicts(names []string) {
	key := fmt.Sprint(names)
	s.cs.mu.Lock()
	changed := s.cs.conflicts != key
	s.cs.conflicts = key
	s.cs.mu.Unlock()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) { st.Conflicts = append([]string(nil), names...) })
	if changed && len(names) > 0 {
		s.notify(Notice{Kind: "error", Title: i18n.T("同步文件有冲突副本"),
			Detail: i18n.Tf("同步文件夹里有 %d 个冲突副本（%s）。请在「设置 › 常规 › 配置同步」中选择保留哪一个；澜流不会自动删除任何副本。", len(names), names[0])})
	}
}

// setSyncStatus changes the cached status and emits TopicSettings when
// it changed.
func (s *Service) setSyncStatus(fn func(*model.ConfigSyncStatus)) {
	s.cs.mu.Lock()
	before := s.cs.st
	before.Conflicts = append([]string(nil), before.Conflicts...)
	before.MissingSecrets = append([]string(nil), before.MissingSecrets...)
	fn(&s.cs.st)
	changed := !reflect.DeepEqual(normalStatus(before), normalStatus(s.cs.st))
	s.cs.mu.Unlock()
	if changed {
		s.em.emit(TopicSettings)
	}
}

// normalStatus is st with empty lists as nil, so comparing two statuses
// compares what they say.
func normalStatus(st model.ConfigSyncStatus) model.ConfigSyncStatus {
	if len(st.Conflicts) == 0 {
		st.Conflicts = nil
	}
	if len(st.MissingSecrets) == 0 {
		st.MissingSecrets = nil
	}
	return st
}

// ---- API ----

// ConfigSyncStatus implements API.ConfigSyncStatus (cheap read).
func (s *Service) ConfigSyncStatus() model.ConfigSyncStatus {
	set := s.Settings().Sync
	s.cs.mu.Lock()
	st := s.cs.st
	st.Conflicts = append([]string(nil), st.Conflicts...)
	st.MissingSecrets = append([]string(nil), st.MissingSecrets...)
	s.cs.mu.Unlock()
	st.Mode = set.Mode
	st.ICloudDir = s.iCloudDir()
	st.ICloudAvailable = st.ICloudDir != ""
	st.Dir = s.syncDir(set)
	if st.Dir == "" {
		st.Mode, st.State = model.SyncOff, model.SyncStateOff
		return model.ConfigSyncStatus{State: model.SyncStateOff, ICloudAvailable: st.ICloudAvailable, ICloudDir: st.ICloudDir, Mode: set.Mode}
	}
	st.Path = filepath.Join(st.Dir, syncfile.FileName)
	if st.State == "" {
		st.State = model.SyncStateOK
	}
	s.sealStatus(&st)
	return st
}

// SetConfigSync implements API.SetConfigSync: turns sync on (mode
// model.SyncICloud, or model.SyncFolder with dir) or off. When the folder
// has a lanflow.yaml that differs from this machine's configuration,
// choice says which wins (model.SyncUseFile / SyncUseLocal); without one
// it returns ErrConfigSyncChoice and changes nothing.
func (s *Service) SetConfigSync(mode, dir, choice string) (err error) {
	defer func() { s.logOp("set config sync "+mode, err) }()
	want := model.ConfigSyncSettings{Mode: mode}
	switch mode {
	case model.SyncOff:
		err := s.updateSettings(func(set *model.Settings) { set.Sync = model.ConfigSyncSettings{} })
		s.resetSeal()
		s.releaseHeld()
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { *st = model.ConfigSyncStatus{} })
		s.em.emit(TopicSettings)
		return err
	case model.SyncICloud:
		if s.iCloudDir() == "" {
			return errors.New(i18n.T("没有找到 iCloud 云盘文件夹。请在系统设置中登录 iCloud 并打开 iCloud 云盘。"))
		}
	case model.SyncFolder:
		dir = filepath.Clean(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			return errors.New(i18n.T("请选择同步文件夹。"))
		}
		want.Dir = dir
	default:
		return fmt.Errorf("unknown sync mode %q", mode)
	}
	target := s.syncDir(want)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf(i18n.T("无法创建同步文件夹：%v"), err)
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	path := filepath.Join(target, syncfile.FileName)
	snap := syncfile.Look(target)
	if snap.Placeholder {
		s.askDownload(path)
		return errors.New(i18n.T("iCloud 正在下载同步文件夹中已有的 lanflow.yaml，请稍后再试。"))
	}
	local := s.snapshotLocal()
	localDoc := buildDoc(local, linksKeychain)
	var fileData []byte
	var fileDoc syncfile.Doc
	fileOK := false // the file is there and decodes
	if snap.Exists {
		fileData, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf(i18n.T("读取同步文件失败：%v"), err)
		}
		var derr error
		fileDoc, derr = syncfile.Decode(fileData, defaultSettings())
		fileOK = derr == nil
		same := derr == nil && bytes.Equal(syncfile.Encode(stripPassword(refsOnly(fileDoc))), syncfile.Encode(localDoc))
		switch {
		case same:
			choice = model.SyncUseFile // nothing to merge
		case choice == model.SyncUseFile && derr != nil:
			return fmt.Errorf(i18n.T("同步文件无效，无法使用：%v"), derr)
		case choice == model.SyncUseFile:
			plain, webdav := s.unsealDoc(fileDoc)
			if _, err := s.applyDoc(plain, false); err != nil {
				return fmt.Errorf(i18n.T("同步文件中的配置未能加载：%v"), err)
			}
			s.adoptWebDAVPassword(webdav)
		case choice == model.SyncUseLocal:
			if syncfile.Foreign(fileData) {
				return errSyncForeign // not even on request: see syncfile.Foreign
			}
			if err := s.backupSyncFile(path, fileData); err != nil {
				return err
			}
		default:
			return ErrConfigSyncChoice
		}
	}
	if err := s.updateSettings(func(set *model.Settings) { set.Sync = want }); err != nil {
		return err
	}
	s.releaseHeld() // the user decided which version wins
	switch {
	case snap.Exists && choice == model.SyncUseFile:
		s.primeSeal(fileDoc) // the file's encryption (and this machine's key for it)
	case fileOK && fileDoc.Encryption != nil:
		// This machine's configuration wins, but the file stays sealed:
		// the choice is about the configuration, not about turning the
		// passphrase off for every device (SetSyncPassphrase does that).
		// Without the key this machine writes its links as keychain
		// references and passes the sealed values it cannot open through.
		s.primeSeal(fileDoc)
	default:
		s.resetSeal() // this machine's configuration wins: written with keychain references
		s.markSealedPrimed()
	}
	s.cs.mu.Lock()
	s.cs.snap, s.cs.snapDir, s.cs.changedAt = snap, target, time.Time{}
	s.cs.readAt = time.Time{} // look at the file's content again
	s.cs.dirty = true
	s.cs.notified = ""
	if snap.Exists && choice == model.SyncUseFile {
		s.cs.canon = syncfile.Encode(fileDoc)
	} else {
		s.cs.canon = fileData // backed up (or none): write this machine's configuration now
	}
	s.cs.mu.Unlock()
	if snap.Exists && choice == model.SyncUseFile {
		s.rememberHash(path, syncfile.Hash(fileData))
		m := s.missingSecrets(fileDoc)
		s.setSyncStatus(func(st *model.ConfigSyncStatus) {
			*st = model.ConfigSyncStatus{State: model.SyncStateOK, LastAppliedAt: s.d.now(), MissingSecrets: m.subs, WebDAVPasswordMissing: m.webdav}
		})
	} else {
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { *st = model.ConfigSyncStatus{State: model.SyncStateOK} })
	}
	s.writeSyncFile(target)
	s.em.emit(TopicSettings)
	return nil
}

func stripPassword(d syncfile.Doc) syncfile.Doc { d.WebDAVPassword = ""; return d }

// refsOnly is d with sealed links written as keychain references, to
// compare a sealed file with this machine's configuration.
func refsOnly(d syncfile.Doc) syncfile.Doc {
	d.Encryption = nil
	d.Subscriptions = append([]syncfile.Subscription(nil), d.Subscriptions...)
	for i, sub := range d.Subscriptions {
		if syncfile.IsSealed(sub.URL) {
			d.Subscriptions[i].URL = syncfile.SubscriptionRef(sub.ID)
		}
	}
	return d
}

// backupSyncFile keeps a copy of a file about to be replaced by the
// user's explicit choice in <DataDir>/sync-backups.
func (s *Service) backupSyncFile(name string, data []byte) error {
	dst := filepath.Join(s.opt.DataDir, syncBackupDir, s.d.now().Format("20060102-150405.000")+"-"+filepath.Base(name))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf(i18n.T("备份原有同步文件失败：%v"), err)
	}
	if err := writeFileAtomic(dst, data, 0o600); err != nil {
		return fmt.Errorf(i18n.T("备份原有同步文件失败：%v"), err)
	}
	return nil
}

// ResolveConfigConflict implements API.ResolveConfigConflict: the user
// keeps the conflict copy name (keepCopy: it is loaded and becomes
// lanflow.yaml) or the current file. The copy not kept is moved to
// <DataDir>/sync-backups, not deleted.
func (s *Service) ResolveConfigConflict(name string, keepCopy bool) (err error) {
	defer func() { s.logOp("resolve config conflict "+name, err) }()
	dir := s.syncDir(s.Settings().Sync)
	if dir == "" {
		return errors.New(i18n.T("配置同步未开启。"))
	}
	if !syncfile.IsConflictCopy(name) || filepath.Base(name) != name {
		return errors.New(i18n.T("不是同步文件的冲突副本。"))
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	copyPath := filepath.Join(dir, name)
	data, err := os.ReadFile(copyPath)
	if err != nil {
		return fmt.Errorf(i18n.T("读取冲突副本失败：%v"), err)
	}
	path := filepath.Join(dir, syncfile.FileName)
	if keepCopy {
		doc, err := syncfile.Decode(data, defaultSettings())
		if err != nil {
			return fmt.Errorf(i18n.T("冲突副本无效：%v"), err)
		}
		if cur, err := os.ReadFile(path); err == nil && syncfile.Foreign(cur) {
			return errSyncForeign // lanflow.yaml itself must not be rewritten
		}
		plain, webdav := s.unsealDoc(doc)
		if _, err := s.applyDoc(plain, false); err != nil {
			return fmt.Errorf(i18n.T("冲突副本中的配置未能加载：%v"), err)
		}
		s.primeSeal(doc)
		s.adoptWebDAVPassword(webdav)
		cur, err := os.ReadFile(path)
		if err == nil {
			if err := s.backupSyncFile(path, cur); err != nil {
				return err
			}
		}
		s.releaseHeld() // the user decided: the copy replaces lanflow.yaml
		s.cs.mu.Lock()
		s.cs.canon = cur // backed up: rewrite lanflow.yaml from what was loaded
		s.cs.mu.Unlock()
	}
	if err := s.backupSyncFile(copyPath, data); err != nil {
		return err
	}
	if err := os.Remove(copyPath); err != nil {
		return fmt.Errorf(i18n.T("移走冲突副本失败：%v"), err)
	}
	s.writeSyncFile(dir)
	s.noteConflicts(syncfile.Look(dir).Conflicts)
	return nil
}
