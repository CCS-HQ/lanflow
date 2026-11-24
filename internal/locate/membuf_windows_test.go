//go:build windows

package locate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOffHeapWindows(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db.bin")
	want := bytes.Repeat([]byte("lanflow-geo-test"), 256)
	if err := os.WriteFile(p, want, 0o600); err != nil {
		t.Fatal(err)
	}
	data, free, err := readOffHeap(p)
	if err != nil {
		t.Fatalf("readOffHeap: %v", err)
	}
	defer free()
	if !bytes.Equal(data, want) {
		t.Fatalf("data mismatch: len=%d want=%d", len(data), len(want))
	}
}
