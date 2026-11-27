package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/fsx"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

const (
	startTimeout    = 40 * time.Second
	validateTimeout = 20 * time.Second
	ctrlTimeout     = 10 * time.Second
	autoRestartGap  = 60 * time.Second // a second crash within this window is not restarted
)

var errNoSubscription = i18n.Error("还没有订阅。请先在「订阅」页导入订阅，再启动内核。")

// ---- composition ----

// composeArgs selects the inputs of one composition. Zero values mean
// "use what is stored"; pointers override one layer (override preview/save).
type composeArgs struct {
	subID string // "" = current subscription
	// ov, when set, takes the place of the override-list entry with its ID
	// (appended when new) for this composition (override preview/save).
	ov *model.Override
	// ovs, when set, is the whole override list for this composition
	// (loading lanflow.yaml) instead of the stored one.
	ovs *[]model.Override
	// subOverrides, when set, is the subscription's override selection for
	// this composition instead of the stored one.
	subOverrides   *[]string
	custom         []model.CustomRule
	useRules       bool               // custom is authoritative even when nil
	manualNodes    []model.ManualNode // overrides stored manual nodes when useManualNodes
	useManualNodes bool
	chains         []model.ProxyChain // overrides stored proxy chains when useChains
	useChains      bool
	settings       *model.Settings // nil = current settings
	content        []byte          // nil = stored content of subID
}

// buildInput assembles profile.Input from the store and settings. It reads
// the store (disk) and must not be called with mu held.
func (s *Service) buildInput(a composeArgs) (profile.Input, model.Subscription, error) {
	manualNodes := s.d.store.ManualNodes()
	if a.useManualNodes {
		manualNodes = a.manualNodes
	}
	manualNodes = readyManualNodes(manualNodes)
	var sub model.Subscription
	var ok bool
	if a.subID == "" {
		sub, ok = s.d.store.Current()
	} else {
		sub, ok = s.d.store.Get(a.subID)
	}
	if !ok && (a.subID != "" || len(manualNodes) == 0) {
		return profile.Input{}, sub, errNoSubscription
	}
	data := a.content
	if data == nil && ok {
		var err error
		data, err = s.d.store.Content(sub.ID)
		if err != nil {
			return profile.Input{}, sub, fmt.Errorf(i18n.T("读取订阅「%s」的内容失败：%v"), sub.Name, err)
		}
	}
	s.mu.Lock()
	set := copySettings(s.state.settings)
	probe := s.state.probePort
	helperOK := s.state.helperOK
	co := s.state.coexist
	tsInfo := s.state.tailscale
	sysDNS := s.state.net.DNS
	s.mu.Unlock()
	if a.settings != nil {
		set = *a.settings
	}
	layerSub := sub
	if a.subOverrides != nil {
		layerSub.Overrides = *a.subOverrides
	}
	list := s.d.store.Overrides()
	if a.ovs != nil {
		list = *a.ovs
	}
	in := profile.Input{
		Subscription: data,
		Overrides:    profile.Layers(substituteOverride(list, a.ov), layerSub),
		CustomRules:  s.d.store.CustomRules(),
		ManualNodes:  manualNodes,
		ProxyChains:  s.d.store.ProxyChains(),
		DNS:          set.DNS,
		Sniffer:      set.Sniffer,
		Geo:          mirroredGeo(set), // the download mirror applies to geox-url (mirror.go)
		Managed: profile.Managed{
			MixedPort: set.MixedPort,
			AllowLAN:  set.AllowLAN,
			Mode:      set.Mode,
			LogLevel:  set.LogLevel,
			TUN:       set.TUN && helperOK,
			TUNStack:  composeTUNStack(set.TUNStack, set.CoreVersion),
			ProbePort: probe,
			IPv6:      set.Advanced.IPv6,
		},
		Advanced: set.Advanced,
	}
	in.Coexist = coexistInput(co, set, sysDNS)
	in.Tailscale = tailscaleInput(tsInfo, set, sysDNS)
	if n := in.Tailscale.Node; n != nil {
		n.AuthKey = s.tailscaleAuthKey() // the keychain; tailscale_builtin.go
	}
	in.Capture = s.captureInput(set) // capture.go
	if a.useRules {
		in.CustomRules = a.custom
	}
	if a.useChains {
		in.ProxyChains = a.chains
	}
	return in, sub, nil
}

