//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"lanflow/internal/platform/helper/protocol"
)

// Install locations on macOS.
const (
	Label      = "com.lanflow.helper"
	HelperDir  = "/Library/PrivilegedHelperTools"
	HelperBin  = HelperDir + "/com.lanflow.helper"
	CoreBin    = HelperDir + "/com.lanflow.mihomo"
	ConfigDir  = "/Library/Application Support/Lanflow Helper"
	ConfigPath = ConfigDir + "/config.json"
	PlistPath  = "/Library/LaunchDaemons/" + Label + ".plist"
	LogDir     = "/Library/Logs/Lanflow Helper"
	LogPath    = LogDir + "/helper.log"
	// PidPath records the Mihomo the helper runs, in a directory only root
	// writes, so the next helper finds a core this one left behind.
	PidPath = ConfigDir + "/core.pid"
	// WorkDir is where the helper runs Mihomo (stage.go): root's own, 0700,
	// inside ConfigDir, which only root writes.
	WorkDir = ConfigDir + "/work"
)

// installer holds what Install touches outside this process, so tests
// run it against a temporary directory and a fake launchctl.
type installer struct {
	helperDir, helperBin, coreBin string
	configDir, configPath         string
	plistPath, logDir             string

	euid       func() int
	self       func() (string, error)
	launchctl  func(ctx context.Context, args ...string) error
	chown      func(path string, uid, gid int) error
	waitSocket func(ctx context.Context, d time.Duration) error
}

func systemInstaller() *installer {
	return &installer{
		helperDir: HelperDir, helperBin: HelperBin, coreBin: CoreBin,
		configDir: ConfigDir, configPath: ConfigPath,
		plistPath: PlistPath, logDir: LogDir,
		euid: os.Geteuid,
		self: func() (string, error) {
			self, err := os.Executable()
			if err != nil {
				return "", err
			}
			if real, err := filepath.EvalSymlinks(self); err == nil {
				self = real
			}
			return self, nil
		},
		launchctl:  launchctl,
		chown:      os.Chown,
		waitSocket: waitForSocket,
	}
}

// Install copies the helper (the running executable) and the core into
// HelperDir, records the config and (re)loads the LaunchDaemon. Must run
// as root.
func Install(ctx context.Context, o InstallOptions, logf Logf) error {
	return systemInstaller().install(ctx, o, logf)
}

