//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"lanflow/internal/platform/helper/protocol"
)

// ServiceName is the Windows service name.
const ServiceName = "LanflowHelper"

// Install locations on Windows.
var (
	InstallDir = filepath.Join(programFiles(), "Lanflow Helper")
	HelperBin  = filepath.Join(InstallDir, "lanflow-helper.exe")
	CoreBin    = filepath.Join(InstallDir, "mihomo.exe")
	ConfigPath = filepath.Join(InstallDir, "config.json")
	LogDir     = filepath.Join(programData(), "Lanflow Helper")
	LogPath    = filepath.Join(LogDir, "helper.log")
	// WorkDir is where the helper runs Mihomo (stage.go): inside LogDir,
	// whose DACL admits SYSTEM and Administrators only.
	WorkDir = filepath.Join(LogDir, "work")
)

func programFiles() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0); err == nil && d != "" {
		return d
	}
	if v := os.Getenv("ProgramFiles"); v != "" {
		return v
	}
	return `C:\Program Files`
}

func programData() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0); err == nil && d != "" {
		return d
	}
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}
	return `C:\ProgramData`
}

// Install copies the helper and core into InstallDir, records the config
// and registers/starts the LanflowHelper service. Must run elevated.
//
// Every file is prepared under a temporary name in InstallDir first (the
// core copy is checked against o.CoreSHA256 there); only then is the
// running service stopped and the files moved into place. A failure before
// that changes nothing; a failure after it puts the previous files back
// and starts the previous service again.
func Install(ctx context.Context, o InstallOptions, logf Logf) (err error) {
	if err := o.check(); err != nil {
		return err
	}
	if _, err := windows.StringToSid(o.UID); err != nil {
		return fmt.Errorf("--uid 必须是用户 SID: %w", err)
	}
	dataDir, err := resolveDataDir(o.DataDir, o.UID)
	if err != nil {
		return err
	}
	if err := checkDowngrade(ConfigPath, o); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败（需要管理员权限）: %w", err)
	}
	defer m.Disconnect()

	if err := os.MkdirAll(InstallDir, 0o755); err != nil {
		return err
	}
	if err := PrepareLogDir(); err != nil {
		return err
	}
	var files fileSwap
	defer func() {
		if err != nil {
			files.rollback()
		}
	}()
	if !samePath(self, HelperBin) {
		logf("copy %s -> %s", self, HelperBin)
		tmp, _, err := stageFile(self, InstallDir, 0o755, "")
		if err != nil {
			return fmt.Errorf("复制权限组件失败: %w", err)
		}
		files.add(tmp, HelperBin)
	}
	src := filepath.Clean(o.CoreBin)
	logf("copy %s -> %s (sha256 %s)", src, CoreBin, o.CoreSHA256)
	tmp, sum, err := stageFile(src, InstallDir, 0o755, o.CoreSHA256)
	if err != nil {
		return fmt.Errorf("复制 Mihomo 内核失败: %w", err)
	}
	files.add(tmp, CoreBin)
	cfg, err := encodeConfig(Config{Version: ConfigVersion, Core: CoreBin, CoreSource: src, SHA256: sum, DataDir: dataDir, UID: o.UID, HelperVersion: o.Version})
	if err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if tmp, err = stageBytes(InstallDir, cfg, 0o644); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	files.add(tmp, ConfigPath)

	// Everything is ready: stop the previous service and switch.
	s, err := m.OpenService(ServiceName)
	wasRunning := false
	created := false
	if err == nil {
		if st, qerr := s.Query(); qerr == nil && st.State != svc.Stopped {
			wasRunning = true
		}
		logf("stopping existing service")
		if err := stopService(ctx, s); err != nil {
			s.Close()
			return err
		}
	} else {
		s = nil
	}
	restore := func() {
		if created && s != nil {
			_ = s.Delete()
		}
		files.rollback()
		if !created && s != nil && wasRunning {
			if rerr := s.Start(); rerr != nil {
				logf("restore the previous helper: %v", rerr)
			} else {
				logf("the previous helper runs again")
			}
		}
	}
	if err := files.commit(); err != nil {
		restore()
		if s != nil {
			s.Close()
		}
		return fmt.Errorf("替换权限组件文件失败: %w", err)
	}

	conf := mgr.Config{
		ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
		DisplayName:  "Lanflow Helper",
		Description:  "Lanflow TUN mode privileged helper (runs Mihomo core).",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}
	if s == nil {
		s, err = m.CreateService(ServiceName, HelperBin, conf, "run")
		if err != nil {
			if errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
				s = nil
				restore()
				return errors.New("旧的服务正在等待系统清理（请关闭服务管理器「services.msc」或稍后重试）")
			}
			s = nil
			restore()
			return fmt.Errorf("注册服务失败: %w", err)
		}
		created = true
	} else {
		conf.BinaryPathName = windows.ComposeCommandLine([]string{HelperBin, "run"})
		if err := s.UpdateConfig(conf); err != nil {
			restore()
			s.Close()
			return fmt.Errorf("更新服务配置失败: %w", err)
		}
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)
	if err := s.Start(); err != nil {
		restore()
		return fmt.Errorf("启动服务失败: %w", err)
	}
	if err := waitServiceRunning(ctx, s, 30*time.Second); err != nil {
		_ = stopService(context.Background(), s)
		restore()
		return err
	}
	files.finish()
	logf("service started")
	return nil
}

