//go:build !windows && !darwin && !linux

package split

// processStart is not implemented here: nothing is pinned.
func processStart(int) (int64, bool) { return 0, false }
