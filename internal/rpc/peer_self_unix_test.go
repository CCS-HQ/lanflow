//go:build !windows

package rpc

import "os"

func pidExeOfSelf() (int, string, error) {
	exe, err := pidExe(os.Getpid())
	return os.Getpid(), exe, err
}
