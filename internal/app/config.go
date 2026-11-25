package app

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// SaveSettings persists s and applies what changed. Each change takes the
// cheapest path that applies it:
//
//   - restart (applySettingsRestart): the core binary or TUN changed;
//   - reload (reloadLocked): the composed config changed in a way PATCH
//     /configs cannot apply (needsReload, aTrust coexistence, advanced);
//   - hot patch (applySettingsHot): mode, log level, LAN, ports, sniffing.
//
// The system proxy then follows the port, bypass list and PAC mode
// (followSystemProxy). A failed apply restores the previous settings.
//
// SaveSettings is the path of settings made on this machine (the UI, ctl,
// an agent); settings read from a sync file or an imported file go
// through saveSettingsFrom(in, true).
//
// It replaces every setting: a caller that read the settings, changed an
// option and saves the lot undoes whatever was stored in between. Callers
// that change a few options use UpdateSettings.
func (s *Service) SaveSettings(in model.Settings) error {
	return s.saveSettingsFrom(in, false)
}

// UpdateSettings saves the options that differ between base and next and
// leaves the others as they are stored now (model.MergeSettings): the
// merge runs under opMu, on the settings SaveSettings would replace, and
// the result takes the same path.
func (s *Service) UpdateSettings(base, next model.Settings) error {
	return s.saveSettingsWith(func(cur model.Settings) model.Settings {
		return model.MergeSettings(cur, base, next)
	}, false)
}

// NormalizeSettings is what the service stores when asked to save s:
// invalid values replaced by defaults, and options that follow from
// others set (a port equal to the mixed port cleared). The local
// interface uses it to know all that a settings write of an agent
// changed (internal/rpc, undo).
func NormalizeSettings(s model.Settings) model.Settings {
	return normalizeSettings(model.CloneSettings(s))
}

// settingsWarning is an error of SaveSettings that came after the
// settings were stored and applied to the core: they are in force, and
// only a follow-up step (the system proxy) failed. Callers that undo
// their own work when the settings were refused must not do so for it.
type settingsWarning struct{ err error }

func (w *settingsWarning) Error() string { return w.err.Error() }
func (w *settingsWarning) Unwrap() error { return w.err }

// saveSettingsFrom is SaveSettings; remote marks settings that were not
// made on this machine. A WebDAV server such settings bring is not
// confirmed for automatic backups (backup.go, confirmWebDAVOrigin).
func (s *Service) saveSettingsFrom(in model.Settings, remote bool) error {
	return s.saveSettingsWith(func(model.Settings) model.Settings { return in }, remote)
}

