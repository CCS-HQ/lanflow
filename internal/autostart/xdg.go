package autostart

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// XDG autostart (Linux and the other Unix desktops): a desktop entry in
// $XDG_CONFIG_HOME/autostart that starts the service process. MyGo's own
// login item, which main removes when it moves the login start to the
// service, is such an entry too (it opens the window instead).

// xdgEnv and xdgHome are variables for tests.
var (
	xdgEnv  = os.Getenv
	xdgHome = os.UserHomeDir
)

func xdgEntryPath() (string, error) {
	dir := xdgEnv("XDG_CONFIG_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		h, err := xdgHome()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(h, ".config")
	}
	return filepath.Join(dir, "autostart", Label+".desktop"), nil
}

// xdgExecArg quotes one argument of a desktop entry's Exec key: in double
// quotes, with the characters the specification reserves there escaped by
// a backslash, the backslash itself doubled once more for the string
// value, and "%" doubled (a field code otherwise).
func xdgExecArg(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`)
			b.WriteRune(r)
		case '\\':
			b.WriteString(`\\\\`)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func xdgEntry(exe string) []byte {
	cmd := xdgExecArg(exe) + " " + strings.Join(Args, " ")
	return []byte("[Desktop Entry]\nType=Application\nName=Lanflow\nExec=" + cmd +
		"\nTerminal=false\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n")
}

func xdgApply(exe string, on bool) (bool, error) {
	// A path with a line break cannot be written to a desktop entry.
	if on && strings.ContainsAny(exe, "\n\r") {
		return false, errors.New("autostart: the executable path contains a line break")
	}
	path, err := xdgEntryPath()
	if err != nil {
		return false, err
	}
	if !on {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	want := xdgEntry(exe)
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, want) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func xdgEnabled(exe string) bool {
	path, err := xdgEntryPath()
	if err != nil {
		return false
	}
	have, err := os.ReadFile(path)
	return err == nil && bytes.Equal(have, xdgEntry(exe))
}
