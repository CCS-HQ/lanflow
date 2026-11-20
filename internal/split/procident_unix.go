//go:build !windows

package split

// pinProcess remembers when process pid started; same reports whether
// the pid still names a process started at that moment. A start time that
// cannot be read pins nothing.
func pinProcess(pid int) (same func() bool, release func()) {
	start, ok := processStart(pid)
	if !ok {
		return func() bool { return true }, func() {}
	}
	return func() bool {
		now, ok := processStart(pid)
		return ok && now == start
	}, func() {}
}
