//go:build !windows

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func pathKey(s string) string { return s }

// prepareWorkDir creates dir (0700) and requires it to be a real
// directory owned by the helper's own user, root. Its parent is ConfigDir,
// which only root writes, so nobody else can have put it there.
func prepareWorkDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s 不是目录", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s 不属于权限组件", dir)
	}
	return os.Chmod(dir, 0o700)
}

// openUserFile opens a file of the installing user's for reading. path
// (symlinks resolved at validation) lies in the user's data directory,
// which the user can rewrite at any moment, and the helper reads it as
// root: no component is followed when it is a symbolic link, and the
// opened file must be a regular one with a single link that belongs to
// that user. A link (symbolic or hard) to a file only root may read is
// therefore never copied to where the core, and through its controller
// the user, would read it.
func openUserFile(path string, uid string) (*os.File, error) {
	n, err := parseUID(uid)
	if err != nil {
		return nil, err
	}
	dirfd, err := openDirNoFollow(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirfd)
	fd, err := unix.Openat(dirfd, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	switch {
	case fileMode(&st) != unix.S_IFREG:
		err = fmt.Errorf("%w: 不是普通文件", errUnsafeFile)
	case st.Nlink != 1:
		err = fmt.Errorf("%w: 存在 %d 个硬链接", errUnsafeFile, st.Nlink)
	case int(st.Uid) != n:
		err = fmt.Errorf("%w: 属主 %d", errUnsafeFile, st.Uid)
	}
	if err == nil {
		err = unix.SetNonblock(fd, false)
	}
	if err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// listUserDir names the entries of a directory of the user's (symlinks
// resolved at validation), at most maxSeedEntries of them; none when it
// cannot be read. It is opened as a directory without following a link,
// so a FIFO put in its place does not block the helper. The names are
// only candidates: each file is checked when it is opened.
func listUserDir(dir string) []string {
	fd, err := openDirNoFollow(dir)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	names, _ := f.Readdirnames(maxSeedEntries)
	return names
}
