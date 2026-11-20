package rpc

import "os"

// On Windows the path of a process is read through its pipe connection
// (peerProc); the handshake tests cover it.
func pidExeOfSelf() (int, string, error) {
	exe, err := os.Executable()
	return os.Getpid(), exe, err
}
