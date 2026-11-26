package app

import (
	"os"
	"path/filepath"
	"testing"
)

// unified-delay is on by default and on for settings files from before it
// was; turning it off is saved and survives a reload.
func TestUnifiedDelayDefaultOn(t *testing.T) {
	dir := t.TempDir()
	s, err := loadSettings(dir)
	if err != nil || !s.Advanced.UnifiedDelay {
		t.Fatalf("no file: unified=%v err=%v", s.Advanced.UnifiedDelay, err)
	}

	if err := os.WriteFile(filepath.Join(dir, settingsFile), []byte(`{"mixedPort":7890,"advanced":{"ipv6":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err = loadSettings(dir); err != nil || !s.Advanced.UnifiedDelay || !s.Advanced.IPv6 {
		t.Fatalf("old file: %+v err=%v", s.Advanced, err)
	}

	s.Advanced.UnifiedDelay = false
	if err := saveSettings(dir, s); err != nil {
		t.Fatal(err)
	}
	if s, err = loadSettings(dir); err != nil || s.Advanced.UnifiedDelay {
		t.Fatalf("turned off: unified=%v err=%v", s.Advanced.UnifiedDelay, err)
	}
}
