//go:build !windows

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func writeFile(t *testing.T, path string, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

// noStaged fails when a staged or kept-back file is left in dir.
func noStaged(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lanflow-") || strings.HasSuffix(e.Name(), oldSuffix) {
			t.Errorf("left behind in %s: %s", dir, e.Name())
		}
	}
}

func TestStageFile(t *testing.T) {
	const good = "#!/bin/sh\necho mihomo\n"
	for _, tc := range []struct {
		name string
		// prepare returns the source path and the digest passed on.
		prepare func(t *testing.T, dir string) (src, want string)
		wantErr error // nil = accepted; errAny = any error
	}{
		{"matching digest", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "core")
			writeFile(t, src, good, 0o755)
			return src, sha([]byte(good))
		}, nil},
		{"digest in upper case", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "core")
			writeFile(t, src, good, 0o755)
			return src, strings.ToUpper(sha([]byte(good)))
		}, nil},
		{"no digest wanted (the helper's own copy)", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "core")
			writeFile(t, src, good, 0o755)
			return src, ""
		}, nil},
		// The TOCTOU of review item 1: the app hashed the file, then the
		// file was replaced while the user typed the password.
		{"replaced after it was hashed", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "core")
			writeFile(t, src, good, 0o755)
			want := sha([]byte(good))
			writeFile(t, src, "#!/bin/sh\necho pwned\n", 0o755)
			return src, want
		}, errDigest},
		// Root must not copy what a link in the user's directory points at.
		{"symbolic link to another file", func(t *testing.T, dir string) (string, string) {
			secret := filepath.Join(dir, "root-only")
			writeFile(t, secret, "secret", 0o600)
			src := filepath.Join(dir, "core")
			if err := os.Symlink(secret, src); err != nil {
				t.Fatal(err)
			}
			return src, sha([]byte("secret"))
		}, errAny},
		{"a FIFO does not block the installer", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "core")
			if err := syscall.Mkfifo(src, 0o644); err != nil {
				t.Skip(err)
			}
			return src, ""
		}, errAny},
		{"a directory", func(t *testing.T, dir string) (string, string) {
			src := filepath.Join(dir, "core")
			if err := os.Mkdir(src, 0o755); err != nil {
				t.Fatal(err)
			}
			return src, ""
		}, errAny},
		{"missing", func(t *testing.T, dir string) (string, string) {
			return filepath.Join(dir, "core"), ""
		}, errAny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			dst := filepath.Join(dir, "privileged")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			src, want := tc.prepare(t, dir)
			tmp, sum, err := stageFile(src, dst, 0o755, want)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatal(err)
				}
				b, rerr := os.ReadFile(tmp)
				if rerr != nil || string(b) != good || sum != sha([]byte(good)) {
					t.Fatalf("staged %q sum %s: %v", b, sum, rerr)
				}
				if fi, _ := os.Stat(tmp); fi.Mode().Perm() != 0o755 || filepath.Dir(tmp) != dst {
					t.Fatalf("staged as %s %v", tmp, fi.Mode())
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted: staged %s", tmp)
			}
			if tc.wantErr != errAny && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tmp != "" || sum != "" {
				t.Fatalf("returned %q %q with an error", tmp, sum)
			}
			noStaged(t, dst)
		})
	}
}

var errAny = errors.New("any error")

func TestInstallOptionsCheck(t *testing.T) {
	dir := t.TempDir()
	core := filepath.Join(dir, "mihomo")
	writeFile(t, core, "x", 0o755)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(core, link); err != nil {
		t.Fatal(err)
	}
	sum := sha([]byte("x"))
	for _, tc := range []struct {
		name string
		o    InstallOptions
		ok   bool
	}{
		{"complete", InstallOptions{CoreBin: core, CoreSHA256: sum, DataDir: dir, UID: "501"}, true},
		// An app from before --core-sha256: refused, not trusted.
		{"no digest", InstallOptions{CoreBin: core, DataDir: dir, UID: "501"}, false},
		{"short digest", InstallOptions{CoreBin: core, CoreSHA256: sum[:10], DataDir: dir, UID: "501"}, false},
		{"digest not hex", InstallOptions{CoreBin: core, CoreSHA256: strings.Repeat("z", 64), DataDir: dir, UID: "501"}, false},
		{"core is a link", InstallOptions{CoreBin: link, CoreSHA256: sum, DataDir: dir, UID: "501"}, false},
		{"relative core", InstallOptions{CoreBin: "mihomo", CoreSHA256: sum, DataDir: dir, UID: "501"}, false},
		{"relative data dir", InstallOptions{CoreBin: core, CoreSHA256: sum, DataDir: "data", UID: "501"}, false},
		{"no uid", InstallOptions{CoreBin: core, CoreSHA256: sum, DataDir: dir}, false},
	} {
		if err := tc.o.check(); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestFileSwap(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return "<" + filepath.Base(p) + " missing>"
		}
		return string(b)
	}
	setup := func(t *testing.T) (dir string, f *fileSwap) {
		dir = t.TempDir()
		writeFile(t, filepath.Join(dir, "helper"), "old helper", 0o755)
		writeFile(t, filepath.Join(dir, "core"), "old core", 0o755)
		f = &fileSwap{}
		for _, name := range []string{"helper", "core", "config"} { // config is new
			tmp, err := stageBytes(dir, []byte("new "+name), 0o644)
			if err != nil {
				t.Fatal(err)
			}
			f.add(tmp, filepath.Join(dir, name))
		}
		return dir, f
	}
	state := func(dir string) string {
		return read(filepath.Join(dir, "helper")) + "|" + read(filepath.Join(dir, "core")) + "|" + read(filepath.Join(dir, "config"))
	}
	const before, after = "old helper|old core|<config missing>", "new helper|new core|new config"

	t.Run("commit then finish", func(t *testing.T) {
		dir, f := setup(t)
		if err := f.commit(); err != nil {
			t.Fatal(err)
		}
		if got := state(dir); got != after {
			t.Fatalf("after commit: %s", got)
		}
		f.finish()
		noStaged(t, dir)
	})
	t.Run("commit then rollback", func(t *testing.T) {
		dir, f := setup(t)
		if err := f.commit(); err != nil {
			t.Fatal(err)
		}
		f.rollback()
		if got := state(dir); got != before {
			t.Fatalf("after rollback: %s", got)
		}
		noStaged(t, dir)
	})
	t.Run("rollback before commit", func(t *testing.T) {
		dir, f := setup(t)
		f.rollback()
		if got := state(dir); got != before {
			t.Fatalf("after rollback: %s", got)
		}
		noStaged(t, dir)
	})
	t.Run("a failing move undoes the earlier ones", func(t *testing.T) {
		dir, f := setup(t)
		// The second staged file vanishes: its move fails.
		if err := os.Remove(f.swaps[1].tmp); err != nil {
			t.Fatal(err)
		}
		if err := f.commit(); err == nil {
			t.Fatal("commit succeeded")
		}
		if got := state(dir); got != before {
			t.Fatalf("after the failed commit: %s", got)
		}
		noStaged(t, dir)
	})
}

// copyFile copies src to dst (temp file + rename) with mode, for fixtures.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".lanflow-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
