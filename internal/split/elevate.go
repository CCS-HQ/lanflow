package split

import (
	"time"

	"lanflow/internal/i18n"
)

// ErrElevationCanceled is returned when the user dismisses the Windows UAC prompt.
var ErrElevationCanceled = i18n.Error("已取消管理员授权")

// WaitExit waits until process pid exits or timeout elapses.
func WaitExit(pid int, timeout time.Duration) {
	if pid <= 0 || timeout <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for Alive(pid) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
}
