//go:build !darwin && !windows

package autostart

func apply(exe string, on bool) (bool, error) { return xdgApply(exe, on) }
func enabled(exe string) bool                 { return xdgEnabled(exe) }
