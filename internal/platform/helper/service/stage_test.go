//go:build !windows

package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stageFixture is a supervisor with a work directory of its own next to a
// user data directory (fixture), and the spec as Validate returns it.
func stageFixture(t *testing.T) (*Supervisor, *fixture) {
	t.Helper()
	f := newFixture(t)
	s := NewSupervisor(f.policy)
	s.Hooks = Hooks{}
	s.Logf = t.Logf
	s.WorkDir = filepath.Join(resolvedTempDir(t), "helper-work")
	resolved, err := f.policy.Validate(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	f.spec = resolved
	return s, f
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func TestStageCopiesConfigAndData(t *testing.T) {
	s, f := stageFixture(t)
	mustWrite(t, f.spec.Config, "mixed-port: 7890\nexternal-ui: ui\nexternal-controller: 0.0.0.0:9090\n"+
		"rule-providers:\n  ads: {type: http, behavior: domain, url: 'https://e/ads', path: ./ruleset/ads.yaml}\n")
	user := f.spec.WorkDir
	mustWrite(t, filepath.Join(user, "geoip.metadb"), "GEOIP")
	mustWrite(t, filepath.Join(user, "GeoSite.dat"), "GEOSITE")
	mustWrite(t, filepath.Join(user, "ruleset", "ads.yaml"), "payload: [a.com]")
	mustWrite(t, filepath.Join(user, "proxies", "0123abcd"), "proxies: []")
	mustWrite(t, filepath.Join(user, "rules", "4567"), "payload: []")
	// Not the core's data: stays where it is.
	mustWrite(t, filepath.Join(user, "cache.db"), "CACHE")
	mustWrite(t, filepath.Join(user, "mihomo-tun.log"), "LOG")
	mustWrite(t, filepath.Join(user, "bin", "mihomo"), "BIN")

	path, err := s.stage(f.spec, true)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(s.WorkDir, stagedConfigName) {
		t.Fatalf("staged config = %s", path)
	}
	cfg := readOr(path)
	if strings.Contains(cfg, "external-") || !strings.Contains(cfg, "mixed-port: 7890") || !strings.Contains(cfg, "ruleset/ads.yaml") {
		t.Fatalf("staged config:\n%s", cfg)
	}
	for rel, want := range map[string]string{
		"geoip.metadb": "GEOIP", "GeoSite.dat": "GEOSITE", "ruleset/ads.yaml": "payload: [a.com]",
		"proxies/0123abcd": "proxies: []", "rules/4567": "payload: []",
	} {
		if got := readOr(filepath.Join(s.WorkDir, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	for _, rel := range []string{"cache.db", "mihomo-tun.log", "bin"} {
		if _, err := os.Lstat(filepath.Join(s.WorkDir, rel)); err == nil {
			t.Errorf("%s was copied", rel)
		}
	}
	// Nothing temporary is left, and nothing is readable by others.
	entries, _ := os.ReadDir(s.WorkDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lanflow-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
		if fi, err := e.Info(); err == nil && fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", e.Name(), fi.Mode())
		}
	}
	// The user's files were only read.
	if got := readOr(f.spec.Config); !strings.Contains(got, "external-ui") {
		t.Fatal("the user's config was rewritten")
	}
}

// A reload stages the config and the provider files it names, not the
// databases.
func TestStageWithoutData(t *testing.T) {
	s, f := stageFixture(t)
	mustWrite(t, f.spec.Config, "proxy-providers:\n  a: {type: http, url: 'https://e/a', path: p/a.yaml}\n")
	mustWrite(t, filepath.Join(f.spec.WorkDir, "geoip.metadb"), "GEOIP")
	mustWrite(t, filepath.Join(f.spec.WorkDir, "p", "a.yaml"), "proxies: []")
	if _, err := s.stage(f.spec, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(s.WorkDir, "geoip.metadb")); err == nil {
		t.Error("a reload copied the databases")
	}
	if got := readOr(filepath.Join(s.WorkDir, "p", "a.yaml")); got != "proxies: []" {
		t.Errorf("provider file = %q", got)
	}
}

// The databases are tens of megabytes: copied when missing or when the
// user's copy is newer, and never over a newer one the core fetched.
func TestSeedFileOnlyWhenNewer(t *testing.T) {
	s, f := stageFixture(t)
	src := filepath.Join(f.spec.WorkDir, "geoip.metadb")
	dst := filepath.Join(s.WorkDir, "geoip.metadb")
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	mustWrite(t, src, "v1")
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}
	stage := func() {
		t.Helper()
		if _, err := s.stage(f.spec, true); err != nil {
			t.Fatal(err)
		}
	}
	stage()
	fi, err := os.Stat(dst)
	if err != nil || readOr(dst) != "v1" || !fi.ModTime().Equal(old) {
		t.Fatalf("first copy: %q %v %v", readOr(dst), fi, err)
	}

	// Unchanged source: the copy is left alone (same inode).
	before := inode(t, dst)
	stage()
	if inode(t, dst) != before {
		t.Fatal("an unchanged database was copied again")
	}

	// The privileged core downloaded a newer one: it stays.
	if err := os.WriteFile(dst, []byte("core"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage()
	if got := readOr(dst); got != "core" {
		t.Fatalf("the core's newer database was replaced by %q", got)
	}

	// The user's copy is updated later (an unprivileged run): it wins.
	mustWrite(t, src, "v2")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, later, later); err != nil {
		t.Fatal(err)
	}
	stage()
	if got := readOr(dst); got != "v2" {
		t.Fatalf("the user's newer database was not copied: %q", got)
	}
}

// What the user can plant in the data directory must not make root read
// another file: links of both kinds, other file types, and a directory
// swapped for a link after validation.
func TestStageRefusesLinksToOtherFiles(t *testing.T) {
	secret := filepath.Join(resolvedTempDir(t), "secret")
	mustWrite(t, secret, "mixed-port: 1\nROOT-ONLY\n")

	t.Run("config is a symlink", func(t *testing.T) {
		s, f := stageFixture(t)
		if err := os.Remove(f.spec.Config); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, f.spec.Config); err != nil {
			t.Fatal(err)
		}
		if path, err := s.stage(f.spec, true); err == nil {
			t.Fatalf("staged a symlinked config: %s", readOr(path))
		}
		if _, err := os.Lstat(filepath.Join(s.WorkDir, stagedConfigName)); err == nil {
			t.Fatal("a config was written")
		}
	})
	t.Run("config is a hard link", func(t *testing.T) {
		s, f := stageFixture(t)
		if err := os.Remove(f.spec.Config); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(secret, f.spec.Config); err != nil {
			t.Skipf("hard link: %v", err)
		}
		if _, err := s.stage(f.spec, true); err == nil {
			t.Fatal("staged a hard-linked config")
		}
	})
	t.Run("config is a FIFO", func(t *testing.T) {
		s, f := stageFixture(t)
		if err := os.Remove(f.spec.Config); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(f.spec.Config, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		done := make(chan error, 1)
		go func() { _, err := s.stage(f.spec, true); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("staged a FIFO")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a FIFO blocked the helper")
		}
	})
	t.Run("config of another user", func(t *testing.T) {
		s, f := stageFixture(t)
		s.Policy.Config.UID = strconv.Itoa(os.Getuid() + 1)
		if _, err := s.stage(f.spec, true); err == nil {
			t.Fatal("staged a config that does not belong to the installing user")
		}
	})
	t.Run("config directory swapped for a link", func(t *testing.T) {
		s, f := stageFixture(t)
		// The config lives in <data>/conf; after validation that directory
		// becomes a link to one with another file of the same name.
		conf := filepath.Join(f.dataDir, "conf")
		mustWrite(t, filepath.Join(conf, "runtime.yaml"), "mixed-port: 7890\n")
		spec := f.spec
		spec.Config = filepath.Join(filepath.Dir(f.spec.Config), "conf", "runtime.yaml")
		other := filepath.Join(resolvedTempDir(t), "other")
		mustWrite(t, filepath.Join(other, "runtime.yaml"), "mixed-port: 1\nROOT-ONLY\n")
		if err := os.RemoveAll(conf); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, conf); err != nil {
			t.Fatal(err)
		}
		if path, err := s.stage(spec, true); err == nil {
			t.Fatalf("followed a swapped directory: %s", readOr(path))
		}
	})
	t.Run("data files", func(t *testing.T) {
		s, f := stageFixture(t)
		user := f.spec.WorkDir
		mustWrite(t, f.spec.Config, "rule-providers:\n  a: {type: file, behavior: domain, path: linked/a.yaml}\n  b: {type: file, behavior: domain, path: b.yaml}\n")
		// A database that is a symlink, a provider file that is one, a
		// provider directory that is one, a hard link in a default dir.
		if err := os.Symlink(secret, filepath.Join(user, "geoip.metadb")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, filepath.Join(user, "b.yaml")); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(resolvedTempDir(t), "outside")
		mustWrite(t, filepath.Join(outside, "a.yaml"), "ROOT-ONLY")
		if err := os.Symlink(outside, filepath.Join(user, "linked")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(user, "proxies"), 0o700); err != nil {
			t.Fatal(err)
		}
		hardLinked := os.Link(secret, filepath.Join(user, "proxies", "hard")) == nil
		mustWrite(t, filepath.Join(user, "geosite.dat"), "GEOSITE")

		// The start goes on: the core fetches what was not copied.
		if _, err := s.stage(f.spec, true); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{"geoip.metadb", "b.yaml", "linked/a.yaml", "linked", "proxies/hard"} {
			if rel == "proxies/hard" && !hardLinked {
				continue
			}
			if _, err := os.Lstat(filepath.Join(s.WorkDir, rel)); err == nil {
				t.Errorf("%s was copied through a link: %q", rel, readOr(filepath.Join(s.WorkDir, rel)))
			}
		}
		if got := readOr(filepath.Join(s.WorkDir, "geosite.dat")); got != "GEOSITE" {
			t.Errorf("the plain database was not copied: %q", got)
		}
	})
	t.Run("work dir swapped for a link", func(t *testing.T) {
		s, f := stageFixture(t)
		user := f.spec.WorkDir
		outside := filepath.Join(resolvedTempDir(t), "outside")
		mustWrite(t, filepath.Join(outside, "geoip.metadb"), "ROOT-ONLY")
		if err := os.RemoveAll(user); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, user); err != nil {
			t.Fatal(err)
		}
		if _, err := s.stage(f.spec, true); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(s.WorkDir, "geoip.metadb")); err == nil {
			t.Fatal("a database was copied through a swapped work dir")
		}
	})
}

// A link inside the helper's own directory (which only root writes; this
// is the second line) is not written through either.
func TestSeedFileStaysInsideWorkDir(t *testing.T) {
	s, f := stageFixture(t)
	if err := prepareWorkDir(s.WorkDir); err != nil {
		t.Fatal(err)
	}
	outside := resolvedTempDir(t)
	if err := os.Symlink(outside, filepath.Join(s.WorkDir, "ruleset")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.spec.WorkDir, "ruleset", "a.yaml"), "payload: []")
	root, err := os.OpenRoot(s.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if copied, err := seedFile(root, f.spec.WorkDir, "ruleset/a.yaml", myUID(), wideBudget()); err == nil || copied {
		t.Fatalf("wrote through a link out of the work dir: %v %v", copied, err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("files outside the work dir: %v", entries)
	}
	for _, rel := range []string{"../x", "/etc/x", ""} {
		if _, err := seedFile(root, f.spec.WorkDir, rel, myUID(), wideBudget()); err == nil {
			t.Errorf("seedFile accepted %q", rel)
		}
	}
}

func TestStageRefusesBadConfig(t *testing.T) {
	s, f := stageFixture(t)
	mustWrite(t, f.spec.Config, "mixed-port: 7890\n")
	if _, err := s.stage(f.spec, true); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(s.WorkDir, stagedConfigName)
	for name, cfg := range map[string]string{
		"provider path outside": "proxy-providers:\n  p: {type: http, url: 'https://e/x', path: /etc/cron.d/x}\n",
		"provider path up":      "rule-providers:\n  p: {type: http, behavior: domain, url: 'https://e/x', path: ../x}\n",
		"not YAML":              "a: [\n",
	} {
		mustWrite(t, f.spec.Config, cfg)
		if _, err := s.stage(f.spec, true); err == nil {
			t.Errorf("%s: staged", name)
		}
		// The previous copy is still the one in place.
		if got := readOr(staged); got != "mixed-port: 7890\n" {
			t.Errorf("%s: staged config is now %q", name, got)
		}
	}
}

func TestPrepareWorkDir(t *testing.T) {
	base := resolvedTempDir(t)
	dir := filepath.Join(base, "a", "work")
	if err := prepareWorkDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkDir(dir); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir).Perm(); got != 0o700 {
		t.Fatalf("mode = %v", got)
	}
	// A link or a file in its place is refused, not followed.
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkDir(link); err == nil {
		t.Fatal("a symlinked work dir was accepted")
	}
	if got := modeOf(t, target).Perm(); got != 0o755 {
		t.Fatalf("the link's target was changed: %v", got)
	}
	file := filepath.Join(base, "file")
	mustWrite(t, file, "x")
	if err := prepareWorkDir(file); err == nil {
		t.Fatal("a file was accepted as work dir")
	}

	s, f := stageFixture(t)
	s.WorkDir = ""
	if _, err := s.stage(f.spec, true); err == nil {
		t.Fatal("staged without a work dir")
	}
	s.WorkDir = "relative"
	if _, err := s.stage(f.spec, true); err == nil {
		t.Fatal("staged into a relative work dir")
	}
}

func wideBudget() *seedBudget {
	return &seedBudget{files: maxSeedFiles, bytes: maxSeedBytes, deadline: time.Now().Add(time.Minute)}
}

// Review 1: nothing copied from the user's directory can end up as the
// config the core loads. The app keeps runtime.yaml in the work directory
// it seeds from; a provider path naming it is refused, seedFile never
// writes that name, and the checked config is written after every copy.
func TestStageConfigIsNotOverwrittenBySeeds(t *testing.T) {
	s, f := stageFixture(t)
	spec := f.spec
	spec.Config = filepath.Join(spec.WorkDir, "runtime.yaml") // the app's layout
	future := time.Now().Add(24 * time.Hour)
	for _, p := range []string{"runtime.yaml", "RUNTIME.YAML", "./x/../runtime.yaml"} {
		mustWrite(t, spec.Config, "mixed-port: 7890\nexternal-controller: 0.0.0.0:9090\nrule-providers:\n  p: {type: file, behavior: domain, path: '"+p+"'}\n")
		if err := os.Chtimes(spec.Config, future, future); err != nil {
			t.Fatal(err)
		}
		if path, err := s.stage(spec, true); err == nil {
			t.Fatalf("path %q: staged:\n%s", p, readOr(path))
		}
	}
	// Without such a path the config is staged, cleaned, however new the
	// user's file is.
	mustWrite(t, spec.Config, "mixed-port: 7890\nexternal-controller: 0.0.0.0:9090\n")
	if err := os.Chtimes(spec.Config, future, future); err != nil {
		t.Fatal(err)
	}
	path, err := s.stage(spec, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := readOr(path); strings.Contains(got, "external-controller") {
		t.Fatalf("staged config:\n%s", got)
	}
	// seedFile itself refuses the name, in any spelling.
	root, err := os.OpenRoot(s.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, rel := range []string{"runtime.yaml", "RUNTIME.YAML", "Runtime.yaml/x", ".lanflow-00"} {
		if copied, err := seedFile(root, spec.WorkDir, rel, myUID(), wideBudget()); err == nil || copied {
			t.Errorf("seedFile(%q) = %v, %v", rel, copied, err)
		}
	}
	if got := readOr(path); strings.Contains(got, "external-controller") {
		t.Fatalf("staged config after seedFile:\n%s", got)
	}
}

// Review 4: a directory where one of the core's files belongs (made
// through a provider path by an earlier version) is repaired, not left
// for root to clean up by hand.
func TestStageRepairsDirectoriesInPlaceOfFiles(t *testing.T) {
	s, f := stageFixture(t)
	for _, name := range []string{"geoip.metadb", "cache.db", stagedConfigName, "ruleset/ads.yaml"} {
		mustWrite(t, filepath.Join(s.WorkDir, name, "x", "junk"), "junk")
	}
	if err := os.Chmod(s.WorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, f.spec.Config, "rule-providers:\n  ads: {type: http, behavior: domain, url: 'https://e/a', path: ruleset/ads.yaml}\n")
	mustWrite(t, filepath.Join(f.spec.WorkDir, "geoip.metadb"), "GEOIP")
	mustWrite(t, filepath.Join(f.spec.WorkDir, "ruleset", "ads.yaml"), "payload: []")
	path, err := s.stage(f.spec, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := readOr(filepath.Join(s.WorkDir, "geoip.metadb")); got != "GEOIP" {
		t.Errorf("geoip.metadb = %q", got)
	}
	if got := readOr(filepath.Join(s.WorkDir, "ruleset", "ads.yaml")); got != "payload: []" {
		t.Errorf("provider file = %q", got)
	}
	if got := readOr(path); !strings.Contains(got, "rule-providers") {
		t.Errorf("config = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(s.WorkDir, "cache.db")); err == nil {
		t.Error("the cache.db directory is still there: the core could not create its cache")
	}
}

// Review 5: one stage copies a bounded number of files and bytes in a
// bounded time.
func TestSeedFileBudget(t *testing.T) {
	s, f := stageFixture(t)
	if err := prepareWorkDir(s.WorkDir); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(s.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(f.spec.WorkDir, name), strings.Repeat("x", 1000))
	}
	seed := func(name string, b *seedBudget) (bool, error) {
		return seedFile(root, f.spec.WorkDir, name, myUID(), b)
	}
	b := &seedBudget{files: 10, bytes: 1500, deadline: time.Now().Add(time.Minute)}
	if copied, err := seed("a", b); err != nil || !copied || b.bytes != 500 || b.files != 9 {
		t.Fatalf("first file: %v %v, budget %+v", copied, err, b)
	}
	if copied, err := seed("b", b); !errors.Is(err, errSeedBudget) || copied {
		t.Fatalf("a file beyond the byte budget: %v %v", copied, err)
	}
	b = &seedBudget{files: 1, bytes: 1 << 20, deadline: time.Now().Add(time.Minute)}
	if _, err := seed("b", b); err != nil {
		t.Fatal(err)
	}
	if copied, err := seed("c", b); !errors.Is(err, errSeedBudget) || copied {
		t.Fatalf("a file beyond the file budget: %v %v", copied, err)
	}
	b = &seedBudget{files: 10, bytes: 1 << 20, deadline: time.Now().Add(-time.Second)}
	if copied, err := seed("c", b); !errors.Is(err, errSeedBudget) || copied {
		t.Fatalf("a file after the deadline: %v %v", copied, err)
	}
	for _, name := range []string{"c", ".lanflow-x"} {
		if _, err := os.Lstat(filepath.Join(s.WorkDir, name)); err == nil {
			t.Errorf("%s exists", name)
		}
	}
	entries, _ := os.ReadDir(s.WorkDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
	// A copy cut short by the deadline is not left half written.
	if _, err := io.Copy(io.Discard, &deadlineReader{r: strings.NewReader("x"), deadline: time.Now().Add(-time.Second)}); !errors.Is(err, errSeedBudget) {
		t.Fatalf("deadlineReader: %v", err)
	}
}

// Review 5: before a start, files nothing refers to any more go; what the
// core may still use stays.
func TestStagePrunesOldFiles(t *testing.T) {
	s, f := stageFixture(t)
	old := time.Now().Add(-2 * pruneAge)
	put := func(rel string, age time.Time) {
		t.Helper()
		p := filepath.Join(s.WorkDir, rel)
		mustWrite(t, p, "x")
		if err := os.Chtimes(p, age, age); err != nil {
			t.Fatal(err)
		}
	}
	recent := time.Now().Add(-time.Hour)
	put("cache.db", old)
	put("GeoSite.dat", old)
	put("geoip.metadb", old)
	put("ruleset/current.yaml", old) // named by the config
	put("ruleset/left.yaml", old)    // a subscription the user left
	put("gone/deep/left.yaml", old)
	put("proxies/hashed-user", old) // the user's directory has it too
	put("proxies/hashed-core-old", old)
	put("proxies/hashed-core", recent) // the core refreshed it
	put("unknown-recent", recent)
	put(tempPrefix+"0123", recent)
	mustWrite(t, f.spec.Config, "rule-providers:\n  c: {type: http, behavior: domain, url: 'https://e/c', path: ruleset/current.yaml}\n")
	mustWrite(t, filepath.Join(f.spec.WorkDir, "proxies", "hashed-user"), "x")
	userOld := old.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(f.spec.WorkDir, "proxies", "hashed-user"), userOld, userOld); err != nil {
		t.Fatal(err)
	}
	exists := func(rel string) bool {
		_, err := os.Lstat(filepath.Join(s.WorkDir, rel))
		return err == nil
	}
	gone := []string{"ruleset/left.yaml", "gone", "proxies/hashed-core-old", tempPrefix + "0123"}

	// A reload removes nothing: the core is running on these files.
	if _, err := s.stage(f.spec, false); err != nil {
		t.Fatal(err)
	}
	for _, rel := range gone {
		if !exists(rel) {
			t.Fatalf("a reload removed %s", rel)
		}
	}
	if _, err := s.stage(f.spec, true); err != nil {
		t.Fatal(err)
	}
	for _, rel := range gone {
		if exists(rel) {
			t.Errorf("%s was kept", rel)
		}
	}
	for _, rel := range []string{"cache.db", "GeoSite.dat", "geoip.metadb", "ruleset/current.yaml", "proxies/hashed-user", "proxies/hashed-core", "unknown-recent", stagedConfigName} {
		if !exists(rel) {
			t.Errorf("%s was removed", rel)
		}
	}
}
