//go:build !windows

package syncfile

func isCloudPlaceholder(string) bool { return false }
