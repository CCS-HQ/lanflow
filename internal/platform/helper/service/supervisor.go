package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"lanflow/internal/platform/helper/protocol"
)

// Process is a running Mihomo as seen by the supervisor.
type Process interface {
	Pid() int
	// Wait blocks until the process exits and returns its exit error.
	Wait() error
	// Interrupt asks the process to exit gracefully (SIGINT on unix).
	Interrupt() error
	Kill() error
}

// Command describes the process to start.
type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
	// Output receives stdout and stderr.
	Output io.Writer
}

// StartFunc spawns a process. The default is exec-based; tests inject a
// fake.
type StartFunc func(ctx context.Context, cmd Command) (Process, error)

// Hooks are platform actions around a run. Nil fields are skipped.
type Hooks struct {
	// OpenLog opens the Mihomo log for appending (rotating first). The
	// default rotates at 5 MB keeping one old file and hands ownership of
	// the file to the user on unix.
	OpenLog func(path string, uid string) (io.WriteCloser, error)
	// AfterStart runs in the background once the process is spawned, e.g.
	// to hand the controller socket to the user when it appears.
	AfterStart func(ctx context.Context, spec protocol.Spec, uid string)
	// AfterExit runs once the process has exited.
	AfterExit func(spec protocol.Spec, uid string)
}

// StopGrace is how long Stop waits after Interrupt before killing.
const StopGrace = 5 * time.Second

// LogRotateSize is the size at which the Mihomo log is rotated.
const LogRotateSize = 5 << 20

// Supervisor runs at most one Mihomo process.
type Supervisor struct {
	Policy *Policy
	Start  StartFunc
	Hooks  Hooks
	Logf   func(format string, args ...any)
	// PidFile, when set, records the running process's pid (removed when
	// it exits) so the next helper can end a core this one left behind.
	PidFile string
	// WorkDir is the directory Mihomo runs in: the helper's own, which
	// only root/SYSTEM can write (see stage.go). Run sets it; a supervisor
	// without one starts nothing.
	WorkDir string
	// PrepareWorkDir creates and checks WorkDir before every use. Nil
	// means the platform default.
	PrepareWorkDir func(dir string) error

	// stageMu serializes starts and stage requests: one at a time works
	// on WorkDir. It is taken before mu and held while files are read,
	// checked and copied, which takes time a config can stretch; mu, which
	// status, stop and ping need, is held only to look at or change the
	// fields below.
	stageMu  sync.Mutex
	starting bool // a start is preparing WorkDir
	aborted  bool // a stop arrived while it did
	mu       sync.Mutex
	proc     Process
	spec     protocol.Spec
	log      io.WriteCloser
	done     chan struct{} // closed when the current process has exited
	stopping bool
	exitErr  string
	cancel   context.CancelFunc
}

// NewSupervisor returns a supervisor with platform defaults.
func NewSupervisor(p *Policy) *Supervisor {
	return &Supervisor{Policy: p, Start: execStart, Hooks: defaultHooks(), Logf: func(string, ...any) {}}
}

// Running reports whether a process is alive.
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proc != nil
}

// Status returns the current state for the status op.
func (s *Supervisor) Status() protocol.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := protocol.Response{OK: true, Running: s.proc != nil, ExitError: s.exitErr}
	if s.proc != nil {
		r.PID = s.proc.Pid()
	}
	return r
}

