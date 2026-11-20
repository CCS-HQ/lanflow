package split

import "golang.org/x/sys/unix"

// processStart returns the start time of process pid (microseconds since
// the epoch), from the kernel's process table.
func processStart(pid int) (int64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	tv := kp.Proc.P_starttime
	return int64(tv.Sec)*1e6 + int64(tv.Usec), true
}
