//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review item 2 (the part done here): the root Mihomo is never started on
// a work dir or config that is reached through a symbolic link inside the
// user's data directory, even when the link resolves to a place inside it.
func TestValidateRefusesLinksInsideDataDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		// arrange changes the fixture's files and spec.
		arrange func(t *testing.T, f *fixture)
		wantErr string // "" = accepted
	}{
		{"plain paths", func(*testing.T, *fixture) {}, ""},
		{"work dir is a link to a directory inside", func(t *testing.T, f *fixture) {
			link := filepath.Join(f.dataDir, "work-link")
			if err := os.Symlink(f.spec.WorkDir, link); err != nil {
				t.Fatal(err)
			}
			f.spec.WorkDir = link
		}, "工作目录路径中不能有符号链接"},
		{"a parent of the work dir is a link", func(t *testing.T, f *fixture) {
			real := filepath.Join(f.dataDir, "real")
			if err := os.MkdirAll(filepath.Join(real, "work"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(f.dataDir, "via")); err != nil {
				t.Fatal(err)
			}
			f.spec.WorkDir = filepath.Join(f.dataDir, "via", "work")
		}, "工作目录路径中不能有符号链接"},
		{"config is a link to a file inside", func(t *testing.T, f *fixture) {
			link := filepath.Join(f.dataDir, "linked.yaml")
			if err := os.Symlink(f.spec.Config, link); err != nil {
				t.Fatal(err)
			}
			f.spec.Config = link
		}, "配置文件路径中不能有符号链接"},
		{"config below a linked directory", func(t *testing.T, f *fixture) {
			real := filepath.Join(f.dataDir, "profiles")
			if err := os.MkdirAll(real, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(real, "c.yaml"), []byte("mixed-port: 7890\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(f.dataDir, "p")); err != nil {
				t.Fatal(err)
			}
			f.spec.Config = filepath.Join(f.dataDir, "p", "c.yaml")
		}, "配置文件路径中不能有符号链接"},
		// The user's own layout above the data directory (a linked home)
		// stays allowed: the helper recorded the resolved data directory.
		{"the data directory is reached through a link", func(t *testing.T, f *fixture) {
			alias := filepath.Join(filepath.Dir(f.dataDir), "alias")
			if err := os.Symlink(f.dataDir, alias); err != nil {
				t.Fatal(err)
			}
			f.spec.WorkDir = filepath.Join(alias, "work")
			f.spec.Config = filepath.Join(alias, "runtime.yaml")
			f.spec.LogFile = filepath.Join(alias, "core.log")
			f.spec.Controller = filepath.Join(alias, "ctl.sock")
		}, ""},
		{"unclean but link-free paths", func(_ *testing.T, f *fixture) {
			f.spec.WorkDir = f.dataDir + "/./work/../work/"
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.arrange(t, f)
			out, err := f.policy.Validate(f.spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if want, _ := filepath.EvalSymlinks(filepath.Join(f.dataDir, "work")); out.WorkDir != want {
					t.Fatalf("WorkDir = %s, want %s", out.WorkDir, want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// verifyNoLinks is the walk without following links: a link swapped in
// after the paths were resolved is refused.
func TestVerifyNoLinks(t *testing.T) {
	f := newFixture(t)
	dataDir, _ := filepath.EvalSymlinks(f.dataDir)
	work := filepath.Join(dataDir, "work")
	config := filepath.Join(dataDir, "runtime.yaml")
	if err := verifyNoLinks(dataDir, work, true); err != nil {
		t.Fatal(err)
	}
	if err := verifyNoLinks(dataDir, config, false); err != nil {
		t.Fatal(err)
	}
	// Swap both for links.
	if err := os.Rename(work, work+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(work+".real", work); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(config, config+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(config+".real", config); err != nil {
		t.Fatal(err)
	}
	if err := verifyNoLinks(dataDir, work, true); err == nil {
		t.Fatal("a linked work dir passed")
	}
	if err := verifyNoLinks(dataDir, config, false); err == nil {
		t.Fatal("a linked config passed")
	}
	if err := verifyNoLinks(dataDir, dataDir, false); err == nil {
		t.Fatal("a directory passed as the config")
	}
}