// saveSettingsWith saves what edit makes of the stored settings. edit
// runs with opMu held, so no other save comes between reading the
// settings and storing the result; it gets a copy and must not block.
func (s *Service) saveSettingsWith(edit func(cur model.Settings) model.Settings, remote bool) error {
	if remote {
		s.adoptWebDAVOrigin() // record what was here before it changes
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	old := copySettings(s.state.settings)
	running := s.state.st.Core == model.CoreRunning
	ours := s.state.weSetProxy
	s.mu.Unlock()

	in := normalizeSettings(edit(copySettings(old)))
	if err := s.checkSettingsChange(old, in); err != nil {
		return err
	}
	// persist first so a failed apply still keeps the user's choice
	if err := s.storeSettings(old, in, running); err != nil {
		return err
	}
	if ch := changedSettings(old, in); len(ch) > 0 {
		s.alog.Infof("settings changed: %s", strings.Join(ch, ", "))
	}
	if !remote && webdavOrigin(in.WebDAV.URL) != webdavOrigin(old.WebDAV.URL) {
		s.setWebDAVOrigin(webdavOrigin(in.WebDAV.URL)) // the user set this server here
	}
	if in.CorePath != old.CorePath {
		s.mu.Lock()
		s.resolveBinaryLocked()
		s.mu.Unlock()
		s.em.emit(TopicStatus)
	}
	if in.LogLevel != old.LogLevel && running {
		s.restartLogStream(in.LogLevel)
	}
	if old.CaptureEnabled && !in.CaptureEnabled {
		// captureOffLocked reloads the core, which takes opMu: not here.
		s.goBackground(func() { _ = s.SetCapture(false) })
	}
	tsToggled := in.TailscaleCompat != old.TailscaleCompat || in.TailscaleBuiltin.Enabled != old.TailscaleBuiltin.Enabled
	tsChanged := (tsToggled && s.detectTailscale()) || s.tailscaleSettingsChanged(old, in)
	coexistChanged := (in.ATrustCompat != old.ATrustCompat && s.detectCoexist()) || s.coexistSettingsChanged(old, in) || tsChanged
	if !running {
		return nil // a stopped core picks the new settings up at its next start
	}
	if (in.TUNSetDNS != old.TUNSetDNS || coexistChanged) && in.TUN == old.TUN {
		s.syncDNS()
	}

	needRestart := (in.CorePath != old.CorePath || in.TUN != old.TUN) && s.running()
	needReload := needsReload(old, in) || coexistChanged || advancedNeedsReload(old.Advanced, in.Advanced) ||
		(in.AllowLAN != old.AllowLAN && s.subscriptionListens(in.DNS.TakeOver))
	var err error
	switch {
	case needRestart:
		err = s.restartLocked()
	case needReload:
		err = s.reloadLocked()
	default:
		err = s.applySettingsHot(old, in)
	}
	if err != nil {
		return s.revertSettings(old, in, err)
	}
	if err := s.followSystemProxy(old, in, ours, coexistChanged); err != nil {
		return err
	}
	if in.Mode != old.Mode {
		s.rebuildRoutes()
	}
	return nil
}

// subscriptionListens reports whether the current subscription brings
// listeners of its own (listeners, tunnels, and dns.listen unless Lanflow
// writes the dns section). Composition confines those to this machine
// while 允许局域网 is off (profile/lan.go), and PATCH /configs only moves
// the main inbound ports: with such a subscription the switch needs a
// reload to take effect either way. The test reads keys off the raw text
// (a line that starts with one), so the word in a node's name does not
// count; text in flow style, where keys start no line, counts as a yes.
func (s *Service) subscriptionListens(dnsTakenOver bool) bool {
	cur, ok := s.d.store.Current()
	if !ok {
		return false
	}
	data, err := s.d.store.Content(cur.ID)
	if err != nil {
		return false
	}
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '{' {
		return true
	}
	if subListenersRe.Match(data) {
		return true
	}
	return !dnsTakenOver && subDNSListenRe.Match(data)
}

var (
	subListenersRe = regexp.MustCompile(`(?m)^(?:listeners|tunnels)[ \t]*:`)
	subDNSListenRe = regexp.MustCompile(`(?m)^[ \t]+listen[ \t]*:`)
)

// checkSettingsChange refuses changes that cannot work: TUN without the
// helper or beside another client's TUN, and ports another program holds
// (Mihomo would keep running without its mixed port and only log it).
func (s *Service) checkSettingsChange(old, in model.Settings) error {
	if in.TUN != old.TUN && in.TUN {
		if !s.refreshHelper() {
			return ErrHelperRequired
		}
		if err := s.checkOtherTUN(); err != nil {
			return err
		}
	}
	if in.MixedPort != old.MixedPort && s.d.portInUse(in.MixedPort, in.AllowLAN) {
		return fmt.Errorf(i18n.T("端口 %d 已被其他程序占用（可能是另一款代理软件），已保留原端口 %d。"), in.MixedPort, old.MixedPort)
	}
	for _, p := range []struct{ was, now int }{{old.Advanced.HTTPPort, in.Advanced.HTTPPort}, {old.Advanced.SocksPort, in.Advanced.SocksPort}} {
		if p.now != 0 && p.now != p.was && s.d.portInUse(p.now, in.AllowLAN) {
			return fmt.Errorf(i18n.T("端口 %d 已被其他程序占用，请换一个端口。"), p.now)
		}
	}
	return nil
}

// storeSettings makes in, which was made of old, the current settings in
// memory and on disk. Settings changed without opMu since old was read
// (updateSettings: a finished backup's time, the sync folder, the main
// group) stay unless in changes them too.
func (s *Service) storeSettings(old, in model.Settings, running bool) error {
	s.mu.Lock()
	in = model.MergeSettings(s.state.settings, old, in)
	// The remembered UI state belongs to UpdateUIState: a settings page
	// saving the snapshot it opened with must not roll back a sort order or
	// view picked on another page since.
	in.UI = s.state.settings.UI
	s.state.settings = copySettings(in)
	i18n.SetLang(in.Language)
	if !running {
		s.state.st.Mode, s.state.st.MixedPort, s.state.st.AllowLAN = in.Mode, in.MixedPort, in.AllowLAN
	}
	s.state.proxies = buildSnapshot(s.state.rawProxy, s.state.testing, in.MainGroup)
	s.mu.Unlock()
	s.em.emit(TopicSettings, TopicProxies)
	if err := s.persistSettings(); err != nil {
		s.alog.Errorf("save settings: %v", err)
		return err
	}
	return nil
}

// revertSettings takes the change from old to in back after applying it
// failed, so the UI shows what the core actually runs, and returns err.
func (s *Service) revertSettings(old, in model.Settings, err error) error {
	s.mu.Lock()
	back := model.MergeSettings(s.state.settings, in, old)
	back.UI = s.state.settings.UI
	s.state.settings = copySettings(back)
	s.state.proxies = buildSnapshot(s.state.rawProxy, s.state.testing, back.MainGroup)
	s.mu.Unlock()
	i18n.SetLang(back.Language)
	_ = s.persistSettings()
	s.em.emit(TopicSettings, TopicProxies)
	return err
}

// settingsPatch is the PATCH /configs body for the hot-patchable changes
// between old and in; ok is false when there are none.
func settingsPatch(old, in model.Settings) (patch controller.ConfigPatch, ok bool) {
	if in.Mode != old.Mode {
		m := string(in.Mode)
		patch.Mode, ok = &m, true
	}
	if in.LogLevel != old.LogLevel {
		l := in.LogLevel
		patch.LogLevel, ok = &l, true
	}
	if in.AllowLAN != old.AllowLAN {
		a := in.AllowLAN
		patch.AllowLan, ok = &a, true
	}
	if in.MixedPort != old.MixedPort {
		p := in.MixedPort
		patch.MixedPort, ok = &p, true
	}
	if in.Sniffer.Enable != old.Sniffer.Enable {
		e := in.Sniffer.Enable
		patch.Sniffing, ok = &e, true
	}
	if ap, changed := advancedPatch(old.Advanced, in.Advanced); changed {
		patch.Port, patch.SocksPort, patch.TCPConcurrent, patch.InterfaceName = ap.Port, ap.SocksPort, ap.TCPConcurrent, ap.InterfaceName
		ok = true
	}
	return patch, ok
}

// applySettingsHot patches the running core with the hot-patchable
// changes and updates Status; opMu must be held.
func (s *Service) applySettingsHot(old, in model.Settings) error {
	if patch, ok := settingsPatch(old, in); ok {
		ctx, cancel := s.svcCtx(ctrlTimeout)
		err := s.d.ctrl.PatchConfigs(ctx, patch)
		cancel()
		if err != nil {
			if patch.MixedPort != nil {
				// most likely the port is taken
				return fmt.Errorf(i18n.T("端口 %d 无法使用：%v。已保留原端口 %d。"), in.MixedPort, err, old.MixedPort)
			}
			return fmt.Errorf(i18n.T("应用设置失败：%v。已恢复原设置。"), err)
		}
	}
	s.mu.Lock()
	s.state.st.Mode, s.state.st.MixedPort, s.state.st.AllowLAN = in.Mode, in.MixedPort, in.AllowLAN
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return nil
}

// followSystemProxy points the system proxy Lanflow set (ours) at the new
// port, bypass list or PAC mode.
func (s *Service) followSystemProxy(old, in model.Settings, ours, coexistChanged bool) error {
	if !ours {
		return nil
	}
	fail := func(err error) error {
		return &settingsWarning{fmt.Errorf(i18n.T("设置已保存，但更新系统代理失败：%v"), err)}
	}
	switch {
	case pacChanged(old.Advanced, in.Advanced):
		// Global proxy ↔ PAC: let go of the old form first so the saved
		// state is restored, then set the new one.
		if err := s.releaseProxy(); err != nil {
			return fail(err)
		}
		if err := s.applySystemProxy(); err != nil {
			return fail(err)
		}
	case in.MixedPort != old.MixedPort || !reflect.DeepEqual(in.Bypass, old.Bypass) || coexistChanged || in.Advanced.PACDirectCN != old.Advanced.PACDirectCN:
		if err := s.applySystemProxy(); err != nil {
			return fail(err)
		}
	}
	return nil
}

// needsReload reports whether a settings change touches the composed config
// in a way PATCH /configs cannot apply.
func needsReload(old, in model.Settings) bool {
	oldS, inS := old.Sniffer, in.Sniffer
	oldS.Enable, inS.Enable = false, false
	return !reflect.DeepEqual(old.DNS, in.DNS) ||
		!reflect.DeepEqual(oldS, inS) ||
		!reflect.DeepEqual(old.Geo.GeoIP, in.Geo.GeoIP) ||
		old.Geo.GeoSite != in.Geo.GeoSite || old.Geo.MMDB != in.Geo.MMDB || old.Geo.ASN != in.Geo.ASN ||
		old.Geo.Mode != in.Geo.Mode || old.Geo.AutoUpdate != in.Geo.AutoUpdate || old.Geo.IntervalHours != in.Geo.IntervalHours ||
		old.TUNStack != in.TUNStack
}

// ApplyDNS saves the DNS section and reloads.
func (s *Service) ApplyDNS(d model.DNSSettings) error {
	return s.saveSettingsWith(func(set model.Settings) model.Settings {
		set.DNS = d
		return set
	}, false)
}

// ApplySniffer saves the sniffer section and reloads (or patches the switch).
func (s *Service) ApplySniffer(sn model.SnifferSettings) error {
	return s.saveSettingsWith(func(set model.Settings) model.Settings {
		set.Sniffer = sn
		return set
	}, false)
}

// overrideArgs builds composeArgs that substitute the text of the override
// entry key: an override ID, or the legacy keys "global" (a global entry)
// and a subscription's ID (an entry that subscription selects), created on
// demand (profile.Store.SetOverride).
func (s *Service) overrideArgs(key, text string) (composeArgs, error) {
	o, ok := s.d.store.GetOverride(key)
	switch {
	case ok:
	case key == "global":
		o = model.Override{ID: key, Name: i18n.T("全局覆写"), Enabled: true, Global: true, Source: model.OverrideLocal}
	default:
		sub, found := s.d.store.Get(key)
		if !found {
			return composeArgs{}, errors.New(i18n.T("覆写不存在，可能已被删除。"))
		}
		o = model.Override{ID: key, Name: sub.Name + i18n.T(" 覆写"), Enabled: true, Source: model.OverrideLocal}
	}
	o.Text = text
	args := composeArgs{ov: &o}
	if sub, found := s.d.store.Get(key); found {
		// The legacy per-subscription entry: compose against that subscription,
		// selected as SetOverride will select it.
		args.subID = sub.ID
		if !hasID(sub.Overrides, key) {
			sel := append(append([]string(nil), sub.Overrides...), key)
			args.subOverrides = &sel
		}
	}
	return args, nil
}

// SaveOverride validates the override by composing and running mihomo -t,
// stores it, and reloads when it affects the running config.
func (s *Service) SaveOverride(key, text string) (err error) {
	defer func() { s.logOp("save override "+key, err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if text == "" {
		if _, ok := s.d.store.GetOverride(key); !ok {
			return nil
		}
		return s.deleteOverrideLocked(key)
	}
	args, err := s.overrideArgs(key, text)
	if err != nil {
		return err
	}
	if err := s.validateOverrideLocked(*args.ov, args); err != nil {
		return err
	}
	if err := s.d.store.SetOverride(key, text); err != nil {
		return fmt.Errorf(i18n.T("保存覆写失败：%v"), err)
	}
	return s.applyOverrideChangeLocked(*args.ov)
}

// PreviewOverride diffs the current runtime config against the config
// composed with the new override text.
func (s *Service) PreviewOverride(key, text string) ([]model.DiffLine, error) {
	args, err := s.overrideArgs(key, text)
	if err != nil {
		return nil, err
	}
	in, _, err := s.buildInput(args)
	if err != nil {
		return nil, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return nil, err
	}
	cur, err := s.currentYAML(args.subID)
	if err != nil {
		return nil, err
	}
	return s.d.prof.Diff(cur, out.YAML), nil
}

// currentYAML returns the runtime YAML (the last composed one when the
// subscription matches, else a fresh composition with the stored overrides).
func (s *Service) currentYAML(subID string) ([]byte, error) {
	s.mu.Lock()
	y := s.state.runtimeYAML
	curID := s.state.st.CurrentSubscription
	s.mu.Unlock()
	if y != nil && (subID == "" || subID == curID) {
		return y, nil
	}
	in, _, err := s.buildInput(composeArgs{subID: subID})
	if err != nil {
		return nil, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return nil, err
	}
	return out.YAML, nil
}

// RuntimeConfig returns the last composed runtime config, annotated by layer.
// The annotation is composed on demand (and kept until the next commit)
// rather than with every start and reload: most runs never show it.
func (s *Service) RuntimeConfig() ([]model.RuntimeLine, error) {
	s.mu.Lock()
	lines := s.state.runtimeLines
	s.mu.Unlock()
	if lines != nil {
		return append([]model.RuntimeLine(nil), lines...), nil
	}
	in, _, err := s.buildInput(composeArgs{})
	if err != nil {
		return nil, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.state.runtimeYAML != nil && bytes.Equal(out.YAML, s.state.runtimeYAML) {
		s.state.runtimeLines = out.Lines // the running config: keep it
	}
	s.mu.Unlock()
	return append([]model.RuntimeLine(nil), out.Lines...), nil
}

// UpdateGeoData asks the running core to download the geo databases.
func (s *Service) UpdateGeoData() error {
	if !s.running() {
		return errors.New(i18n.T("请先启动内核，再更新地理数据库。"))
	}
	ctx, cancel := s.svcCtx(5 * time.Minute)
	defer cancel()
	if err := s.d.ctrl.UpdateGeo(ctx); err != nil {
		return fmt.Errorf(i18n.T("更新地理数据库失败：%v"), err)
	}
	err := s.updateSettings(func(set *model.Settings) {
		set.Geo.UpdatedAt = s.d.now()
		s.state.geoInfoAt = time.Time{} // GeoDataInfo re-reads the files
	})
	s.em.emit(TopicSettings)
	return err
}

// changedSettings names the top-level settings that differ (the
// remembered UI state is left out: it changes on every click).
func changedSettings(old, in model.Settings) []string {
	var out []string
	a, b := reflect.ValueOf(old), reflect.ValueOf(in)
	for i := 0; i < a.NumField(); i++ {
		name := a.Type().Field(i).Name
		if name == "UI" || name == "ConnHiddenColumns" {
			continue
		}
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			out = append(out, name)
		}
	}
	return out
}
