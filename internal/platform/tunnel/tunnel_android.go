package tunnel

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"lanflow/internal/core"
	"lanflow/internal/i18n"
	"lanflow/internal/platform/tunnel/android"
)

// Android: Lanflow's VpnService (package android) makes the interface, and
// Mihomo runs as a program, libmihomo.so from the app's native libraries,
// on its descriptor. The core's own connections leave the VPN because the
// service leaves the whole app out of it.
//
// MyGo's build keeps native libraries in the APK, uncompressed, rather than
// extract them at install, and an app may not run programs from its data
// (W^X since Android 10). The core runs from the APK itself: Android's
// dynamic linker, run as a program, loads an executable from a path inside
// an APK ("base.apk!/lib/arm64-v8a/libmihomo.so") as it loads libraries
// from there. Where the library was extracted, it runs from there.

func init() { Register(&androidTunnel{runner: &vpnRunner{exited: make(chan error, 1)}}) }

// coreLibrary is Mihomo's file among the app's native libraries, and
// coreInAPK its path inside the APK.
const (
	coreLibrary = "libmihomo.so"
	coreInAPK   = "lib/arm64-v8a/" + coreLibrary
)

// linker runs an executable that lies inside an APK.
const linker = "/system/bin/linker64"

// command runs the core bin with args: bin is a file, or a path inside the
// APK, "<apk>!/<entry>", which the linker loads.
func command(bin string, args ...string) *exec.Cmd {
	if strings.Contains(bin, "!/") {
		return exec.Command(linker, append([]string{bin}, args...)...)
	}
	return exec.Command(bin, args...)
}

// stagedConfig is the config the core runs on, in its work directory:
// spec.Config made to serve the VPN's descriptor (fdConfig).
const stagedConfig = "android-runtime.yaml"

// tunFD is the descriptor the core finds the VPN at: its first extra file.
const tunFD = 3

var (
	vpnOnce sync.Once
	vpnImpl android.VPN
	vpnErr  error
)

// vpn is the VpnService's module, made at first use: at init the host may
// not be ready to answer.
func vpn() (android.VPN, error) {
	vpnOnce.Do(func() { vpnImpl, vpnErr = android.NewVPN() })
	return vpnImpl, vpnErr
}

type androidTunnel struct{ runner *vpnRunner }

func (t *androidTunnel) Runner() core.Runner { return t.runner }

func (t *androidTunnel) Binary() (string, error) {
	v, err := vpn()
	if err != nil {
		return "", err
	}
	if dir, err := v.NativeLibraryDir(); err == nil {
		if bin := filepath.Join(dir, coreLibrary); fileExists(bin) {
			return bin, nil
		}
	}
	apk, err := v.APKPath()
	if err != nil {
		return "", err
	}
	if !apkHas(apk, coreInAPK) {
		return "", fmt.Errorf("%w: %s!/%s", core.ErrNotFound, apk, coreInAPK)
	}
	return apk + "!/" + coreInAPK, nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// apkHas reports whether the APK holds name, stored uncompressed (which the
// linker needs to map it).
func apkHas(apk, name string) bool {
	z, err := zip.OpenReader(apk)
	if err != nil {
		return false
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == name {
			return f.Method == zip.Store
		}
	}
	return false
}

const cmdTimeout = 30 * time.Second

// versionRe matches the first line of `mihomo -v`, as core does.
var versionRe = regexp.MustCompile(`(?m)^Mihomo(?: Meta)?\s+(\S+)`)

func (t *androidTunnel) Version(ctx context.Context) (string, error) {
	bin, err := t.Binary()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	out, err := run(ctx, command(bin, "-v"))
	if err != nil {
		return "", fmt.Errorf("mihomo -v: %v: %s", err, strings.TrimSpace(out))
	}
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("mihomo -v: %q", strings.TrimSpace(out))
	}
	v := m[1]
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		v = "v" + v
	}
	return v, nil
}

func (t *androidTunnel) Validate(ctx context.Context, workDir, config string) error {
	bin, err := t.Binary()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	cmd := command(bin, "-t", "-d", workDir, "-f", config)
	cmd.Dir = workDir
	out, err := run(ctx, cmd)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New(validateMessage(out))
}

// run runs cmd until it ends or ctx does, and returns its output.
func run(ctx context.Context, cmd *exec.Cmd) (string, error) {
	var buf strings.Builder
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return buf.String(), ctx.Err()
	}
}

// validateMessage keeps the lines of `mihomo -t` that say what is wrong.
func validateMessage(out string) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, "level=error") || strings.Contains(l, "level=fatal") || strings.Contains(l, "failed") || strings.Contains(l, "error") {
			keep = append(keep, strings.TrimSpace(l))
		}
	}
	if len(keep) == 0 {
		return strings.TrimSpace(out)
	}
	return strings.Join(keep, "\n")
}

// ControllerAddress keeps core's: a socket in the app's cache directory
// (TMPDIR, which MyGo's host sets), short enough and the app's alone.
func (t *androidTunnel) ControllerAddress() string { return "" }

// vpnRunner brings the VPN up and runs Mihomo on it. It is also a
// core.ConfigStager: a reload must hand the core the rewritten config, or
// the core would drop the VPN's descriptor.
type vpnRunner struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{} // closed when the core has exited
	exited   chan error
	stopping bool
	cancel   context.CancelFunc // ends the watch of the VPN's events
	why      error              // set when the VPN went down under the core
}

const stopGrace = 5 * time.Second

var (
	errVPNDenied  = i18n.Error("未获得建立 VPN 的许可")
	errVPNRevoked = i18n.Error("VPN 已被关闭（系统设置或其他 VPN 接管）")
	errVPNEnded   = i18n.Error("系统结束了 VPN 服务")
)

