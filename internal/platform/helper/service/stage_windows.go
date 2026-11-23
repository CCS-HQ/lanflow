//go:build windows

package service

import (
	"os"

	"golang.org/x/sys/windows"
)

// prepareWorkDir creates dir (below LogDir in %ProgramData%) with a DACL
// for SYSTEM and Administrators only, or re-applies it. A directory that
// is a reparse point or that an ordinary user created is refused, as for
// LogDir (secureAdminDir).
func prepareWorkDir(dir string) error {
	if err := PrepareLogDir(); err != nil {
		return err
	}
	return secureAdminDir(dir)
}

// openUserFile opens a file of the installing user's for reading. path
// (links resolved at validation) lies in the user's data directory and is
// opened by SYSTEM: a reparse point is opened itself, not followed, and
// refused; the file must have a single link and its final path must be
// path itself, so a junction swapped into a directory above it is
// detected (checkOpened).
func openUserFile(path string, _ string) (*os.File, error) {
	h, err := openExistingNoReparse(path, windows.GENERIC_READ)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// listUserDir names the entries of a directory of the user's, at most
// maxSeedEntries of them; none when it cannot be read. The names are only
// candidates: each file is checked when it is opened (openUserFile), which
// is also what catches a directory that was redirected.
func listUserDir(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	names, _ := f.Readdirnames(maxSeedEntries)
	return names
}
