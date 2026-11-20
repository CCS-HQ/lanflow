package rpc

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

func sockPeerUID(fd int) (int, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}

// sockPeerPID is the process that connected (LOCAL_PEERPID).
func sockPeerPID(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}

// proc_info(2) as libproc's proc_pidpath calls it.
const (
	procInfoCallPIDInfo = 2
	procPIDPathInfo     = 11
	procPIDPathMax      = 4096 // PROC_PIDPATHINFO_MAXSIZE
)

// pidExe is the executable of pid, from the kernel's record of the
// process image (proc_pidpath). Unlike kern.procargs2 the process itself
// cannot rewrite it.
func pidExe(pid int) (string, error) {
	buf := make([]byte, procPIDPathMax)
	_, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(pid), procPIDPathInfo, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return "", errno
	}
	return unix.ByteSliceToString(buf), nil
}