// install prepares every file under a temporary name first (the core copy
// is checked against o.CoreSHA256 there), and only then stops the running
// helper and moves the files into place. A failure before that point
// changes nothing; a failure after it puts the previous files back and
// loads the previous LaunchDaemon again, so a failed upgrade does not
// leave TUN without a helper.
func (in *installer) install(ctx context.Context, o InstallOptions, logf Logf) (err error) {
	if in.euid() != 0 {
		return errors.New("安装权限组件需要管理员权限")
	}
	if err := o.check(); err != nil {
		return err
	}
	if _, err := parseUID(o.UID); err != nil {
		return err
	}
	dataDir, err := resolveDataDir(o.DataDir, o.UID)
	if err != nil {
		return err
	}
	if err := checkDowngrade(in.configPath, o); err != nil {
		return err
	}
	self, err := in.self()
	if err != nil {
		return err
	}
	for _, d := range []string{in.helperDir, in.configDir, in.logDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	var files fileSwap
	defer func() {
		if err != nil {
			files.rollback()
		}
	}()
	// stage owns a prepared file by root:wheel and queues it for dst.
	stage := func(tmp, dst string) error {
		files.add(tmp, dst)
		return in.chown(tmp, 0, 0)
	}
	if self != in.helperBin {
		logf("copy %s -> %s", self, in.helperBin)
		tmp, _, err := stageFile(self, in.helperDir, 0o755, "")
		if err != nil {
			return fmt.Errorf("复制权限组件失败: %w", err)
		}
		if err := stage(tmp, in.helperBin); err != nil {
			return err
		}
	}
	src := filepath.Clean(o.CoreBin)
	logf("copy %s -> %s (sha256 %s)", src, in.coreBin, o.CoreSHA256)
	tmp, sum, err := stageFile(src, in.helperDir, 0o755, o.CoreSHA256)
	if err != nil {
		return fmt.Errorf("复制 Mihomo 内核失败: %w", err)
	}
	if err := stage(tmp, in.coreBin); err != nil {
		return err
	}
	cfg, err := encodeConfig(Config{Version: ConfigVersion, Core: in.coreBin, CoreSource: src, SHA256: sum, DataDir: dataDir, UID: o.UID, HelperVersion: o.Version})
	if err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	for _, f := range []struct {
		data []byte
		dst  string
		what string
	}{{cfg, in.configPath, "配置"}, {[]byte(plist()), in.plistPath, "LaunchDaemon"}} {
		tmp, err := stageBytes(filepath.Dir(f.dst), f.data, 0o644)
		if err != nil {
			return fmt.Errorf("写入%s失败: %w", f.what, err)
		}
		if err := stage(tmp, f.dst); err != nil {
			return err
		}
	}
	for _, d := range []string{in.helperDir, in.configDir} {
		if err := in.chown(d, 0, 0); err != nil {
			return err
		}
	}

	// Everything is ready: stop the previous instance and switch.
	_, statErr := os.Lstat(in.plistPath)
	hadService := statErr == nil
	_ = in.launchctl(ctx, "bootout", "system/"+Label)
	restore := func() {
		files.rollback()
		if hadService {
			if rerr := in.launchctl(ctx, "bootstrap", "system", in.plistPath); rerr != nil {
				logf("restore the previous helper: %v", rerr)
			} else {
				logf("the previous helper runs again")
			}
		}
	}
	if err := files.commit(); err != nil {
		restore()
		return fmt.Errorf("替换权限组件文件失败: %w", err)
	}
	if err := in.launchctl(ctx, "bootstrap", "system", in.plistPath); err != nil {
		restore()
		return fmt.Errorf("加载 LaunchDaemon 失败: %w", err)
	}
	logf("installed; waiting for socket")
	if err := in.waitSocket(ctx, 10*time.Second); err != nil {
		_ = in.launchctl(ctx, "bootout", "system/"+Label)
		restore()
		return err
	}
	files.finish()
	return nil
}

// Uninstall unloads the LaunchDaemon (which stops Mihomo via the helper's
// shutdown) and removes every installed file, the core's work directory
// (inside ConfigDir) included. Must run as root.
func Uninstall(ctx context.Context, logf Logf) error {
	if os.Geteuid() != 0 {
		return errors.New("移除权限组件需要管理员权限")
	}
	if err := launchctl(ctx, "bootout", "system/"+Label); err != nil {
		logf("bootout: %v", err)
	}
	var firstErr error
	for _, p := range []string{PlistPath, HelperBin, CoreBin, ConfigPath, PidPath, protocol.SocketPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	_ = os.RemoveAll(ConfigDir)
	_ = os.RemoveAll(LogDir)
	return firstErr
}

func plist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + HelperBin + `</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + LogPath + `</string>
	<key>StandardErrorPath</key>
	<string>` + LogPath + `</string>
</dict>
</plist>
`
}

func launchctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func waitForSocket(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if fi, err := os.Lstat(protocol.SocketPath); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("权限组件已安装但未在 10 秒内启动，请查看 " + LogPath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Listen creates the helper socket: root-owned directory, chmod 0600 then
// chown to the installing user so only that user (and root) can connect.
func Listen(uid string) (net.Listener, error) {
	n, err := parseUID(uid)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(protocol.SocketPath)
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", protocol.SocketPath)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(protocol.SocketPath, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	if err := os.Chown(protocol.SocketPath, n, -1); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Authorizer returns the per-connection check: the peer's effective uid
// (LOCAL_PEERCRED) must be the installing user or root.
func Authorizer(uid string) func(net.Conn) error {
	return func(conn net.Conn) error {
		peer, err := PeerUID(conn)
		if err != nil {
			return err
		}
		if fmt.Sprint(peer) != uid && peer != 0 {
			return fmt.Errorf("peer uid %d is not the installing user %s", peer, uid)
		}
		return nil
	}
}

// PeerUID returns the effective uid of the process at the other end of a
// unix socket connection.
func PeerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return cred.Uid, nil
}

// Run is the LaunchDaemon entry: load the config, listen, serve until
// SIGTERM/SIGINT, then stop Mihomo and remove the socket.
func Run(ctx context.Context, version string, logf Logf) error {
	cfg, err := LoadConfig(ConfigPath)
	if err != nil {
		return err
	}
	sup := NewSupervisor(NewPolicy(cfg))
	sup.Logf = logf
	// A helper that crashed or was killed left its Mihomo running as root,
	// where nobody can stop it: end it before serving.
	sup.PidFile = PidPath
	sup.WorkDir = WorkDir
	reapOrphan(PidPath, cfg.Core, systemProcs(), logf)
	l, err := Listen(cfg.UID)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", protocol.SocketPath, err)
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	srv := &protocol.Server{Handler: NewHandler(sup, version), Authorize: Authorizer(cfg.UID), Logf: logf}
	logf("lanflow-helper %s listening on %s for uid %s", version, protocol.SocketPath, cfg.UID)
	err = srv.Serve(ctx, l)
	sup.Shutdown()
	_ = os.Remove(protocol.SocketPath)
	return err
}
