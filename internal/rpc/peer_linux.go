package rpc

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func sockPeerUID(fd int) (int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}

// sockPeerPID is the process that connected (SO_PEERCRED).
func sockPeerPID(fd int) (int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Pid), nil
}

// pidExe is the executable of pid (/proc/<pid>/exe). A binary replaced
// on disk while it runs (an update) reads as "<path> (deleted)".
func pidExe(pid int) (string, error) {
	p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(p, " (deleted)"), nil
}
