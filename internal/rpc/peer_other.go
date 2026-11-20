//go:build !windows && !darwin && !linux

package rpc

import "os"

// Elsewhere the 0700 directory and the 0600 socket keep other users out.
func sockPeerUID(int) (int, error) { return os.Getuid(), nil }

// Elsewhere the peer process is not identified: every peer of the user
// counts as Lanflow itself.
func sockPeerPID(int) (int, error) { return os.Getpid(), nil }

func pidExe(int) (string, error) { return os.Executable() }
