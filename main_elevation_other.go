//go:build !windows

package main

import "lanflow/internal/split"

func isElevated() bool                  { return false }
func maybeElevate(split.Args, string) bool { return false }
