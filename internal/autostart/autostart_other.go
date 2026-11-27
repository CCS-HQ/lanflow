//go:build (!darwin || ios) && !windows

package autostart

func applyElevated(exe string, on, _ bool) (bool, error) { return apply(exe, on) }
func apply(exe string, on bool) (bool, error)            { return xdgApply(exe, on) }
func enabled(exe string) bool                            { return xdgEnabled(exe) }