// StageConfig writes spec.Config rewritten for the VPN into spec.WorkDir
// and returns its path.
func (r *vpnRunner) StageConfig(_ context.Context, spec core.Spec) (string, error) {
	path, _, err := stage(spec)
	return path, err
}

func stage(spec core.Spec) (string, FDInterface, error) {
	src, err := os.ReadFile(spec.Config)
	if err != nil {
		return "", FDInterface{}, err
	}
	out, iface, err := fdConfig(src, tunFD)
	if err != nil {
		return "", FDInterface{}, err
	}
	path := filepath.Join(spec.WorkDir, stagedConfig)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return "", FDInterface{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", FDInterface{}, err
	}
	return path, iface, nil
}

func (r *vpnRunner) Start(ctx context.Context, spec core.Spec) (err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runningLocked() {
		return errors.New(i18n.T("Mihomo 内核已在运行，请先停止"))
	}
	if spec.Bin == "" || !filepath.IsAbs(spec.WorkDir) || !filepath.IsAbs(spec.Config) || spec.Controller == "" || !filepath.IsAbs(spec.LogFile) {
		return fmt.Errorf("invalid core spec: %+v", spec)
	}
	v, err := vpn()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(spec.WorkDir, 0o700); err != nil {
		return err
	}
	config, iface, err := stage(spec)
	if err != nil {
		return err
	}
	ok, err := v.Prepare(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errVPNDenied
	}

	// Watch the VPN before it is up: an event that came before the watch
	// would be lost.
	wctx, cancel := context.WithCancel(context.Background())
	defer func() {
		if err != nil {
			cancel()
			_ = v.Stop()
		}
	}()
	go r.watch(wctx, v)

	fd, err := v.Start(ctx, android.Config{
		Session:  "Lanflow",
		MTU:      iface.MTU,
		Address4: iface.Address4,
		Address6: iface.Address6,
		DNS:      iface.DNS,
		Exclude:  iface.Exclude,
	})
	if err != nil {
		return err
	}
	tun := os.NewFile(uintptr(fd), "tun")
	// The core holds the interface from here on: when it exits, the VPN
	// stops carrying traffic, rather than drop it into nothing.
	defer tun.Close()

	if err := os.MkdirAll(filepath.Dir(spec.Controller), 0o700); err != nil {
		return err
	}
	_ = os.Remove(spec.Controller)
	logf, err := openLog(spec.LogFile)
	if err != nil {
		return err
	}
	cmd := command(spec.Bin, "-d", spec.WorkDir, "-f", config, "-ext-ctl-unix", spec.Controller)
	cmd.Dir = spec.WorkDir
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.ExtraFiles = []*os.File{tun} // descriptor 3: tunFD
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return fmt.Errorf(i18n.T("无法启动 Mihomo 内核 %s：%v"), spec.Bin, err)
	}
	r.cmd = cmd
	r.stopping = false
	r.why = nil
	r.cancel = cancel
	r.done = make(chan struct{})
	r.exited = make(chan error, 1)
	go r.wait(cmd, logf, spec, v, r.done, r.exited)
	return nil
}

// watch ends the core when the VPN goes down under it.
func (r *vpnRunner) watch(ctx context.Context, v android.VPN) {
	for reason, err := range v.Events(ctx) {
		if err != nil {
			return
		}
		r.mu.Lock()
		cmd := r.cmd
		if cmd == nil || r.stopping {
			r.mu.Unlock()
			continue
		}
		if reason == "revoked" {
			r.why = errVPNRevoked
		} else {
			r.why = errVPNEnded
		}
		r.mu.Unlock()
		_ = cmd.Process.Signal(syscall.SIGINT)
	}
}

func (r *vpnRunner) wait(cmd *exec.Cmd, logf *os.File, spec core.Spec, v android.VPN, done chan struct{}, exited chan error) {
	werr := cmd.Wait()
	_ = logf.Close()
	_ = os.Remove(spec.Controller)

	r.mu.Lock()
	stopping, why, cancel := r.stopping, r.why, r.cancel
	if r.cmd == cmd {
		r.cmd = nil
	}
	close(done)
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stopping {
		return
	}
	_ = v.Stop()
	if why == nil {
		why = exitError(werr, spec.LogFile)
	}
	exited <- why
}

func exitError(werr error, logFile string) error {
	reason := "exit status 0"
	if werr != nil {
		reason = werr.Error()
	}
	msg := fmt.Sprintf(i18n.T("Mihomo 内核意外退出（%s）"), reason)
	if tail := logTail(logFile, 20); tail != "" {
		msg += i18n.T("，最近日志：\n") + tail
	}
	return errors.New(msg)
}

func (r *vpnRunner) Stop(ctx context.Context) error {
	r.mu.Lock()
	cmd, done := r.cmd, r.done
	r.stopping = true
	r.mu.Unlock()
	if cmd != nil {
		_ = cmd.Process.Signal(syscall.SIGINT)
		t := time.NewTimer(stopGrace)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
			_ = cmd.Process.Kill()
			<-done
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-done
		}
	}
	if v, err := vpn(); err == nil {
		return v.Stop()
	}
	return nil
}

func (r *vpnRunner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runningLocked()
}

func (r *vpnRunner) runningLocked() bool {
	if r.cmd == nil {
		return false
	}
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func (r *vpnRunner) Exited() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exited
}

// logMax is the size at which the core's log starts over, keeping the
// previous one as <log>.1.
const logMax = 5 << 20

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > logMax {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// logTail returns the last n lines of the file.
func logTail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const max = 8 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > max {
		_, _ = f.Seek(-max, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