// substituteOverride returns list with o in place of the entry of the same
// ID (appended when none has it); a nil o leaves the list as is.
func substituteOverride(list []model.Override, o *model.Override) []model.Override {
	if o == nil {
		return list
	}
	out := append([]model.Override(nil), list...)
	for i := range out {
		if out[i].ID == o.ID {
			out[i] = *o
			return out
		}
	}
	return append(out, *o)
}

// prepared is a composed and validated config waiting to be committed.
type prepared struct {
	out profile.Output
	tmp string // a uniquely named temp file next to runtime.yaml
}

// discard removes a prepared file that will not be committed.
func (p prepared) discard() {
	if p.tmp != "" {
		os.Remove(p.tmp)
	}
}

// prepare composes and validates (mihomo -t) the config, writing it to a
// uniquely named temp file next to the runtime file, so concurrent prepares
// (an import's check, a reload) never share or delete each other's file.
// The current runtime file is untouched; commit or discard the result.
func (s *Service) prepare(a composeArgs) (prepared, error) {
	in, _, err := s.buildInput(a)
	if err != nil {
		return prepared{}, err
	}
	out, err := s.d.prof.Compose(in)
	if err != nil {
		return prepared{}, err
	}
	for _, w := range out.Warnings {
		s.alog.Infof("compose: %s", w)
	}
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	if bin == "" {
		return prepared{}, errors.New(coreMissingText(s.Settings().CorePath, core.ErrNotFound))
	}
	if err := os.MkdirAll(s.workDir, 0o700); err != nil {
		return prepared{}, fmt.Errorf(i18n.T("无法创建内核工作目录：%v"), err)
	}
	f, err := fsx.CreateTemp(s.runtime, 0o600)
	if err != nil {
		return prepared{}, fmt.Errorf(i18n.T("写入运行时配置失败：%v"), err)
	}
	p := prepared{out: out, tmp: f.Name()}
	_, werr := f.Write(out.YAML)
	if werr == nil {
		werr = f.Sync() // commit is then a bare rename
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		p.discard()
		return prepared{}, fmt.Errorf(i18n.T("写入运行时配置失败：%v"), werr)
	}
	ctx, cancel := s.svcCtx(validateTimeout)
	defer cancel()
	if err := s.d.core.Validate(ctx, bin, s.workDir, p.tmp); err != nil {
		p.discard()
		return prepared{}, fmt.Errorf(i18n.T("配置未通过内核校验：%v"), err)
	}
	return p, nil
}

