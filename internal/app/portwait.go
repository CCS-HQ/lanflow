package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"lanflow/internal/fsx"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

const rootStoppedFile = "root-core-stopped"

// The helper can keep running with TUN off. Remember its eventual exit
// across app restarts too, before the next app starts an ordinary core.
func (s *Service) noteRootStopped() {
	now := s.d.now()
	s.mu.Lock()
	s.state.rootStoppedAt = now
	s.mu.Unlock()
	if err := writeRootStopped(s.opt.DataDir, now); err != nil {
		s.alog.Warnf("remember the privileged core's exit: %v", err)
	}
}

func writeRootStopped(dataDir string, when time.Time) error {
	return fsx.WriteFile(filepath.Join(dataDir, rootStoppedFile), []byte(when.Format(time.RFC3339Nano)), 0o600)
}

func readRootStopped(dataDir string, now time.Time) time.Time {
	path := filepath.Join(dataDir, rootStoppedFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	when, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
	if err != nil || when.After(now) || now.Sub(when) >= rootTimeWait {
		_ = os.Remove(path)
		return time.Time{}
	}
	return when
}

var errPortListening = errors.New("proxy port has a listener")

const (
	// portWaitShort bounds the wait when no root core went away lately:
	// the port is unbindable for another reason, which waiting rarely fixes.
	portWaitShort = 4 * time.Second
	// rootTimeWait is how long TIME_WAIT sockets of a root core can keep
	// the port from the user's core: two MSLs (macOS net.inet.tcp.msl is
	// 15 s, measured 30 s in the VM) plus a margin.
	rootTimeWait = 35 * time.Second
)

// portWaitTimeout is how long to wait for the proxy port: the rest of
// rootTimeWait after a root core went away at rootStoppedAt (zero = never),
// and never less than portWaitShort.
func portWaitTimeout(now, rootStoppedAt time.Time) time.Duration {
	if rootStoppedAt.IsZero() {
		return portWaitShort
	}
	if left := rootTimeWait - now.Sub(rootStoppedAt); left > portWaitShort {
		return left
	}
	return portWaitShort
}

// waitTCPPortFree handles a macOS privilege transition: sockets left in
// TIME_WAIT by the root-run TUN core cannot be rebound by the user's core,
// even with SO_REUSEADDR. A successful Stop has already reaped the process;
// killing more processes or merely skipping the bind check cannot help.
// The caller picks timeout (portWaitTimeout). A real listener is still a
// conflict and is reported immediately.
func waitTCPPortFree(ctx context.Context, port int, lan bool, timeout time.Duration) error {
	if runtime.GOOS != "darwin" {
		return errPortListening
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return waitPortRelease(ctx, 50*time.Millisecond,
		func() bool { return tcpPortInUse(port, lan) },
		func() bool { return tcpPortListening(port) })
}

// tcpPortListening reports whether something accepts connections on
// 127.0.0.1:port.
func tcpPortListening(port int) bool {
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	c, err := d.Dial("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func waitPortRelease(ctx context.Context, poll time.Duration, busy, listening func() bool) error {
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !busy() {
			return nil
		}
		if listening() {
			return errPortListening
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// waitStartPort also covers a binary switch that reuses runtime.yaml.
// opMu must be held.
func (s *Service) waitStartPort(set model.Settings, helperOK bool, prev model.CoreState) error {
	if prev != model.CoreRunning && s.d.portInUse(set.MixedPort, set.AllowLAN) {
		// A core of ours the previous run left may hold it (takeover.go).
		s.stopStray()
		busy := s.d.portInUse(set.MixedPort, set.AllowLAN)
		if busy && set.TUN && helperOK && runtime.GOOS == "darwin" {
			// The probe runs as this user; a root core binds over the
			// TIME_WAIT sockets that make it fail, so only a real listener
			// is a conflict. (Should launchLocked fall back to a plain
			// proxy because another TUN runs, the core logs the bind error.)
			if s.d.portListening != nil && s.d.portListening(set.MixedPort) {
				return s.portTaken(set.MixedPort)
			}
		} else if busy {
			if s.d.waitPortFree == nil {
				return s.portTaken(set.MixedPort)
			}
			s.mu.Lock()
			timeout := portWaitTimeout(s.d.now(), s.state.rootStoppedAt)
			s.mu.Unlock()
			s.alog.Infof("waiting up to %v for proxy port %d to become reusable", timeout.Round(time.Second), set.MixedPort)
			if timeout > portWaitShort {
				s.notify(Notice{Kind: "toast", Title: i18n.T("正在等待系统释放代理端口"),
					Detail: i18n.Tf("TUN 模式下的连接刚关闭，系统要再占用端口 %d 最多约 %d 秒。", set.MixedPort, int(timeout.Round(time.Second)/time.Second))})
			}
			err := s.d.waitPortFree(s.rootCtx, set.MixedPort, set.AllowLAN, timeout)
			if errors.Is(err, errPortListening) {
				return s.portTaken(set.MixedPort)
			}
			if err != nil {
				return fmt.Errorf(i18n.T("等待代理端口 %d 释放失败：%v。请稍后重试。"), set.MixedPort, err)
			}
		}
	}
	return nil
}
