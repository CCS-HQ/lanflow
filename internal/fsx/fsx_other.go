//go:build !windows

package fsx

func hideTempFile(string)        {}
func unhideFile(string)          {}
func isReadOnlyDest(string) bool { return false }