// commit moves the prepared file over runtime.yaml in one rename, keeping a
// copy of the previous one as runtime.yaml.bak, and records the composed
// lines. runtime.yaml exists at every moment.
func (s *Service) commit(p prepared) error {
	bak := s.runtime + ".bak"
	if old, err := os.ReadFile(s.runtime); err == nil {
		if err := fsx.WriteFile(bak, old, 0o600); err != nil {
			p.discard()
			return fmt.Errorf(i18n.T("备份运行时配置失败：%v"), err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		p.discard()
		return fmt.Errorf(i18n.T("备份运行时配置失败：%v"), err)
	} else {
		os.Remove(bak) // no previous config: nothing to roll back to
	}
	if err := fsx.Rename(p.tmp, s.runtime); err != nil {
		p.discard()
		return fmt.Errorf(i18n.T("写入运行时配置失败：%v"), err)
	}
	icons := groupIconsOf(p.out.YAML)
	rd := parseRuntimeDNS(p.out.YAML)
	s.mu.Lock()
	s.state.runtimeYAML = p.out.YAML
	s.state.runtimeDNS = rd
	s.state.runtimeLines = nil // annotated on demand (RuntimeConfig): one entry per line adds up
	s.state.groupIcons = icons
	s.state.dialers = dialersOf(p.out.YAML)
	s.mu.Unlock()
	s.noteChainWarnings(p.out.ChainNotes)
	return nil
}

// rollback restores runtime.yaml.bak over runtime.yaml (one rename) after a
// failed reload; without a backup there is nothing to restore.
func (s *Service) rollback() error {
	bak := s.runtime + ".bak"
	if _, err := os.Stat(bak); err != nil {
		return nil
	}
	if err := fsx.Rename(bak, s.runtime); err != nil {
		return fmt.Errorf("restore runtime config: %w", err) // logged only
	}
	return nil
}

// ---- core lifecycle ----

// StartCore composes, validates and starts the core.
func (s *Service) StartCore() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.startLocked()
}

// startLocked starts the core; opMu must be held. A running core is restarted.
func (s *Service) startLocked() (err error) {
	defer func() { s.logOp("start core", err) }()
	if s.rootCtx.Err() != nil {
		return errors.New(i18n.T("澜流正在退出，内核不再启动。"))
	}
	s.mu.Lock()
	s.resolveBinaryLocked()
	bin := s.state.coreBin
	set := copySettings(s.state.settings)
	helperOK := s.state.helperOK
	prev := s.state.st.Core
	if bin != "" {
		s.state.st.Core = model.CoreStarting
		s.state.st.CoreError = ""
	}
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	if bin == "" {
		return errors.New(coreMissingText(set.CorePath, core.ErrNotFound))
	}
	fail := s.failStart
	if err := s.waitStartPort(set, helperOK, prev); err != nil {
		return fail(err, model.CoreError)
	}
	s.detectTailscale() // compose with Tailscale's interface when it is already up
	s.detectCoexist()   // compose with aTrust's routes when it is already running
	p, err := s.prepare(composeArgs{})
	if err != nil {
		if errors.Is(err, errNoSubscription) {
			return fail(err, model.CoreStopped)
		}
		return fail(err, model.CoreError)
	}
	if err := s.commit(p); err != nil {
		return fail(err, model.CoreError)
	}
	return s.launchLocked(bin, set, helperOK, prev)
}

// failStart records a failed start in Status and returns err.
func (s *Service) failStart(err error, st model.CoreState) error {
	s.mu.Lock()
	s.state.st.Core = st
	s.state.st.CoreError = err.Error()
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return err
}

// launchLocked starts bin on the committed runtime config and brings the
// streams up; opMu must be held. startLocked calls it after composing;
// the core switch (cores_switch.go) calls it directly so the new binary
// starts on the very same config without a recompose.
func (s *Service) launchLocked(bin string, set model.Settings, helperOK bool, prev model.CoreState) error {
	fail := s.failStart
	if prev == model.CoreRunning {
		s.stopStreams()
	}
	useTUN := set.TUN && helperOK
	if useTUN && prev != model.CoreRunning {
		// e.g. at launch with TUN saved on while another client's TUN runs:
		// start as a plain proxy and say why, rather than break the network.
		if err := s.checkOtherTUN(); err != nil {
			useTUN = false
			s.notify(Notice{Kind: "error", Title: i18n.T("TUN 模式未开启"), Detail: err.Error()})
		}
	}
	s.d.mgr.SetRunner(s.runnerFor(useTUN))
	logFile := s.logFile
	if useTUN {
		logFile = s.tunLogFile()
	}
	spec := core.Spec{Bin: bin, WorkDir: s.workDir, Config: s.runtime, Controller: s.ctrlAddr, LogFile: logFile}
	ctx, cancel := s.svcCtx(startTimeout)
	err := s.d.mgr.Start(ctx, spec)
	cancel()
	if err != nil {
		return fail(fmt.Errorf(i18n.T("内核启动失败：%v。可在「日志」中查看详情，或重新启动。"), err), model.CoreError)
	}
	vctx, vcancel := s.svcCtx(ctrlTimeout)
	version, _ := s.d.ctrl.Version(vctx)
	vcancel()

	s.mu.Lock()
	st := &s.state.st
	st.Core = model.CoreRunning
	st.CoreError = ""
	if version != "" {
		st.CoreVersion = version
	}
	st.StartedAt = s.d.now()
	st.Mode, st.MixedPort, st.AllowLAN = set.Mode, set.MixedPort, set.AllowLAN
	st.TUN = useTUN
	s.state.runningTUN = useTUN
	st.Up, st.Down, st.TotalUp, st.TotalDown, st.Memory, st.Connections = 0, 0, 0, 0, 0, 0
	s.state.up, s.state.down = nil, nil
	s.setConnsLocked(nil)
	s.state.tracker = connTracker{}
	s.state.youDone = false
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicTraffic, TopicConns)

	s.syncDNS()
	s.startStreams()
	s.afterConfigChange()
	s.scheduleMainProbe()
	s.goBackground(s.fetchYou)
	return nil
}

