//go:build !darwin && !linux && !windows

package ctl

import "os"

// Elsewhere every run counts as a script: its writes ask the user.
func isTerminal(*os.File) bool { return false }
