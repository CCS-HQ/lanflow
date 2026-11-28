//go:build linux && !android

package sysproxy

import "context"

func snapshot() (Saved, error) { return gnomeSnapshot(context.Background(), runCommand) }

func restore(s Saved) error { return gnomeRestore(context.Background(), runCommand, s) }

func releaseDialUp(Saved, string, int, string) error { return nil }