// tunLogFile is where the helper-run (root) core logs. The helper only lets
// root write inside the data directory, so the log lives next to the
// runtime config; a link in the log directory keeps it one click away.
func (s *Service) tunLogFile() string {
	p := filepath.Join(s.workDir, "mihomo-tun.log")
	link := filepath.Join(s.opt.LogDir, "mihomo-tun.log")
	if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
		_ = os.Symlink(p, link) // best effort (may need privileges on Windows)
	}
	return p
}

// noteCoreStopped records when a helper-run (root) core went away, so the
// next start waits long enough for its TIME_WAIT sockets (portwait.go).
func (s *Service) noteCoreStopped() {
	s.mu.Lock()
	helper := s.state.tunRunner
	s.mu.Unlock()
	if helper {
		s.noteRootStopped()
	}
}

// runnerFor picks the exec or helper runner and remembers which one is active.
func (s *Service) runnerFor(tun bool) core.Runner {
	s.mu.Lock()
	s.state.tunRunner = tun
	s.mu.Unlock()
	if tun {
		return s.d.core.HelperRunner()
	}
	return s.d.core.ExecRunner()
}

// StopCore stops the core and turns the system proxy off if Lanflow set it.
func (s *Service) StopCore() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.stopLocked()
}

// stopLocked stops the core; opMu must be held.
func (s *Service) stopLocked() (err error) {
	defer func() { s.logOp("stop core", err) }()
	s.stopStreams()
	s.mu.Lock()
	ours := s.state.weSetProxy
	s.mu.Unlock()
	var proxyErr error
	if ours {
		if proxyErr = s.releaseProxy(); proxyErr == nil {
			s.mu.Lock()
			s.state.weSetProxy = false
			s.state.st.SystemProxy = false
			s.mu.Unlock()
		}
	}
	if proxyErr == nil {
		// Nothing points at the PAC file any more. After a failed release
		// the server stays: the system still fetches the file, whose last
		// resort is DIRECT.
		s.stopPAC()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = s.d.mgr.Stop(ctx)
	cancel()
	if err == nil {
		s.noteCoreStopped()
	}
	s.mu.Lock()
	st := &s.state.st
	st.Core = model.CoreStopped
	st.CoreError = ""
	if err != nil {
		// The core may still run: say so instead of showing it stopped.
		st.Core = model.CoreError
		st.CoreError = fmt.Sprintf(i18n.T("停止内核失败：%v"), err)
	}
	st.TUN = false
	s.state.runningTUN = false
	st.Up, st.Down, st.Memory, st.Connections = 0, 0, 0, 0
	s.setConnsLocked(nil)
	s.state.routes = nil
	s.state.tracker = connTracker{}
	if s.state.coreBin == "" {
		st.Core = model.CoreMissing
		st.CoreError = coreMissingText(s.state.settings.CorePath, core.ErrNotFound)
	}
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicConns, TopicExits, TopicProxies)
	s.releaseDNS()
	if err != nil {
		return fmt.Errorf(i18n.T("停止内核失败：%v"), err)
	}
	if proxyErr != nil {
		return fmt.Errorf(i18n.T("内核已停止，但关闭系统代理失败：%v。请在系统设置中手动关闭代理。"), proxyErr)
	}
	return nil
}

