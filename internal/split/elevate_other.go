//go:build !windows

package split

import "errors"

// IsElevated always reports false on non-Windows platforms.
func IsElevated() bool { return false }

// ProcessElevated always reports false on non-Windows platforms.
func ProcessElevated(int) (bool, error) { return false, nil }

// RunElevated is only supported on Windows.
func RunElevated(string, ...string) error { return errors.New("elevation unsupported on this platform") }