// StartCore validates spec, copies its config (and data files) into
// WorkDir and spawns Mihomo there. It fails when a process is already
// running.
func (s *Supervisor) StartCore(ctx context.Context, spec protocol.Spec) error {
	s.stageMu.Lock()
	defer s.stageMu.Unlock()
	s.mu.Lock()
	if s.proc != nil {
		s.mu.Unlock()
		return errors.New("Mihomo 内核已在权限组件中运行，请先停止")
	}
	s.starting, s.aborted = true, false
	s.mu.Unlock()
	// Validation hashes the core and staging copies files: neither holds
	// mu. Only this start can set proc meanwhile (stageMu).
	resolved, err := s.Policy.Validate(spec)
	var config string
	if err == nil {
		// The core gets the helper's directory and the copy of the config
		// in it; resolved keeps the user's paths (log, controller, hooks).
		config, err = s.stage(resolved, true)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starting = false
	if err != nil {
		return err
	}
	if s.aborted {
		// A stop that arrived while the start was preparing must not be
		// followed by a running core.
		return errors.New("启动 Mihomo 的请求已被停止请求取消")
	}
	run := resolved
	run.WorkDir, run.Config = s.WorkDir, config
	uid := s.Policy.Config.UID
	openLog := s.Hooks.OpenLog
	if openLog == nil {
		openLog = openLogFile
	}
	logw, err := openLog(resolved.LogFile, uid)
	if err != nil {
		return fmt.Errorf("无法打开日志文件: %w", err)
	}
	cmd := Command{
		Path:   resolved.Bin,
		Args:   append([]string{"-d", run.WorkDir, "-f", run.Config}, controllerArgs(run.Controller)...),
		Dir:    run.WorkDir,
		Env:    append(baseEnv(uid, run), run.Env...),
		Output: logw,
	}
	runCtx, cancel := context.WithCancel(context.Background())
	proc, err := s.Start(runCtx, cmd)
	if err != nil {
		cancel()
		_ = logw.Close()
		return fmt.Errorf("启动 Mihomo 失败: %w", err)
	}
	s.proc, s.spec, s.log, s.cancel = proc, resolved, logw, cancel
	s.exitErr = ""
	s.stopping = false
	s.done = make(chan struct{})
	s.Logf("started mihomo pid=%d", proc.Pid())
	if s.PidFile != "" {
		if err := writePidFile(s.PidFile, proc.Pid()); err != nil {
			s.Logf("pid file: %v", err)
		}
	}
	if s.Hooks.AfterStart != nil {
		go s.Hooks.AfterStart(runCtx, resolved, uid)
	}
	go s.wait(proc, s.done)
	return nil
}

func (s *Supervisor) wait(proc Process, done chan struct{}) {
	err := proc.Wait()
	s.mu.Lock()
	if s.proc == proc {
		if !s.stopping {
			if err != nil {
				s.exitErr = "Mihomo 意外退出: " + err.Error()
			} else {
				s.exitErr = "Mihomo 意外退出（退出码 0）"
			}
			s.Logf("mihomo exited unexpectedly: %v", err)
		} else {
			s.Logf("mihomo stopped")
		}
		spec := s.spec
		if s.log != nil {
			_ = s.log.Close()
		}
		if s.cancel != nil {
			s.cancel()
		}
		s.proc, s.log, s.cancel = nil, nil, nil
		if s.PidFile != "" {
			_ = os.Remove(s.PidFile)
		}
		s.mu.Unlock()
		if s.Hooks.AfterExit != nil {
			s.Hooks.AfterExit(spec, s.Policy.Config.UID)
		}
	} else {
		s.mu.Unlock()
	}
	close(done)
}

// StageConfig copies the user's config into WorkDir again, checked and
// cleaned as at start, and returns the copy's path: what the app passes
// to the running core's reload, which only loads a file inside the core's
// work directory. Provider files the new config names are copied too.
func (s *Supervisor) StageConfig(spec protocol.Spec) (string, error) {
	s.stageMu.Lock()
	defer s.stageMu.Unlock()
	s.mu.Lock()
	running, user := s.proc != nil, s.spec // the run's validated paths
	s.mu.Unlock()
	if !running {
		return "", errors.New("Mihomo 内核没有在权限组件中运行")
	}
	config, err := s.Policy.ValidateConfig(spec.Config)
	if err != nil {
		return "", err
	}
	user.Config = config // only the config is new
	return s.stage(user, false)
}

// writePidFile records pid in path (temporary file + rename).
func writePidFile(path string, pid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// StopCore stops the process gracefully, killing it after StopGrace.
// It is a no-op when nothing runs.
func (s *Supervisor) StopCore(ctx context.Context) error {
	s.mu.Lock()
	proc, done := s.proc, s.done
	if proc == nil {
		if s.starting {
			s.aborted = true
		}
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	s.mu.Unlock()

	if err := proc.Interrupt(); err != nil {
		s.Logf("interrupt: %v", err)
	}
	timer := time.NewTimer(StopGrace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	s.Logf("mihomo did not exit after %s, killing", StopGrace)
	if err := proc.Kill(); err != nil {
		s.Logf("kill: %v", err)
	}
	select {
	case <-done:
		return nil
	case <-time.After(StopGrace):
		return errors.New("无法结束 Mihomo 进程")
	}
}

// Shutdown stops a running process; called when the helper exits.
func (s *Supervisor) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*StopGrace)
	defer cancel()
	if err := s.StopCore(ctx); err != nil {
		s.Logf("shutdown: %v", err)
	}
}

// NewHandler maps protocol requests onto the supervisor.
func NewHandler(s *Supervisor, version string) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		switch req.Op {
		case protocol.OpPing:
			return protocol.Response{OK: true, Version: version, Protocol: protocol.Version, CorePath: s.Policy.Config.Core, Running: s.Running()}
		case protocol.OpStart:
			if req.Spec == nil {
				return protocol.Response{Error: "start 请求缺少 spec"}
			}
			if err := s.StartCore(ctx, *req.Spec); err != nil {
				s.Logf("start refused: %v", err)
				return protocol.Response{Error: err.Error()}
			}
			return s.Status()
		case protocol.OpStop:
			if err := s.StopCore(ctx); err != nil {
				return protocol.Response{Error: err.Error()}
			}
			return s.Status()
		case protocol.OpStatus:
			return s.Status()
		case protocol.OpStage:
			if req.Spec == nil {
				return protocol.Response{Error: "stage 请求缺少 spec"}
			}
			path, err := s.StageConfig(*req.Spec)
			if err != nil {
				s.Logf("stage refused: %v", err)
				return protocol.Response{Error: err.Error()}
			}
			resp := s.Status()
			resp.ConfigPath = path
			return resp
		default:
			return protocol.Response{Error: fmt.Sprintf("未知操作: %q", req.Op)}
		}
	}
}