// RestartCore restarts the core, keeping the system proxy as is.
func (s *Service) RestartCore() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.restartLocked()
}

// portTaken explains a mixed port held by another program, naming it
// when it can be learned.
func (s *Service) portTaken(port int) error {
	if s.d.portOwner != nil {
		if owner := portOwnerText(s.d.portOwner(port)); owner != "" {
			return fmt.Errorf(i18n.T("代理端口 %d 已被其他程序 %s 占用。请在「设置 › 代理」中换一个端口，或先退出占用它的程序"), port, owner)
		}
	}
	return portTakenError(port)
}

// portTakenError explains a mixed port held by another program.
func portTakenError(port int) error {
	return fmt.Errorf(i18n.T("代理端口 %d 已被其他程序占用（可能是另一款代理软件）。请在「设置 › 代理」中换一个端口，或先退出占用它的程序"), port)
}

// ensureStarted starts the core unless it is already running (or starting
// through another operation, which holds opMu). Unlike StartCore it never
// restarts a running core, so the launch-time auto start cannot race with
// e.g. the first subscription import and bounce the core.
func (s *Service) ensureStarted() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.running() {
		return nil
	}
	return s.startLocked()
}

// restartLocked stops (without touching the system proxy) and starts again.
// When the start fails the core stays down, so the system proxy and DNS
// are let go as after a crash (coreGoneCleanupLocked) rather than left
// pointing at a core that is not there; the error says so.
func (s *Service) restartLocked() (err error) {
	defer func() { s.logOp("restart core", err) }()
	s.stopStreams()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	serr := s.d.mgr.Stop(ctx)
	cancel()
	if serr == nil {
		s.noteCoreStopped()
	}
	if serr != nil {
		// The old core may still own the TUN, sockets and DNS. Do not
		// replace its runner or start a second core over it.
		return s.failStart(fmt.Errorf(i18n.T("停止内核失败：%v"), serr), model.CoreError)
	}
	s.mu.Lock()
	s.state.st.Core = model.CoreStopped
	s.mu.Unlock()
	if err = s.startLocked(); err == nil {
		return nil
	}
	s.mu.Lock()
	s.state.st.Up, s.state.st.Down, s.state.st.Connections = 0, 0, 0
	s.setConnsLocked(nil)
	s.state.routes = nil
	s.mu.Unlock()
	s.em.emit(TopicConns, TopicExits)
	switch g := s.coreGoneCleanupLocked(); {
	case !g.ours:
		return err
	case g.proxyErr != nil:
		return fmt.Errorf(i18n.T("%w。关闭系统代理失败（%v），请在系统设置中手动关闭代理。"), err, g.proxyErr)
	default:
		return fmt.Errorf(i18n.T("%w。系统代理已关闭，内核恢复后请重新打开。"), err)
	}
}

// coreGone is what coreGoneCleanupLocked found and did.
type coreGone struct {
	ours     bool  // Lanflow had set the system proxy
	helper   bool  // the core that went away ran through the helper, even with TUN off
	proxyErr error // the system proxy could not be released: it still points at the core
}

// coreGoneCleanupLocked lets go of everything that points the system at a
// core that is gone (a crash, a restart whose start failed), so the user
// is not left without network: the system proxy Lanflow set is released,
// TUN is marked off and the system DNS put back. A proxy that cannot be
// released stays Lanflow's (weSetProxy), is logged and reported in the
// result. opMu must be held.
func (s *Service) coreGoneCleanupLocked() coreGone {
	s.mu.Lock()
	g := coreGone{ours: s.state.weSetProxy, helper: s.state.tunRunner}
	s.state.runningTUN = false
	s.state.st.TUN = false
	s.mu.Unlock()
	if g.ours {
		if g.proxyErr = s.releaseProxy(); g.proxyErr == nil {
			s.mu.Lock()
			s.state.weSetProxy = false
			s.state.st.SystemProxy = false
			s.mu.Unlock()
			s.stopPAC()
		} else {
			s.alog.Errorf("the core is gone and the system proxy could not be restored: %v", g.proxyErr)
		}
	}
	s.em.emit(TopicStatus)
	s.releaseDNS() // a later start sets it again when TUN comes back
	return g
}

