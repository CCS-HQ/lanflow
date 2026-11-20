package autostart

import "strings"

// runStore is the user's Run value and Task Manager's switch for it, as
// the Windows implementation uses the registry; tests use a fake.
type runStore interface {
	// Get returns the Run command (ok = the value exists).
	Get() (cmd string, ok bool)
	Set(cmd string) error
	Delete() error
	// ClearApproval removes Task Manager's "disabled" record
	// (StartupApproved), reporting whether one was there.
	ClearApproval() bool
}

// applyRun makes the Run value say want (on) or removes it (off).
//
// Task Manager's Startup page turns an entry off in StartupApproved and
// leaves the Run value in place. That record is only cleared when the Run
// value is created, i.e. when the user turns 开机时启动 on in Lanflow; it is
// left alone while the value already exists. Apply runs on every settings
// change and at every start, and clearing it each time would switch an
// entry the user disabled in Task Manager back on behind their back.
func applyRun(st runStore, want string, on bool) (bool, error) {
	have, ok := st.Get()
	if !on {
		if !ok {
			return false, nil
		}
		return true, st.Delete()
	}
	switch {
	case !ok:
		if err := st.Set(want); err != nil {
			return false, err
		}
		st.ClearApproval()
		return true, nil
	case !strings.EqualFold(have, want):
		// The executable moved: the same entry, the user's choice in Task
		// Manager stays.
		return true, st.Set(want)
	}
	return false, nil
}
