package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lanflow/internal/platform/helper/protocol"
)

// The privileged core never runs on the user's files. Before a start the
// helper prepares its own work directory, which only root/SYSTEM can
// write (WorkDir: /Library/Application Support/Lanflow Helper/work on
// macOS, %ProgramData%\Lanflow Helper\work on Windows):
//
//   - the user's config is read without following a link, checked and
//     cleaned (sanitizeConfig) and written there as stagedConfigName;
//   - the databases and provider files the core would otherwise have to
//     download again are copied from the user's work directory when they
//     are missing there or the user's copy is newer (seedFile).
//
// Both are bounded (files, bytes, time), and before a start files nothing
// refers to any more are removed (pruneWorkDir).
//
// The core is then started with -d and -f on that directory. Nothing is
// copied back: what the core writes while it runs with privileges
// (cache.db, updated databases, provider files) stays in WorkDir and is
// used by the next privileged run. The log and the controller endpoint
// are still the user's (logfile_<os>.go, handOverSocket).
//
// Why: a core running as root/SYSTEM in a directory the user owns can be
// pointed anywhere through a link planted there (a provider path, the
// cache, the directory itself swapped after validation). In a directory
// the user cannot write, the core's own rule that every path stays inside
// its work directory holds.

// stagedConfigName is the config's name inside WorkDir.
const stagedConfigName = "runtime.yaml"

// maxSeedSize bounds one file copied into WorkDir (the largest database
// is some tens of megabytes).
const maxSeedSize = 256 << 20

// maxSeedEntries bounds how many files of one directory are looked at.
const maxSeedEntries = 256

// seedSlack is how much newer the user's file must be to replace the copy
// (file systems keep times at different precisions).
const seedSlack = 2 * time.Second

// geoNames are the databases Mihomo looks for in its work directory, in
// lower case (it accepts other spellings of some).
var geoNames = map[string]bool{
	"geoip.metadb":      true,
	"geoip.dat":         true,
	"geosite.dat":       true,
	"country.mmdb":      true,
	"asn.mmdb":          true,
	"geolite2-asn.mmdb": true,
}

// providerDirs are where Mihomo keeps the files of providers that name no
// path (a hash of the URL as the file name).
var providerDirs = []string{"proxies", "rules"}

// tempPrefix starts the names of the helper's temporary files in WorkDir.
const tempPrefix = ".lanflow-"

// Limits of one stage. The databases are some tens of megabytes together
// and provider files are small; what is beyond these numbers is not
// copied, and the core fetches it.
const (
	// maxSeedFiles bounds how many files one stage looks at.
	maxSeedFiles = 1024
	// maxSeedBytes bounds how much one stage copies in all.
	maxSeedBytes = 768 << 20
	// seedTimeout bounds how long one stage spends copying. A start waits
	// for it, and the app waits 15 s for a start.
	seedTimeout = 8 * time.Second
	// pruneAge is how long a file nothing refers to stays in WorkDir.
	pruneAge = 30 * 24 * time.Hour
	// maxPruneEntries bounds how many entries one clean-up looks at.
	maxPruneEntries = 20000
)

// seedBudget is what is left of a stage's limits.
type seedBudget struct {
	files    int
	bytes    int64
	deadline time.Time
}

var errSeedBudget = errors.New("已达到单次复制的文件数、总大小或时间上限")