// reloadLocked recomposes, validates and hot-reloads the running core.
// When the core is not running nothing happens: the next start composes
// afresh. On failure the previous runtime file stays in place.
func (s *Service) reloadLocked() error {
	if !s.running() {
		return nil
	}
	p, err := s.prepare(composeArgs{})
	if err != nil {
		s.logOp("reload config", err)
		return err
	}
	return s.reloadPrepared(p)
}

// reloadPath is the file the running core is told to reload: runtime.yaml,
// or, when the helper runs the core, the copy the helper makes of it in
// its own work directory (that core loads nothing outside it, and the
// helper checks the config on the way in).
func (s *Service) reloadPath(ctx context.Context) (string, error) {
	s.mu.Lock()
	tun := s.state.tunRunner
	s.mu.Unlock()
	// On a phone the core always runs in the system VPN, whatever the TUN
	// setting says, and the tunnel rewrites its config (tun.file-descriptor
	// and the rest): every reload goes through the tunnel's runner.
	if !tun && !onPhone() {
		return s.runtime, nil
	}
	st, ok := s.d.core.HelperRunner().(core.ConfigStager)
	if !ok {
		return s.runtime, nil
	}
	path, err := st.StageConfig(ctx, core.Spec{WorkDir: s.workDir, Config: s.runtime})
	if errors.Is(err, core.ErrStageUnsupported) {
		// The core was started by a helper from before this (taken over
		// after an upgrade, until the helper is reinstalled): it runs on
		// runtime.yaml itself.
		return s.runtime, nil
	}
	return path, err
}

// reloadPrepared commits an already validated config and hot-reloads the
// running core with it, so a caller that validated a change before storing
// it (subscription switch, update, edit) does not validate it twice; opMu
// must be held and p must compose what the store now says. A stopped core
// only drops p: the next start composes afresh.
func (s *Service) reloadPrepared(p prepared) error {
	return s.reloadPreparedChecked(p, nil)
}

// check, when present, verifies a runtime change that the core can reject
// without failing the reload request. A failed check reloads the restored
// config as well as restoring it on disk. restoring tells check to verify
// the previous runtime after rollback instead of the requested change.
func (s *Service) reloadPreparedChecked(p prepared, check func(ctx context.Context, restoring bool) error) (err error) {
	if !s.running() {
		p.discard()
		return nil
	}
	defer func() { s.logOp("reload config", err) }()
	s.mu.Lock()
	prevYAML := s.state.runtimeYAML
	s.mu.Unlock()
	if err := s.commit(p); err != nil {
		return err
	}
	ctx, cancel := s.svcCtx(ctrlTimeout * 2)
	path, err := s.reloadPath(ctx)
	attempted := err == nil
	if err == nil {
		err = s.d.ctrl.ReloadConfig(ctx, path)
	}
	if err == nil && check != nil {
		err = check(ctx, false)
	}
	cancel()
	if err != nil {
		var restoreErr error
		if rerr := s.rollback(); rerr != nil {
			restoreErr = rerr
			s.alog.Errorf("reload config: %v", rerr)
		} else if (path != "" && path != s.runtime) || (check != nil && attempted) {
			// The helper's copy is now the config the core refused, and it
			// is the file that core reads when it is told to reload its own
			// config: stage the restored one again.
			rctx, rcancel := s.svcCtx(ctrlTimeout)
			restored, serr := s.reloadPath(rctx)
			if serr != nil {
				restoreErr = serr
				s.alog.Warnf("reload config: restore the helper's copy: %v", serr)
			} else if check != nil && attempted {
				restoreErr = s.d.ctrl.ReloadConfig(rctx, restored)
				if restoreErr == nil {
					restoreErr = check(rctx, true)
				}
			}
			rcancel()
		}
		icons, rd := groupIconsOf(prevYAML), parseRuntimeDNS(prevYAML)
		s.mu.Lock()
		s.state.runtimeYAML, s.state.runtimeLines = prevYAML, nil
		s.state.runtimeDNS, s.state.groupIcons = rd, icons
		s.state.dialers = dialersOf(prevYAML)
		s.mu.Unlock()
		if restoreErr != nil {
			return fmt.Errorf(i18n.T("内核重新加载配置失败：%v；恢复运行配置也失败：%v。请重新启动内核。"), err, restoreErr)
		}
		return fmt.Errorf(i18n.T("内核重新加载配置失败：%v。仍在使用之前的配置。"), err)
	}
	s.mu.Lock()
	set := s.state.settings
	st := &s.state.st
	st.Mode, st.MixedPort, st.AllowLAN = set.Mode, set.MixedPort, set.AllowLAN
	st.TUN = set.TUN && s.state.helperOK && s.state.tunRunner
	s.state.runningTUN = st.TUN
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	s.syncDNS()
	s.afterConfigChange()
	return nil
}

