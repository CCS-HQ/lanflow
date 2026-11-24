//go:build windows

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"lanflow/internal/platform/helper/protocol"
)

// winStage is a supervisor whose work directory is a plain temporary one
// (the real one needs SYSTEM's DACL) next to a user data directory.
func winStage(t *testing.T) (*Supervisor, protocol.Spec) {
	t.Helper()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	d := winTempDir(t)
	user := filepath.Join(d, "data", "core")
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(&Policy{Config: Config{UID: tu.User.Sid.String()}})
	s.Logf = t.Logf
	s.WorkDir = filepath.Join(d, "helper-work")
	s.PrepareWorkDir = func(dir string) error { return os.MkdirAll(dir, 0o700) }
	return s, protocol.Spec{WorkDir: user, Config: filepath.Join(user, "runtime.yaml")}
}

func winWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStageWindows(t *testing.T) {
	s, spec := winStage(t)
	winWrite(t, spec.Config, "mixed-port: 7890\nexternal-controller: 0.0.0.0:9090\nrule-providers:\n  a: {type: http, behavior: domain, url: 'https://e/a', path: ./rs/a.yaml}\n")
	winWrite(t, filepath.Join(spec.WorkDir, "geoip.metadb"), "GEOIP")
	winWrite(t, filepath.Join(spec.WorkDir, "rs", "a.yaml"), "payload: []")
	winWrite(t, filepath.Join(spec.WorkDir, "cache.db"), "CACHE")
	path, err := s.stage(spec, true)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(s.WorkDir, stagedConfigName) {
		t.Fatalf("staged config = %s", path)
	}
	cfg, _ := os.ReadFile(path)
	if strings.Contains(string(cfg), "external-controller") || !strings.Contains(string(cfg), "mixed-port: 7890") {
		t.Fatalf("staged config:\n%s", cfg)
	}
	for rel, want := range map[string]string{"geoip.metadb": "GEOIP", `rs\a.yaml`: "payload: []"} {
		if b, err := os.ReadFile(filepath.Join(s.WorkDir, rel)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", rel, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.WorkDir, "cache.db")); err == nil {
		t.Error("cache.db was copied")
	}
	// A second stage replaces the config in place.
	winWrite(t, spec.Config, "mixed-port: 7891\n")
	if _, err := s.stage(spec, false); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := os.ReadFile(path); string(cfg) != "mixed-port: 7891\n" {
		t.Fatalf("restaged config:\n%s", cfg)
	}
}

// SYSTEM does not read through what the user can plant: a junction
// swapped into the path after validation, or a hard link to another file.
func TestStageWindowsRefusesLinks(t *testing.T) {
	t.Run("config behind a junction", func(t *testing.T) {
		s, spec := winStage(t)
		other := filepath.Join(winTempDir(t), "other")
		winWrite(t, filepath.Join(other, "runtime.yaml"), "mixed-port: 1\n")
		conf := filepath.Join(filepath.Dir(spec.WorkDir), "conf")
		junction(t, conf, other)
		spec.Config = filepath.Join(conf, "runtime.yaml")
		if _, err := s.stage(spec, true); err == nil {
			t.Fatal("staged a config through a junction")
		}
	})
	t.Run("config is a hard link", func(t *testing.T) {
		s, spec := winStage(t)
		secret := filepath.Join(winTempDir(t), "secret")
		winWrite(t, secret, "mixed-port: 1\n")
		if err := os.Link(secret, spec.Config); err != nil {
			t.Skipf("hard link: %v", err)
		}
		if _, err := s.stage(spec, true); err == nil {
			t.Fatal("staged a hard-linked config")
		}
	})
	t.Run("work dir behind a junction", func(t *testing.T) {
		s, spec := winStage(t)
		winWrite(t, spec.Config, "mixed-port: 7890\n")
		other := filepath.Join(winTempDir(t), "other")
		winWrite(t, filepath.Join(other, "geoip.metadb"), "ELSEWHERE")
		linked := filepath.Join(filepath.Dir(spec.WorkDir), "linked")
		junction(t, linked, other)
		spec.WorkDir = linked
		// The start goes on without the database.
		if _, err := s.stage(spec, true); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(s.WorkDir, "geoip.metadb")); err == nil {
			t.Fatal("a database was copied through a junction")
		}
	})
	t.Run("config owned by another sid", func(t *testing.T) {
		s, spec := winStage(t)
		s.Policy.Config.UID = "S-1-5-18"
		winWrite(t, spec.Config, "mixed-port: 7890\n")
		if _, err := s.stage(spec, true); err == nil {
			t.Fatal("staged a config whose owner did not match UID")
		}
	})
}