func waitServiceRunning(ctx context.Context, s *mgr.Service, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		st, err := s.Query()
		if err != nil {
			return fmt.Errorf("查询服务状态失败: %w", err)
		}
		switch st.State {
		case svc.Running:
			return nil
		case svc.Stopped:
			return fmt.Errorf("权限组件服务启动后立即退出，详情见 %s", LogPath)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("权限组件服务未能在 %d 秒内就绪，详情见 %s", int(d/time.Second), LogPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// CleanupLogDir removes LogDir after uninstall logs have been closed.
func CleanupLogDir() { _ = os.RemoveAll(LogDir) }

// Uninstall stops and deletes the service and removes the files.
func Uninstall(ctx context.Context, logf Logf) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败（需要管理员权限）: %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(ServiceName); err == nil {
		if err := stopService(ctx, s); err != nil {
			logf("stop: %v", err)
		}
		if err := s.Delete(); err != nil {
			logf("delete: %v", err)
		}
		s.Close()
	}
	self, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	var firstErr error
	for _, p := range []string{CoreBin, ConfigPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	// Remove leftover backup files or staged files inside InstallDir and WorkDir.
	if matches, err := filepath.Glob(filepath.Join(InstallDir, "*"+oldSuffix)); err == nil {
		for _, match := range matches {
			_ = os.Remove(match)
		}
	}
	_ = os.RemoveAll(WorkDir)
	if err := os.Remove(HelperBin); err != nil && !errors.Is(err, os.ErrNotExist) {
		if samePath(self, HelperBin) {
			// Running from the installed copy itself: schedule deletion on reboot
			// instead of failing uninstall with ERROR_ACCESS_DENIED.
			if p1, e1 := windows.UTF16PtrFromString(HelperBin); e1 == nil {
				_ = windows.MoveFileEx(p1, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
			}
			if p2, e2 := windows.UTF16PtrFromString(InstallDir); e2 == nil {
				_ = windows.MoveFileEx(p2, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
			}
			logf("helper binary is currently running; scheduled %s for removal on reboot", HelperBin)
		} else if firstErr == nil {
			firstErr = err
		}
	}
	_ = os.Remove(InstallDir)
	return firstErr
}

func stopService(ctx context.Context, s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("停止服务失败: %w", err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			// Give the process a moment to release its executable.
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("服务未能在 30 秒内停止")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Listen creates the helper pipe with a DACL for SYSTEM, Administrators
// and the installing user only.
func Listen(sid string) (net.Listener, error) {
	return winio.ListenPipe(protocol.PipeName, &winio.PipeConfig{
		SecurityDescriptor: PipeSDDL(sid),
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
}

// Run is the service entry. Under the SCM it runs as a service; from a
// console (debugging) it serves in the foreground until interrupted.
func Run(ctx context.Context, version string, logf Logf) error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isSvc {
		return serve(ctx, version, logf)
	}
	return svc.Run(ServiceName, &handler{version: version, logf: logf})
}

func serve(ctx context.Context, version string, logf Logf) error {
	return serveReady(ctx, version, logf, nil)
}

// serveReady is serve; ready is closed once the pipe listens (nil = not
// wanted), so the SCM is told "running" only when clients can connect.
func serveReady(ctx context.Context, version string, logf Logf, ready chan<- struct{}) error {
	cfg, err := LoadConfig(ConfigPath)
	if err != nil {
		return err
	}
	sup := NewSupervisor(NewPolicy(cfg))
	sup.Logf = logf
	sup.WorkDir = WorkDir
	l, err := Listen(cfg.UID)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", protocol.PipeName, err)
	}
	if ready != nil {
		close(ready)
	}
	srv := &protocol.Server{Handler: NewHandler(sup, version), Logf: logf}
	logf("lanflow-helper %s listening on %s for %s", version, protocol.PipeName, cfg.UID)
	err = srv.Serve(ctx, l)
	sup.Shutdown()
	return err
}

type handler struct {
	version string
	logf    Logf
}

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending, WaitHint: 15000, CheckPoint: 1}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- serveReady(ctx, h.version, h.logf, ready) }()
	// Running is reported once the pipe listens: a helper that cannot load
	// its config or listen ends as a failed start instead of a service
	// that looks started.
	select {
	case <-ready:
	case err := <-done:
		cancel()
		h.logf("serve: %v", err)
		changes <- svc.Status{State: svc.Stopped}
		return true, 1
	}
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				h.logf("serve: %v", err)
				changes <- svc.Status{State: svc.Stopped}
				return true, 1
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending, WaitHint: uint32((3 * StopGrace) / time.Millisecond), CheckPoint: 1}
				cancel()
				select {
				case <-done:
				case <-time.After(3 * StopGrace):
				}
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		}
	}
}
