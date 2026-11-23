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
	if v := os.Getenv("ProgramFiles"); v != "" {
		return v
	}
	return `C:\Program Files`
}

func programData() string {
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
		files.rollback()
		if s != nil && wasRunning {
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
		DisplayName:  "Lanflow Helper",
		Description:  "澜流 TUN 模式权限组件：以系统权限运行 Mihomo 内核。",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}
	if s == nil {
		s, err = m.CreateService(ServiceName, HelperBin, conf, "run")
		if err != nil {
			s = nil
			restore()
			return fmt.Errorf("注册服务失败: %w", err)
		}
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
	if err := s.Start(); err != nil {
		restore()
		return fmt.Errorf("启动服务失败: %w", err)
	}
	files.finish()
	logf("service started")
	return nil
}

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
	var firstErr error
	for _, p := range []string{HelperBin, CoreBin, ConfigPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	_ = os.Remove(InstallDir)
	_ = os.RemoveAll(LogDir)
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
	changes <- svc.Status{State: svc.StartPending}
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
				changes <- svc.Status{State: svc.StopPending}
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