// afterConfigChange refreshes everything the controller reports after a
// start or reload.
func (s *Service) afterConfigChange() {
	s.refreshProxies()
	s.restoreSelections()
	s.pruneExits()
	s.refreshRules()
	s.refreshProviders()
	s.rebuildRoutes()
	s.captureAfterReload()   // capture.go
	s.tailscaleAfterConfig() // tailscale_builtin.go
}

func (s *Service) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.st.Core == model.CoreRunning
}

// ---- crash handling ----

// watchCrashes handles unexpected exits: the system proxy is turned off at
// once (so the user is not left without network), one automatic restart is
// attempted, and the UI is told what happened.
func (s *Service) watchCrashes() {
	ch := s.d.mgr.Crashes()
	if ch == nil {
		return
	}
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case err, ok := <-ch:
			if !ok {
				return
			}
			s.handleCrash(err)
		}
	}
}

func (s *Service) handleCrash(cause error) {
	msg := i18n.T("内核意外退出")
	if cause != nil {
		msg = fmt.Sprintf(i18n.T("内核意外退出：%v"), cause)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.d.mgr.Running() {
		// The report is stale or a duplicate: the operation that held opMu
		// while the core exited has started it again since (a restart, a
		// TUN switch), or the same exit was already handled through the
		// other path (watchCrashes and the wake recheck both report it).
		// The manager marks itself stopped before it reports an exit, so a
		// running manager means this report is not about the current core.
		s.alog.Infof("ignored a stale core exit report, the core is running: %v", cause)
		return
	}
	s.mu.Lock()
	if s.state.st.Core != model.CoreRunning && s.state.st.Core != model.CoreStarting {
		s.mu.Unlock()
		return // a stop we requested raced with the exit report
	}
	s.mu.Unlock()
	s.stopStreams()
	s.mu.Lock()
	last := s.state.lastAutoStart
	now := s.d.now()
	s.state.st.Core = model.CoreError
	s.state.st.CoreError = msg
	s.state.st.Up, s.state.st.Down, s.state.st.Connections = 0, 0, 0
	s.setConnsLocked(nil)
	s.state.routes = nil
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicConns, TopicExits)
	s.recordCoreExit(cause)

	g := s.coreGoneCleanupLocked() // the restart below sets the DNS again when TUN comes back
	if g.helper {
		s.noteRootStopped()
	}
	if errors.Is(cause, core.ErrStateUnknown) && s.d.stray != nil {
		// The helper went silent: the core may well still run and hold
		// its ports, which would fail the restart below.
		sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := s.d.stray.Stop(sctx, s.workDir, g.helper)
		scancel()
		if err != nil {
			// Still silent, so the core cannot be stopped either: a start
			// now would find its ports held and blame another program.
			// Wait for the helper instead.
			s.alog.Warnf("stop the core of unknown state: %v; restarting once the helper answers", err)
			s.awaitHelperLocked(msg, g)
			return
		}
		if g.helper {
			s.noteRootStopped()
		}
	}
	if s.rootCtx.Err() == nil && (last.IsZero() || now.Sub(last) > autoRestartGap) {
		s.mu.Lock()
		s.state.lastAutoStart = now
		s.mu.Unlock()
		if s.restartAfterCrashLocked(g) {
			return
		}
	}
	s.crashedLocked(msg, g)
}

