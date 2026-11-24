package app

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/helper"
	"lanflow/internal/platform/sysproxy"
)

// SetSystemProxy points the system proxy at the core (starting it first if
// needed) or clears it.
func (s *Service) SetSystemProxy(on bool) (err error) {
	defer func() { s.logOp("system proxy "+onOff(on), err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.setSystemProxyLocked(on)
}

// setSystemProxyLocked implements SetSystemProxy; opMu must be held.
func (s *Service) setSystemProxyLocked(on bool) error {
	if !on {
		if err := s.releaseProxy(); err != nil {
			return fmt.Errorf(i18n.T("关闭系统代理失败：%v。请在系统设置中手动关闭代理。"), err)
		}
		s.stopPAC() // nothing fetches the PAC file any more
		s.mu.Lock()
		s.state.weSetProxy = false
		s.state.st.SystemProxy = false
		s.state.st.PendingRecovery = false
		s.mu.Unlock()
		s.em.emit(TopicStatus)
		return nil
	}
	if !s.running() {
		if err := s.startLocked(); err != nil {
			return err
		}
	}
	if err := s.applySystemProxy(); err != nil {
		return err
	}
	s.mu.Lock()
	s.state.weSetProxy = true
	s.state.st.SystemProxy = true
	s.state.st.PendingRecovery = false
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return nil
}

// applySystemProxy points the system proxy at the core the way the
// settings ask: a PAC file served by Lanflow (设置 › 高级 › PAC 模式) or a
// manual proxy at 127.0.0.1:<mixed port>. The settings in place before
// are remembered in the marker so releaseProxy can put them back.
func (s *Service) applySystemProxy() error {
	s.mu.Lock()
	port := s.state.settings.MixedPort
	bypass := s.bypassLocked()
	adv := s.state.settings.Advanced
	s.mu.Unlock()
	// Remember what was there before (another app's proxy, or off) so
	// turning ours off puts it back instead of just switching it off.
	m, owned := s.readMarker()
	saved := m.Saved
	if !owned {
		saved, _ = s.d.sys.Snapshot() // best effort: without it we fall back to Clear
	}
	if adv.PACMode {
		url, err := s.startPAC(adv.PACPort)
		if err != nil {
			return err
		}
		if err := s.d.sys.SetPAC(url); err != nil {
			return fmt.Errorf(i18n.T("开启 PAC 代理失败：%v。内核仍在运行，可以手动把系统代理指向 127.0.0.1:%d。"), err, port)
		}
		s.writeMarkerPAC(port, saved, url)
		return nil
	}
	if err := s.d.sys.Set("127.0.0.1", port, bypass); err != nil {
		return fmt.Errorf(i18n.T("开启系统代理失败：%v。内核仍在运行，可以手动把系统代理指向 127.0.0.1:%d。"), err, port)
	}
	s.writeMarker(port, saved)
	s.stopPAC() // PAC mode was turned off: the manual proxy replaced the PAC URL
	return nil
}

// checkOtherTUN refuses to turn TUN on while another client's TUN owns the
// default route: two TUNs fight over routes and DNS and break the network.
func (s *Service) checkOtherTUN() error {
	s.mu.Lock()
	ours := s.state.runningTUN
	s.mu.Unlock()
	if ours {
		return nil // the TUN we see is our own
	}
	ctx, cancel := s.svcCtx(3 * time.Second)
	defer cancel()
	if s.d.stray != nil {
		if found, tun := s.d.stray.Find(ctx, s.workDir); found && tun {
			return nil // a core of ours in the helper (a previous run's): its TUN is Lanflow's own
		}
	}
	if name, found := s.d.otherTUN(ctx); found {
		return fmt.Errorf(i18n.T("检测到其他代理软件的 TUN（%s）正在接管网络。两个 TUN 同时开启会互相冲突，请先在那款软件中关闭 TUN 模式，再开启澜流的 TUN"), name)
	}
	return nil
}

// releaseProxy undoes Lanflow's system proxy change. While the system
// proxy still points at us it restores the settings saved before we set it
// (another app's proxy, or off; Clear when no snapshot exists). If someone
// else has changed it since, it is left alone. The marker is removed.
func (s *Service) releaseProxy() error {
	m, _ := s.readMarker()
	port := m.Port
	if port == 0 {
		s.mu.Lock()
		port = s.state.settings.MixedPort
		s.mu.Unlock()
	}
	cur, err := s.d.sys.Get()
	if err == nil && !proxyIsOurs(cur, m, port) {
		s.abandonProxy(m, port) // not ours any more: leave it as it is
		return nil
	}
	if len(m.Saved) > 0 {
		if err := s.d.sys.Restore(m.Saved); err == nil {
			s.removeMarker()
			return nil
		}
	}
	if m.PAC != "" {
		if err := s.d.sys.SetPAC(""); err != nil {
			return err
		}
	} else if err := s.d.sys.Clear(); err != nil {
		return err
	}
	s.removeMarker()
	return nil
}

// dialUpReleaser is the optional part of the system proxy that knows about
// per-connection settings (Windows dial-up / VPN entries).
type dialUpReleaser interface {
	ReleaseDialUp(saved sysproxy.Saved, host string, port int, pac string) error
}

func (realSysProxy) ReleaseDialUp(saved sysproxy.Saved, host string, port int, pac string) error {
	return sysproxy.ReleaseDialUp(saved, host, port, pac)
}

// abandonProxy drops Lanflow's claim on a system proxy that someone else
// has changed: the setting Get reports is left as it is and the marker is
// removed. On Windows the dial-up / VPN entries Lanflow also wrote may
// still point at it, and with the marker the snapshot would be lost, so
// those that still do are put back first (sysproxy.ReleaseDialUp; a no-op
// on other systems).
func (s *Service) abandonProxy(m proxyMarker, port int) {
	if m.Port > 0 {
		port = m.Port
	}
	if r, ok := s.d.sys.(dialUpReleaser); ok {
		_ = r.ReleaseDialUp(m.Saved, "127.0.0.1", port, m.PAC)
	}
	s.removeMarker()
}

// ResolveRecovery answers the "recovery" notice: restore = turn the leftover
// system proxy off; otherwise keep it and start the core.
func (s *Service) ResolveRecovery(restore bool) (err error) {
	defer func() { s.logOp(fmt.Sprintf("recovery restore=%v", restore), err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	pending := s.state.st.PendingRecovery
	s.state.st.PendingRecovery = false
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	if !pending {
		return nil
	}
	if restore {
		if err := s.releaseProxy(); err != nil {
			return fmt.Errorf(i18n.T("关闭系统代理失败：%v。请在系统设置中手动关闭代理。"), err)
		}
		s.mu.Lock()
		s.state.weSetProxy = false
		s.state.st.SystemProxy = false
		s.mu.Unlock()
		s.em.emit(TopicStatus)
		return nil
	}
	if err := s.startLocked(); err != nil {
		return err
	}
	s.reclaimProxyLocked()
	return nil
}

// reclaimProxyLocked takes ownership of the system proxy a previous run
// pointed at Lanflow (the marker); in PAC mode the PAC server comes up
// again at the recorded URL. opMu must be held.
func (s *Service) reclaimProxyLocked() {
	if m, ok := s.readMarker(); ok && m.PAC != "" {
		s.mu.Lock()
		pacPort := s.state.settings.Advanced.PACPort
		s.mu.Unlock()
		if url, err := s.startPAC(pacPort); err == nil && url != m.PAC {
			if err := s.d.sys.SetPAC(url); err == nil {
				s.writeMarkerPAC(m.Port, m.Saved, url)
			}
		}
	}
	s.mu.Lock()
	s.state.weSetProxy = true
	s.state.st.SystemProxy = true
	s.state.st.PendingRecovery = false
	s.mu.Unlock()
	s.em.emit(TopicStatus)
}

// ---- TUN ----

// TUNHelperInstalled reports the last known helper state (refreshed at
// start and after install/uninstall).
func (s *Service) TUNHelperInstalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.helperOK
}

// SetTUN turns TUN mode on or off. On requires the helper; the core is
// restarted through the helper (on) or as the current user (off).
func (s *Service) SetTUN(on bool) (err error) {
	defer func() { s.logOp("TUN "+onOff(on), err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if on && !s.refreshHelper() {
		return ErrHelperRequired
	}
	if on {
		if err := s.checkOtherTUN(); err != nil {
			return err
		}
	}
	var wasRunning, oldTUN bool
	err = s.updateSettings(func(set *model.Settings) {
		oldTUN = set.TUN
		set.TUN = on
		// A failed stop leaves runningTUN set: retry the handoff instead
		// of reporting success after merely changing the saved setting.
		wasRunning = s.state.st.Core == model.CoreRunning || s.state.runningTUN
	})
	s.em.emit(TopicSettings)
	if err != nil {
		return err
	}
	if !wasRunning {
		return nil
	}
	if err := s.restartLocked(); err != nil {
		// Match SaveSettings: a refused transition must not persist the
		// requested mode while the old core may still be running.
		if rerr := s.updateSettings(func(set *model.Settings) { set.TUN = oldTUN }); rerr != nil {
			s.alog.Warnf("TUN: restore the stored mode: %v", rerr)
		}
		s.em.emit(TopicSettings)
		return fmt.Errorf(i18n.T("切换 TUN 模式后重新启动内核失败：%v"), err)
	}
	return nil
}

// InstallTUNHelper installs the privileged helper (administrator prompt).
func (s *Service) InstallTUNHelper() (err error) {
	defer func() { s.logOp("install helper", err) }()
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	if bin == "" {
		return errors.New(i18n.T("未找到 Mihomo 内核，无法安装权限组件。请先在设置 › 内核中选择内核文件。"))
	}
	ctx, cancel := s.svcCtx(2 * time.Minute)
	defer cancel()
	err = s.d.helper.Install(ctx, helper.InstallOptions{HelperBin: s.opt.helperBinary(), CoreBin: bin, DataDir: s.opt.DataDir})
	s.refreshHelper()
	if err != nil {
		return fmt.Errorf(i18n.T("安装权限组件失败：%v"), err)
	}
	return nil
}

// UninstallTUNHelper removes the helper; TUN is turned off first when active.
func (s *Service) UninstallTUNHelper() (err error) {
	defer func() { s.logOp("uninstall helper", err) }()
	s.mu.Lock()
	tunOn := s.state.settings.TUN
	s.mu.Unlock()
	if tunOn {
		if err := s.SetTUN(false); err != nil {
			return err
		}
	}
	ctx, cancel := s.svcCtx(2 * time.Minute)
	defer cancel()
	err = s.d.helper.Uninstall(ctx)
	s.refreshHelper()
	if err != nil {
		return fmt.Errorf(i18n.T("卸载权限组件失败：%v"), err)
	}
	return nil
}

// ---- mode ----

// SetMode switches the routing mode (hot) and persists it. It holds opMu:
// a reload composes from the stored settings, so the mode is stored before
// the core is patched and no reload can run between the two and leave the
// core in another mode than the settings and Status say.
func (s *Service) SetMode(m model.Mode) (err error) {
	defer func() { s.logOp("mode "+string(m), err) }()
	switch m {
	case model.ModeRule, model.ModeGlobal, model.ModeDirect:
	default:
		return fmt.Errorf(i18n.T("未知的模式「%s」"), m)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	var old model.Mode
	saveErr := s.updateSettings(func(set *model.Settings) {
		old = set.Mode
		set.Mode = m
	})
	if s.running() {
		mode := string(m)
		ctx, cancel := s.svcCtx(ctrlTimeout)
		err := s.d.ctrl.PatchConfigs(ctx, controller.ConfigPatch{Mode: &mode})
		cancel()
		if err != nil {
			// The core kept its mode: so do the settings.
			if rerr := s.updateSettings(func(set *model.Settings) { set.Mode = old }); rerr != nil {
				s.alog.Warnf("mode %s: restore the stored mode %s: %v", m, old, rerr)
			}
			s.em.emit(TopicSettings)
			return fmt.Errorf(i18n.T("切换模式失败：%v"), err)
		}
		if m == model.ModeGlobal {
			s.ensureGlobalExit()
		}
	}
	s.mu.Lock()
	s.state.st.Mode = m
	s.mu.Unlock()
	s.em.emit(TopicStatus, TopicSettings)
	s.rebuildRoutes()
	return saveErr
}

// ensureGlobalExit points Mihomo's GLOBAL group at the main group when it
// is still on its default: the group starts on DIRECT, so global mode
// would send everything direct until the user picks a node in it (seen
// in the VM e2e run: every packet left en0 unproxied in global mode).
func (s *Service) ensureGlobalExit() {
	s.refreshProxies()
	px := s.Proxies()
	var g *model.ProxyGroup
	for i := range px.Groups {
		if px.Groups[i].Name == "GLOBAL" {
			g = &px.Groups[i]
		}
	}
	if g == nil || (g.Now != "" && g.Now != "DIRECT" && g.Now != "REJECT") {
		return
	}
	target := ""
	if slices.Contains(g.All, px.MainGroup) {
		target = px.MainGroup
	} else {
		for _, n := range g.All {
			if n != "DIRECT" && n != "REJECT" && n != "REJECT-DROP" && n != "PASS" && n != "COMPATIBLE" {
				target = n
				break
			}
		}
	}
	if target == "" {
		return
	}
	ctx, cancel := s.svcCtx(ctrlTimeout)
	err := s.d.ctrl.SelectProxy(ctx, "GLOBAL", target)
	cancel()
	if err != nil {
		s.alog.Warnf("global mode: GLOBAL left on %s (select %s: %v)", g.Now, target, err)
		return
	}
	s.alog.Infof("global mode: GLOBAL -> %s", target)
	s.refreshProxies()
}
