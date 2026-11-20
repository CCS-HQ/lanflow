package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBin(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHelperDigestPinned(t *testing.T) {
	bin := writeBin(t, "lanflow-helper", "#!/bin/sh\nexit 0\n")
	sum, _ := fileSHA256(bin)
	old := ExpectedHelperSHA256
	t.Cleanup(func() { ExpectedHelperSHA256 = old })

	ExpectedHelperSHA256 = ""
	if got, err := helperDigest(bin); err != nil || got != sum {
		t.Fatalf("unpinned: %s %v", got, err)
	}
	ExpectedHelperSHA256 = strings.ToUpper(sum)
	if got, err := helperDigest(bin); err != nil || got != sum {
		t.Fatalf("pinned: %s %v", got, err)
	}
	ExpectedHelperSHA256 = strings.Repeat("0", 64)
	if _, err := helperDigest(bin); err == nil {
		t.Fatal("mismatching helper accepted")
	}
	if _, err := helperDigest(bin + ".missing"); err == nil {
		t.Fatal("missing helper accepted")
	}
}

func TestCoreDigest(t *testing.T) {
	bin := writeBin(t, "mihomo", "core bytes")
	sum, err := fileSHA256(bin)
	if err != nil || len(sum) != 64 {
		t.Fatalf("sum %q: %v", sum, err)
	}
	for _, tc := range []struct {
		name, path, want string
		ok               bool
	}{
		{"no expectation: the digest of the file now", bin, "", true},
		{"expected digest", bin, sum, true},
		{"expected digest, other case and spaces", bin, " " + strings.ToUpper(sum) + "\n", true},
		{"another file than expected", bin, strings.Repeat("a", 64), false},
		{"unreadable", bin + ".missing", "", false},
	} {
		got, err := coreDigest(tc.path, tc.want)
		if (err == nil) != tc.ok || (tc.ok && got != sum) {
			t.Errorf("%s: %q, %v", tc.name, got, err)
		}
	}
}