// proxyOffText is what to tell the user about the system proxy when it is
// not restored after the core went away.
func proxyOffText(g coreGone) string {
	if g.proxyErr != nil {
		return i18n.Tf("关闭系统代理失败（%v），请在系统设置中手动关闭代理。", g.proxyErr)
	}
	return i18n.T("系统代理已关闭，请在需要时重新打开。")
}

// restartAfterCrashLocked starts the core again after it went away and
// says so; false when the start failed. opMu must be held.
func (s *Service) restartAfterCrashLocked(g coreGone) bool {
	if err := s.startLocked(); err != nil {
		return false
	}
	detail := proxyOffText(g)
	if g.ours {
		if err := s.setSystemProxyLocked(true); err == nil {
			detail = i18n.T("系统代理已恢复。")
		}
	}
	s.notify(Notice{Kind: "toast", Title: i18n.T("内核意外退出，已自动重新启动"), Detail: detail, System: true}) // docs/design.md §8.4: system notification
	return true
}

// crashedLocked leaves the core in the error state and tells the user.
func (s *Service) crashedLocked(msg string, g coreGone) {
	s.mu.Lock()
	s.state.st.Core = model.CoreError
	s.state.st.CoreError = msg
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	detail := i18n.Tf("%s。系统代理已关闭。可在「日志」中查看原因，或重新启动内核。", msg)
	if g.proxyErr != nil {
		detail = i18n.Tf("%s。%s可在「日志」中查看原因，或重新启动内核。", msg, proxyOffText(g))
	}
	s.notify(Notice{Kind: "core-crash", Title: i18n.T("Mihomo 内核意外退出"), Detail: detail, System: true})
}

// How long and how often a silent helper is asked again (tests shorten).
var (
	helperRecoverEvery  = 5 * time.Second
	helperRecoverWindow = 10 * time.Minute
)

// awaitHelperLocked handles a core of unknown state that could not be
// stopped because the helper still does not answer (a starved machine;
// the helper and the core it runs stall together). It tells the user and
// keeps asking the helper to stop the core; once that works the core is
// started again. It gives up when the user starts or stops the core
// meanwhile, or after helperRecoverWindow. opMu must be held.
func (s *Service) awaitHelperLocked(msg string, g coreGone) {
	s.notify(Notice{Kind: "core-crash", Title: i18n.T("权限组件没有应答"),
		Detail: i18n.Tf("%s。%s澜流会在权限组件恢复应答后自动重新启动内核。", msg, proxyOffText(g)), System: true})
	deadline := time.Now().Add(helperRecoverWindow)
	s.goBackground(func() {
		for {
			select {
			case <-s.rootCtx.Done():
				return
			case <-time.After(helperRecoverEvery):
			}
			if s.recoverFromSilentHelper(msg, g) {
				return
			}
			if time.Now().After(deadline) {
				s.alog.Warnf("the helper did not answer for %s; the core stays stopped", helperRecoverWindow)
				return
			}
		}
	})
}

// recoverFromSilentHelper is one attempt of awaitHelperLocked; true when
// there is nothing left to try.
func (s *Service) recoverFromSilentHelper(msg string, g coreGone) bool {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	waiting := s.state.st.Core == model.CoreError && s.state.st.CoreError == msg
	s.mu.Unlock()
	if !waiting || s.d.mgr.Running() || s.rootCtx.Err() != nil {
		return true // started or stopped by the user meanwhile
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := s.d.stray.Stop(ctx, s.workDir, g.helper)
	cancel()
	if err != nil {
		return false
	}
	s.alog.Infof("the helper answers again: stopped the core of unknown state, starting it")
	if g.helper {
		s.noteRootStopped()
	}
	s.mu.Lock()
	s.state.lastAutoStart = s.d.now()
	s.mu.Unlock()
	if !s.restartAfterCrashLocked(g) {
		s.crashedLocked(msg, g)
	}
	return true
}
