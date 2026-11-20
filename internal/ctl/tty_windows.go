package ctl

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a console (not a pipe or a file).
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
