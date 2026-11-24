//go:build !windows

package helper

func isPlatformTransport(error) bool { return false }
