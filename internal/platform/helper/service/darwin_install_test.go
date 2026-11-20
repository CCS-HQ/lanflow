//go:build darwin

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installEnv is an installer over a temporary directory with a recorded,
// scriptable launchctl. Nothing of the system is touched.
type installEnv struct {
	in      *installer
	root    string
	self    string // the "running" helper binary
	core    string // the user's Mihomo binary
	dataDir string
	calls   []string
	// fail maps a launchctl call ("bootstrap#1" = the first bootstrap) to
	// its error.
	fail   map[string]error
	counts map[string]int
}

func newInstallEnv(t *testing.T) *installEnv {
	t.Helper()
	root := t.TempDir()
	e := &installEnv{root: root, fail: map[string]error{}, counts: map[string]int{}}
	e.self = filepath.Join(root, "tmp", "lanflow-helper")
	writeFile(t, e.self, "helper v2", 0o700)
	e.core = filepath.Join(root, "user", "mihomo")
	writeFile(t, e.core, "core v2", 0o755)
	e.dataDir = filepath.Join(root, "user", "data")
	if err := os.MkdirAll(e.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	daemons := filepath.Join(root, "LaunchDaemons")
	if err := os.MkdirAll(daemons, 0o755); err != nil {
		t.Fatal(err)
	}
	e.in = &installer{
		helperDir:  filepath.Join(root, "PrivilegedHelperTools"),
		helperBin:  filepath.Join(root, "PrivilegedHelperTools", "com.lanflow.helper"),
		coreBin:    filepath.Join(root, "PrivilegedHelperTools", "com.lanflow.mihomo"),
		configDir:  filepath.Join(root, "Support"),
		configPath: filepath.Join(root, "Support", "config.json"),
		plistPath:  filepath.Join(daemons, Label+".plist"),
		logDir:     filepath.Join(root, "Logs"),
		euid:       func() int { return 0 },
		self:       func() (string, error) { return e.self, nil },
		launchctl: func(_ context.Context, args ...string) error {
			e.counts[args[0]]++
			e.calls = append(e.calls, args[0])
			return e.fail[args[0]+"#"+strconv.Itoa(e.counts[args[0]])]
		},
		chown:      func(string, int, int) error { return nil },
		waitSocket: func(context.Context, time.Duration) error { return nil },
	}
	return e
}

func (e *installEnv) options() InstallOptions {
	b, _ := os.ReadFile(e.core)
	return InstallOptions{CoreBin: e.core, CoreSHA256: sha(b), DataDir: e.dataDir, UID: strconv.Itoa(os.Getuid()), Version: "0.2.0"}
}

// previous lays down an installed older helper.
func (e *installEnv) previous(t *testing.T) {
	t.Helper()
	writeFile(t, e.in.helperBin, "helper v1", 0o755)
	writeFile(t, e.in.coreBin, "core v1", 0o755)
	writeFile(t, e.in.plistPath, "plist v1", 0o644)
	resolved, _ := filepath.EvalSymlinks(e.dataDir)
	cfg := Config{Version: ConfigVersion, Core: e.in.coreBin, SHA256: sha([]byte("core v1")), DataDir: resolved, UID: strconv.Itoa(os.Getuid()), HelperVersion: "0.1.0"}
	if err := SaveConfig(e.in.configPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *installEnv) read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// installed summarizes the helper, the core and the digest on record.
func (e *installEnv) installed() string {
	sum := ""
	if c, err := LoadConfig(e.in.configPath); err == nil {
		sum = c.SHA256
	}
	return e.read(e.in.helperBin) + "|" + e.read(e.in.coreBin) + "|" + sum
}

func (e *installEnv) clean(t *testing.T) {
	t.Helper()
	for _, d := range []string{e.in.helperDir, e.in.configDir, filepath.Dir(e.in.plistPath)} {
		if _, err := os.Stat(d); err == nil {
			noStaged(t, d)
		}
	}
}

func TestDarwinInstall(t *testing.T) {
	const v1 = "helper v1|core v1|"
	const v2 = "helper v2|core v2|"
	sumV1, sumV2 := sha([]byte("core v1")), sha([]byte("core v2"))
	boom := errors.New("boom")
	for _, tc := range []struct {
		name     string
		previous bool
		// tweak changes the environment or the options before install.
		tweak     func(t *testing.T, e *installEnv, o *InstallOptions)
		wantErr   string // substring; "" = success
		want      string // installed() afterwards
		wantCalls string
	}{
		{name: "fresh install", want: v2 + sumV2, wantCalls: "bootout bootstrap"},
		{name: "upgrade", previous: true, want: v2 + sumV2, wantCalls: "bootout bootstrap"},
		{name: "not root",
			tweak:   func(_ *testing.T, e *installEnv, _ *InstallOptions) { e.in.euid = func() int { return 501 } },
			wantErr: "管理员权限", want: "||", wantCalls: ""},
		// An app from before the digest: refused, and a running helper is
		// not stopped for it.
		{name: "no digest", previous: true,
			tweak:   func(_ *testing.T, _ *installEnv, o *InstallOptions) { o.CoreSHA256 = "" },
			wantErr: "--core-sha256", want: v1 + sumV1, wantCalls: ""},
		// Review item 1: the core was replaced after the app hashed it.
		// Nothing is installed and the running helper stays as it is.
		{name: "core replaced during the prompt", previous: true,
			tweak: func(t *testing.T, e *installEnv, _ *InstallOptions) {
				writeFile(t, e.core, "evil core", 0o755)
			},
			wantErr: "不一致", want: v1 + sumV1, wantCalls: ""},
		{name: "core replaced, nothing installed before",
			tweak: func(t *testing.T, e *installEnv, _ *InstallOptions) {
				writeFile(t, e.core, "evil core", 0o755)
			},
			wantErr: "不一致", want: "||", wantCalls: ""},
		// The link is swapped in after the app hashed the real file.
		{name: "core path turned into a link", previous: true,
			tweak: func(t *testing.T, e *installEnv, _ *InstallOptions) {
				other := filepath.Join(e.root, "other")
				writeFile(t, other, "core v2", 0o600)
				if err := os.Remove(e.core); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, e.core); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "内核文件不可用", want: v1 + sumV1, wantCalls: ""},
		{name: "data dir of another user", previous: true,
			tweak:   func(_ *testing.T, _ *installEnv, o *InstallOptions) { o.UID = "4294967000" },
			wantErr: "不属于用户", want: v1 + sumV1, wantCalls: ""},
		{name: "downgrade refused", previous: true,
			tweak:   func(_ *testing.T, _ *installEnv, o *InstallOptions) { o.Version = "0.0.9" },
			wantErr: "拒绝降级", want: v1 + sumV1, wantCalls: ""},
		// A chown that fails is an error (it used to be ignored), found
		// before the running helper is stopped.
		{name: "chown fails", previous: true,
			tweak: func(_ *testing.T, e *installEnv, _ *InstallOptions) {
				e.in.chown = func(p string, _, _ int) error {
					if strings.HasPrefix(filepath.Base(p), ".lanflow-") && filepath.Dir(p) == e.in.configDir {
						return boom
					}
					return nil
				}
			},
			wantErr: "boom", want: v1 + sumV1, wantCalls: ""},
		// Review item 14: failures after the old helper was stopped put
		// the old files back and load the old LaunchDaemon again.
		{name: "bootstrap fails on an upgrade", previous: true,
			tweak:   func(_ *testing.T, e *installEnv, _ *InstallOptions) { e.fail["bootstrap#1"] = boom },
			wantErr: "加载 LaunchDaemon 失败", want: v1 + sumV1, wantCalls: "bootout bootstrap bootstrap"},
		{name: "bootstrap fails on a fresh install",
			tweak:   func(_ *testing.T, e *installEnv, _ *InstallOptions) { e.fail["bootstrap#1"] = boom },
			wantErr: "加载 LaunchDaemon 失败", want: "||", wantCalls: "bootout bootstrap"},
		{name: "the new helper never listens", previous: true,
			tweak: func(_ *testing.T, e *installEnv, _ *InstallOptions) {
				e.in.waitSocket = func(context.Context, time.Duration) error { return boom }
			},
			wantErr: "boom", want: v1 + sumV1, wantCalls: "bootout bootstrap bootout bootstrap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newInstallEnv(t)
			if tc.previous {
				e.previous(t)
			}
			o := e.options()
			if tc.tweak != nil {
				tc.tweak(t, e, &o)
			}
			err := e.in.install(context.Background(), o, t.Logf)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatal(err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if got := e.installed(); got != tc.want {
				t.Errorf("installed = %s, want %s", got, tc.want)
			}
			if got := strings.Join(e.calls, " "); got != tc.wantCalls {
				t.Errorf("launchctl calls = %q, want %q", got, tc.wantCalls)
			}
			if tc.previous && tc.wantErr != "" && e.read(e.in.plistPath) != "plist v1" {
				t.Errorf("plist = %q, want the previous one", e.read(e.in.plistPath))
			}
			if tc.wantErr == "" {
				c, err := LoadConfig(e.in.configPath)
				if err != nil {
					t.Fatal(err)
				}
				if c.Core != e.in.coreBin || c.CoreSource != e.core || c.HelperVersion != "0.2.0" {
					t.Errorf("config = %+v", c)
				}
				if e.read(e.in.plistPath) != plist() {
					t.Errorf("plist not written")
				}
			}
			e.clean(t)
		})
	}
}

// The process path the orphan check relies on, read for this very process.
func TestExePathOfSelf(t *testing.T) {
	got, err := exePath(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	a, _ := filepath.EvalSymlinks(got)
	b, _ := filepath.EvalSymlinks(want)
	if a == "" || a != b {
		t.Fatalf("exePath = %s, executable = %s", got, want)
	}
	if _, err := exePath(0x7ffffff0); err == nil {
		t.Fatal("a pid that does not exist has a path")
	}
}

// The supervisor records the running core's pid for the next helper and
// removes the record when the core has ended (stopped or crashed).
func TestSupervisorPidFile(t *testing.T) {
	s, f, fs := newTestSupervisor(t)
	s.PidFile = filepath.Join(t.TempDir(), "core.pid")
	ctx := context.Background()
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	if got := readPidFile(s.PidFile); got != fs.procs[0].Pid() {
		t.Fatalf("pid file = %d, want %d", got, fs.procs[0].Pid())
	}
	if err := s.StopCore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readPidFile(s.PidFile); got != 0 {
		t.Fatalf("pid file after stop = %d", got)
	}

	// An unexpected exit clears it too.
	if err := s.StartCore(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	fs.procs[1].finish(errors.New("exit status 2"))
	deadline := time.Now().Add(5 * time.Second)
	for s.Running() {
		if time.Now().After(deadline) {
			t.Fatal("still running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := readPidFile(s.PidFile); got != 0 {
		t.Fatalf("pid file after a crash = %d", got)
	}
}