// stage prepares s.WorkDir for a run of spec (validated: WorkDir and
// Config are the user's, symlinks resolved) and returns the config the
// core is to load. withData also copies the databases and removes old
// files nothing refers to; a reload only needs the config and the
// provider files it names.
//
// A config that cannot be read or is refused fails the call, and nothing
// is changed. A database or provider file that cannot be copied does not:
// the core fetches it, as it does on a first run.
//
// The config is written last, after every copy: no file copied from the
// user's directory, under whatever name it reaches the work directory
// (another spelling, a short name), can take the place of the config that
// was checked.
//
// The caller holds stageMu, not mu: this reads and copies megabytes, and
// status, stop and ping must go on answering meanwhile.
func (s *Supervisor) stage(spec protocol.Spec, withData bool) (string, error) {
	if s.WorkDir == "" || !filepath.IsAbs(s.WorkDir) {
		return "", errors.New("权限组件没有可用的内核工作目录")
	}
	uid := s.Policy.Config.UID
	data, err := readUserFile(spec.Config, uid, maxConfigSize)
	if err != nil {
		return "", fmt.Errorf("无法读取配置文件（不能是符号链接、硬链接或他人的文件，上限 %d MB）: %w", maxConfigSize>>20, err)
	}
	cfg, err := sanitizeConfig(data)
	if err != nil {
		return "", fmt.Errorf("权限组件拒绝了这份配置: %w", err)
	}
	if len(cfg.Removed) > 0 {
		s.Logf("config: removed %s", strings.Join(cfg.Removed, ", "))
	}

	prepare := s.PrepareWorkDir
	if prepare == nil {
		prepare = prepareWorkDir
	}
	if err := prepare(s.WorkDir); err != nil {
		return "", fmt.Errorf("无法准备权限组件的内核工作目录: %w", err)
	}
	// Everything written below goes through root, which refuses a name
	// that leaves the directory, also through a link.
	root, err := os.OpenRoot(s.WorkDir)
	if err != nil {
		return "", fmt.Errorf("无法打开权限组件的内核工作目录: %w", err)
	}
	defer root.Close()

	var names []string
	if withData {
		for _, name := range listUserDir(spec.WorkDir) {
			if geoNames[strings.ToLower(name)] {
				names = append(names, name)
			}
		}
	}
	names = append(names, cfg.ProviderPaths...)
	hashed := map[string]bool{} // provider files without a path the user has
	for _, dir := range providerDirs {
		for _, name := range listUserDir(filepath.Join(spec.WorkDir, dir)) {
			rel := filepath.Join(dir, name)
			hashed[rel] = true
			names = append(names, rel)
		}
	}
	budget := &seedBudget{files: maxSeedFiles, bytes: maxSeedBytes, deadline: time.Now().Add(seedTimeout)}
	for _, rel := range names {
		copied, err := seedFile(root, spec.WorkDir, rel, uid, budget)
		switch {
		case errors.Is(err, errSeedBudget):
			s.Logf("work dir: %s and what follows not copied: %v", rel, err)
		case err != nil:
			s.Logf("work dir: %s not copied: %v", rel, err)
		case copied:
			s.Logf("work dir: copied %s", rel)
		}
		if errors.Is(err, errSeedBudget) {
			break
		}
	}
	if withData {
		keep := map[string]bool{}
		for _, rel := range cfg.ProviderPaths {
			keep[rel] = true
		}
		for rel := range hashed {
			keep[rel] = true
		}
		if n := pruneWorkDir(root, keep, time.Now().Add(-pruneAge)); n > 0 {
			s.Logf("work dir: removed %d old files nothing refers to", n)
		}
	}
	if err := writeRootFile(root, stagedConfigName, cfg.YAML); err != nil {
		return "", fmt.Errorf("无法写入内核配置: %w", err)
	}
	return filepath.Join(s.WorkDir, stagedConfigName), nil
}

// pruneWorkDir removes from the work directory the files nothing refers
// to any more and returns how many. It runs before a start, while no core
// uses the directory.
//
// It is deliberately timid. Kept are: the config, the databases and the
// cache (top level, by name), every file in keep (the providers of the
// config being staged and the provider files the user's directory has),
// and anything changed since before: the core rewrites what it uses, so a
// provider it downloaded itself under a name the helper cannot compute
// stays as long as it is refreshed. What goes is, in practice, the
// provider files of subscriptions the user left a month ago. Losing a
// file here costs the core one download.
func pruneWorkDir(root *os.Root, keep map[string]bool, before time.Time) (removed int) {
	seen := 0
	var dirs []string
	_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return nil
		}
		if seen++; seen > maxPruneEntries {
			return fs.SkipAll
		}
		rel := filepath.FromSlash(p)
		first, _, nested := strings.Cut(rel, string(filepath.Separator))
		if !nested && (strings.EqualFold(first, stagedConfigName) || strings.EqualFold(first, "cache.db") || geoNames[strings.ToLower(first)]) {
			if d.IsDir() {
				// A directory where one of the core's own files belongs
				// keeps the core from ever creating that file.
				if root.RemoveAll(rel) == nil {
					removed++
				}
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		if keep[rel] {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		// A temporary file of a stage that was interrupted goes at once.
		if strings.HasPrefix(d.Name(), tempPrefix) || fi.ModTime().Before(before) {
			if root.Remove(rel) == nil {
				removed++
			}
		}
		return nil
	})
	// Directories that are empty now, deepest first (Remove fails on the
	// others).
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = root.Remove(dirs[i])
	}
	return removed
}

