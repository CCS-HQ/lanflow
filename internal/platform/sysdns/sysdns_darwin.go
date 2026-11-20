//go:build darwin

package sysdns

import "context"

const supported = true

func snapshot() (Saved, error) { return darwinSnapshot(context.Background(), runCommand) }

func set(saved Saved, servers []string) error {
	only, err := snapshotServices(saved)
	if err != nil {
		return err
	}
	return darwinSet(context.Background(), runCommand, servers, only)
}

func restore(s Saved, ours []string) error {
	return darwinRestore(context.Background(), runCommand, s, ours)
}
