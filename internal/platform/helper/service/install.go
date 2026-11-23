package service

//lint:file-ignore U1000 some of this file is used on other platforms only.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lanflow/internal/platform/helper/protocol"
)

// InstallOptions are the arguments of `lanflow-helper install`, which runs
// elevated (root / Administrator).
type InstallOptions struct {
	// CoreBin is the Mihomo binary to copy into the privileged location.
	CoreBin string
	// CoreSHA256 is the hex SHA-256 of CoreBin as the app saw it before the
	// administrator prompt. CoreBin lies where the user (and anything
	// running as the user) can rewrite it, so the copy is only accepted
	// when it has this digest. Required.
	CoreSHA256 string
	// DataDir is the installing user's Lanflow data directory.
	DataDir string
	// UID identifies the installing user: decimal uid (macOS) or SID (Windows).
	UID string
	// Version is the version of the installing helper binary.
	Version string
	// AllowDowngrade permits replacing a newer installed helper.
	AllowDowngrade bool
}

// Logf is the logging callback used by the install and run entry points.
type Logf func(format string, args ...any)

func (o InstallOptions) check() error {
	switch {
	case o.CoreBin == "" || !filepath.IsAbs(o.CoreBin):
		return errors.New("--core 必须是 Mihomo 内核的绝对路径")
	case o.DataDir == "" || !filepath.IsAbs(o.DataDir):
		return errors.New("--data-dir 必须是数据目录的绝对路径")
	case o.UID == "":
		return errors.New("--uid 未指定")
	case !validSHA256(o.CoreSHA256):
		// An app from before the digest was passed. Refused rather than
		// trusted: without it nothing ties the file root copies to the one
		// the user agreed to.
		return errors.New("--core-sha256 未指定或不是 SHA-256：请用与权限组件同一版本的澜流来安装")
	}
	// Lstat: a symbolic link is refused (the app passes the resolved path).
	if fi, err := os.Lstat(o.CoreBin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("内核文件不可用: %s", o.CoreBin)
	}
	return nil
}

func validSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// maxStagedSize bounds what the installer copies as root/SYSTEM (a Mihomo
// binary is some tens of megabytes).
const maxStagedSize = 1 << 30

// errDigest: the staged copy is not the file the app hashed.
var errDigest = errors.New("内核文件与安装前校验的内容不一致（文件在授权期间被改动），已拒绝安装")

// stageFile copies src into a new file in dir, which must be writable by
// root/SYSTEM only, and returns the copy's path and hex SHA-256. src is
// opened without following a link in its last component and must be a
// regular file; when wantSHA is not empty the copy must have that digest.
// The digest is of the bytes written, so a source rewritten while (or
// after) it was read can never become the installed file unnoticed. On
// any failure the copy is removed.
func stageFile(src, dir string, mode os.FileMode, wantSHA string) (tmpName, sum string, err error) {
	in, err := openSource(src)
	if err != nil {
		return "", "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(dir, ".lanflow-*")
	if err != nil {
		return "", "", err
	}
	tmpName = tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			tmpName, sum = "", ""
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(in, maxStagedSize+1))
	if err != nil {
		return
	}
	if n > maxStagedSize {
		err = fmt.Errorf("文件过大: %s", src)
		return
	}
	if err = tmp.Sync(); err != nil {
		return
	}
	if err = tmp.Close(); err != nil {
		return
	}
	if err = os.Chmod(tmpName, mode); err != nil {
		return
	}
	sum = hex.EncodeToString(h.Sum(nil))
	if wantSHA != "" && !strings.EqualFold(sum, wantSHA) {
		err = errDigest
		return
	}
	return tmpName, sum, nil
}

// stageBytes writes data to a new file in dir with mode.
func stageBytes(dir string, data []byte, mode os.FileMode) (string, error) {
	tmp, err := os.CreateTemp(dir, ".lanflow-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// swap is one staged file and the path it replaces.
type swap struct{ tmp, dst string }

// oldSuffix names the previous file while an install is being committed.
const oldSuffix = ".lanflow-old"

// fileSwap replaces a set of installed files as one step: every file is
// staged first, commit moves them into place keeping the previous ones,
// and rollback puts those back, so a failed install or upgrade leaves the
// installation that was there (which the caller then starts again).
type fileSwap struct {
	swaps []swap
	done  []swap // committed, in order
	fresh []string
}

func (f *fileSwap) add(tmp, dst string) { f.swaps = append(f.swaps, swap{tmp, dst}) }

// commit moves every staged file onto its destination. On an error the
// files already moved are put back and the staged ones removed.
func (f *fileSwap) commit() error {
	for _, s := range f.swaps {
		had := true
		if err := os.Rename(s.dst, s.dst+oldSuffix); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				f.rollback()
				return err
			}
			had = false
		}
		if err := os.Rename(s.tmp, s.dst); err != nil {
			if had {
				_ = os.Rename(s.dst+oldSuffix, s.dst)
			}
			f.rollback()
			return err
		}
		if had {
			f.done = append(f.done, s)
		} else {
			f.fresh = append(f.fresh, s.dst)
		}
	}
	f.swaps = nil
	return nil
}

// rollback undoes commit (or discards what was only staged).
func (f *fileSwap) rollback() {
	for _, s := range f.swaps {
		_ = os.Remove(s.tmp)
	}
	for i := len(f.done) - 1; i >= 0; i-- {
		_ = os.Rename(f.done[i].dst+oldSuffix, f.done[i].dst)
	}
	for _, p := range f.fresh {
		_ = os.Remove(p)
	}
	f.swaps, f.done, f.fresh = nil, nil, nil
}

// finish drops the previous files once the new installation runs.
func (f *fileSwap) finish() {
	for _, s := range f.done {
		_ = os.Remove(s.dst + oldSuffix)
	}
	f.done, f.fresh = nil, nil
}

// checkDowngrade refuses to replace the helper recorded in configPath when
// it is newer than o.Version (both must be release versions; "dev" or a
// missing record never blocks).
func checkDowngrade(configPath string, o InstallOptions) error {
	if o.AllowDowngrade {
		return nil
	}
	cur, err := LoadConfig(configPath)
	if err != nil {
		return nil
	}
	if c, ok := protocol.CompareVersions(cur.HelperVersion, o.Version); ok && c > 0 {
		return fmt.Errorf("已安装的权限组件 %s 比要安装的 %s 更新，拒绝降级（如确需降级请加 --allow-downgrade）", cur.HelperVersion, o.Version)
	}
	return nil
}

// resolveDataDir cleans and resolves the data dir and requires it to be a
// directory owned by the installing user (unix).
func resolveDataDir(dir, uid string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("数据目录不可用: %w", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("数据目录不是目录: %s", dir)
	}
	ok, err := ownerMatches(resolved, uid)
	if err != nil {
		return "", fmt.Errorf("无法检查数据目录属主: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("数据目录不属于用户 %s: %s", uid, dir)
	}
	return resolved, nil
}
