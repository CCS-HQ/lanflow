//go:build !windows

package service

import (
	"fmt"
	"os"
	"syscall"
)

// openSource opens a file the installer copies as root. The last path
// component is never followed when it is a symbolic link (root would
// otherwise read whatever file the user points it at), and the opened
// file must be a regular one: O_NONBLOCK keeps a FIFO from blocking root,
// and the type is checked on the descriptor, not the path.
func openSource(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("不是普通文件: %s", path)
	}
	return f, nil
}
