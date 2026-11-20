//go:build darwin

package service

import (
	"bytes"
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func systemProcs() procs {
	return procs{
		exePath: exePath,
		alive:   pidAlive,
		signal:  syscall.Kill,
		grace:   StopGrace,
		poll:    100 * time.Millisecond,
	}
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// exePath returns the path process pid was executed with, from
// kern.procargs2: a 4-byte argc followed by the NUL-terminated path given
// to execve. The helper starts Mihomo by its absolute path, so this is
// that path for a core of ours.
func exePath(pid int) (string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", err
	}
	if len(b) <= 4 {
		return "", errors.New("kern.procargs2: short answer")
	}
	b = b[4:]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b), nil
}
