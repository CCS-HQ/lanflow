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
	plistPath, logDir, logPath    string

	euid       func() int
	self       func() (string, error)
	launchctl  func(ctx context.Context, args ...string) error
	chown      func(path string, uid, gid int) error
	waitSocket func(ctx context.Context, d time.Duration) error
	// printService returns what launchd reports of the service (launchctl
	// print), "" when it is not loaded.
	printService func(ctx context.Context) string
	// procs finds and ends a helper process that outlives its bootout.
	procs procs
	// exitWait is how long a booted-out helper gets to end on its own
	// before it is killed: launchd's default ExitTimeOut (20 s) plus a
	// margin, so normally launchd ends it first.
	exitWait time.Duration
}

// socketWait is how long install waits for the new helper's socket.
// launchd throttles a daemon that exits soon after starting for 10 s
// before it starts it again, and a helper that finds a core its
// predecessor left behind spends up to 2*StopGrace ending it before it
// listens: both have to fit.
const socketWait = 30 * time.Second

func systemInstaller() *installer {
	return &installer{
		helperDir: HelperDir, helperBin: HelperBin, coreBin: CoreBin,
		configDir: ConfigDir, configPath: ConfigPath,
		plistPath: PlistPath, logDir: LogDir, logPath: LogPath,
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
		launchctl:    launchctl,
		chown:        os.Chown,
		waitSocket:   waitForSocket,
		printService: printService,
		procs:        systemProcs(),
		exitWait:     25 * time.Second,
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
	in.stopService(ctx, logf)
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
	logStart := logSize(in.logPath)
	if err := in.launchctl(ctx, "bootstrap", "system", in.plistPath); err != nil {
		restore()
		return fmt.Errorf("加载 LaunchDaemon 失败: %w", err)
	}
	logf("installed; waiting for socket")
	if err := in.waitSocket(ctx, socketWait); err != nil {
		// What the new helper wrote and what launchd knows of it, taken
		// before it is stopped: the error is all the user gets to see.
		diag := in.diagnose(ctx, logStart)
		logf("the new helper did not start: %s", diag)
		in.stopService(ctx, logf)
		restore()
		return fmt.Errorf("%w（%s）", err, diag)
	}
	files.finish()
	return nil
}

// stopService boots the helper out of launchd and waits until its
// process has ended. launchctl bootout can return while the process is
// still stopping its core (up to 2*StopGrace); a helper started meanwhile
// would find that core in the pid file and spend its own start ending it,
// and the old helper, once done, removed the socket path the new one had
// just created (helpers before 0.1.5 remove it unconditionally), so the
// new helper ran but could not be reached.
func (in *installer) stopService(ctx context.Context, logf Logf) {
	pid := launchdPID(in.printService(ctx))
	_ = in.launchctl(ctx, "bootout", "system/"+Label)
	if pid <= 0 {
		return
	}
	p := in.procs
	if waitGone(ctx, p, pid, in.exitWait) {
		return
	}
	// Only the helper itself is killed: the pid is checked to still run
	// the installed helper binary.
	if exe, err := p.exePath(pid); err != nil || exe != in.helperBin {
		return
	}
	logf("helper pid=%d did not exit after bootout; killing it", pid)
	if err := p.signal(pid, syscall.SIGKILL); err != nil {
		logf("kill pid %d: %v", pid, err)
	}
	if !waitGone(ctx, p, pid, 5*time.Second) {
		logf("helper pid=%d could not be stopped", pid)
	}
}

// diagnose describes a helper that did not start: the last lines it
// wrote to its log since start (where launchd sends its stdout and
// stderr), and launchd's state and last exit status of the service.
func (in *installer) diagnose(ctx context.Context, start int64) string {
	var parts []string
	if tail := logTail(in.logPath, start, 6); tail != "" {
		parts = append(parts, "helper.log: "+tail)
	} else {
		parts = append(parts, "helper.log 没有新内容，进程可能未能启动")
	}
	var st []string
	for _, line := range strings.Split(in.printService(ctx), "\n") {
		line = strings.TrimSpace(line)
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "state", "pid", "runs", "last exit code", "last exit reason", "spawn type", "job state":
			st = append(st, line)
		}
	}
	if len(st) > 0 {
		parts = append(parts, "launchd: "+strings.Join(st, ", "))
	}
	return strings.Join(parts, "；")
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

// plist is the LaunchDaemon. ProcessType is Interactive: Mihomo is the
// helper's child and inherits its scheduling band, and with TUN on it
// carries all of the machine's traffic. As a Background daemon both ran at
// the lowest priority, so a busy machine (a build, a VM) starved them for
// minutes: the network stopped and the app, getting no answer to status,
// reported the core gone.
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
	<string>Interactive</string>
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

// printService returns launchctl print of the service, "" when it is
// not loaded or launchctl fails.
func printService(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "/bin/launchctl", "print", "system/"+Label).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// waitForSocket waits until a helper accepts connections on the socket:
// a socket file alone may be one a previous helper left behind.
func waitForSocket(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if fi, err := os.Lstat(protocol.SocketPath); err == nil && fi.Mode()&os.ModeSocket != 0 {
			if c, err := net.DialTimeout("unix", protocol.SocketPath, time.Second); err == nil {
				_ = c.Close()
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("权限组件已安装但未在 %d 秒内启动，请查看 %s", int(d/time.Second), LogPath)
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
	// The socket path is removed at the end only while it still is this
	// helper's socket: when a new helper is installed it may already have
	// created its own there, which closing this listener (Go unlinks the
	// path on Close by default) or a plain remove would take away.
	own, _ := os.Lstat(protocol.SocketPath)
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	srv := &protocol.Server{Handler: NewHandler(sup, version), Authorize: Authorizer(cfg.UID), Logf: logf}
	logf("lanflow-helper %s listening on %s for uid %s", version, protocol.SocketPath, cfg.UID)
	err = srv.Serve(ctx, l)
	sup.Shutdown()
	removeOwnSocket(protocol.SocketPath, own)
	return err
}

// CleanupLogDir is a no-op on macOS (uninstall removes LogDir directly).
func CleanupLogDir() {}