// readUserFile reads a file of the user's, at most limit bytes of it.
func readUserFile(path, uid string, limit int64) ([]byte, error) {
	f, err := openUserFile(path, uid)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("文件过大: %s", path)
	}
	return data, nil
}

// tempName returns a new name next to rel.
func tempName(rel string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(rel), tempPrefix+hex.EncodeToString(b[:])), nil
}

// writeRootFile writes data as rel inside root (temporary file + rename),
// readable by root/SYSTEM only: the config carries the user's node
// passwords.
func writeRootFile(root *os.Root, rel string, data []byte) (err error) {
	tmp, err := tempName(rel)
	if err != nil {
		return err
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = root.Remove(tmp)
		}
	}()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if fi, lerr := root.Lstat(rel); lerr == nil && !fi.Mode().IsRegular() {
		if err = root.RemoveAll(rel); err != nil {
			return err
		}
	}
	err = root.Rename(tmp, rel)
	return err
}

// seedFile copies the user's file rel (relative to userDir, local) to the
// same name inside root when root has none or the user's is newer by more
// than seedSlack; the copy keeps the source's time, so an unchanged file
// is copied once. A file the privileged core has written since is newer
// and stays. It reports whether it copied. A source that does not exist
// is not an error. What it copies is taken from budget; errSeedBudget
// means the stage's limits are used up.
//
// The config's own name is never a target, whatever asks for it. A target
// that is something else than a regular file (a directory left where a
// database belongs, by an earlier version or by anything else) is removed
// and written anew: only root could repair it otherwise.
func seedFile(root *os.Root, userDir, rel, uid string, budget *seedBudget) (copied bool, err error) {
	if !filepath.IsLocal(rel) {
		return false, fmt.Errorf("不是工作目录内的相对路径: %s", rel)
	}
	rel = filepath.Clean(rel)
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	if strings.EqualFold(first, stagedConfigName) || strings.HasPrefix(first, tempPrefix) {
		return false, fmt.Errorf("不能覆盖内核配置: %s", rel)
	}
	if budget.files <= 0 || budget.bytes <= 0 || time.Now().After(budget.deadline) {
		return false, errSeedBudget
	}
	budget.files--
	src, err := openUserFile(filepath.Join(userDir, rel), uid)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer src.Close()
	sfi, err := src.Stat()
	if err != nil {
		return false, err
	}
	if sfi.Size() > maxSeedSize {
		return false, fmt.Errorf("文件过大: %s", rel)
	}
	replace := false
	if dfi, err := root.Lstat(rel); err == nil {
		if !dfi.Mode().IsRegular() {
			replace = true
		} else if sfi.ModTime().Sub(dfi.ModTime()) <= seedSlack {
			return false, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if sfi.Size() > budget.bytes {
		return false, errSeedBudget
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return false, err
		}
	}
	tmp, err := tempName(rel)
	if err != nil {
		return false, err
	}
	dst, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = root.Remove(tmp)
		}
	}()
	// One byte more than allowed is read to notice a source that grew (or
	// a sparse one whose size said little).
	limit := min(int64(maxSeedSize), budget.bytes)
	n, err := io.Copy(dst, &deadlineReader{r: io.LimitReader(src, limit+1), deadline: budget.deadline})
	budget.bytes -= n
	if err == nil && n > limit {
		err = errSeedBudget
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if err = root.Chtimes(tmp, sfi.ModTime(), sfi.ModTime()); err != nil {
		return false, err
	}
	if replace {
		if err = root.RemoveAll(rel); err != nil {
			return false, err
		}
	}
	if err = root.Rename(tmp, rel); err != nil {
		return false, err
	}
	return true, nil
}

// deadlineReader stops a copy that runs past the stage's time limit.
type deadlineReader struct {
	r        io.Reader
	deadline time.Time
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	if time.Now().After(d.deadline) {
		return 0, errSeedBudget
	}
	return d.r.Read(p)
}
