package iosvpn

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The extension's Mihomo, the desktop's pinned core and this constant
// move together.
func TestCoreVersionPinned(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	mod, err := os.ReadFile(filepath.Join(root, "mobile", "ios", "tunnelcore", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\s*github\.com/metacubex/mihomo ` + regexp.QuoteMeta(CoreVersion) + `\b`).Match(mod) {
		t.Errorf("tunnelcore/go.mod does not require mihomo %s", CoreVersion)
	}
	fetch, err := os.ReadFile(filepath.Join(root, "scripts", "fetch-core.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fetch), `PINNED_VERSION="`+CoreVersion+`"`) {
		t.Errorf("scripts/fetch-core.sh pins another Mihomo than %s", CoreVersion)
	}
}

func TestHandOver(t *testing.T) {
	work := t.TempDir()
	group := t.TempDir()
	p := PathsIn(group)
	config := filepath.Join(work, "runtime.yaml")
	src := "rule-providers:\n  a:\n    path: " + filepath.Join(work, "providers", "a.yaml") + "\n"
	if err := os.WriteFile(config, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"geoip.metadb": "geo", "GeoSite.dat": "site", "cache.db": "no", "runtime.yaml.tmp": "no"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := handOver(p, work, config); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(p.Home, sourceName))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(p.Home, "providers", "a.yaml"); !strings.Contains(string(got), want) || strings.Contains(string(got), work) {
		t.Errorf("source.yaml = %q, want paths under %s", got, p.Home)
	}
	for name, want := range map[string]bool{"geoip.metadb": true, "GeoSite.dat": true, "cache.db": false, "runtime.yaml.tmp": false} {
		if _, err := os.Stat(filepath.Join(p.Home, name)); (err == nil) != want {
			t.Errorf("%s copied = %v, want %v", name, err == nil, want)
		}
	}

	// A newer geodata file replaces the copy; an unchanged one is kept.
	later := time.Now().Add(time.Hour)
	if err := os.WriteFile(filepath.Join(work, "geoip.metadb"), []byte("geo2"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(work, "geoip.metadb"), later, later)
	if err := handOver(p, work, config); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(p.Home, "geoip.metadb")); string(b) != "geo2" {
		t.Errorf("geoip.metadb = %q after update", b)
	}
}

func TestRebaseLeavesOtherPaths(t *testing.T) {
	got := string(rebase([]byte("a: /data/core/x\nb: /data/core2/y\nc: /data/core\n"), "/data/core", "/g/core"))
	if got != "a: /g/core/x\nb: /data/core2/y\nc: /data/core\n" {
		t.Errorf("rebase = %q", got)
	}
	if got := string(rebase([]byte("/x/y"), "/", "/g")); got != "/x/y" {
		t.Errorf("rebase from / = %q", got)
	}
}

func TestCheckSocket(t *testing.T) {
	device := PathsIn("/private/var/mobile/Containers/Shared/AppGroup/0A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9").Controller
	if err := checkSocket(device); err != nil {
		t.Errorf("a device's path: %v", err)
	}
	if err := checkSocket("/" + strings.Repeat("x", 120)); err == nil {
		t.Error("an overlong path passed")
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	for src, ok := range map[string]bool{"mixed-port: 7890\n": true, "a: [": false, "": false, "- 1\n": false} {
		p := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Validate(p); (err == nil) != ok {
			t.Errorf("Validate(%q) = %v", src, err)
		}
	}
	if Validate(filepath.Join(dir, "missing.yaml")) == nil {
		t.Error("a missing file validated")
	}
}

func TestTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte("1\n2\n3\n4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tail(p, 2); got != "3\n4" {
		t.Errorf("tail = %q", got)
	}
	if tail(p+"x", 2) != "" {
		t.Error("tail of a missing file")
	}
}
