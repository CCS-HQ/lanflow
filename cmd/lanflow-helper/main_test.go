package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lanflow/internal/platform/helper/service"
)

func TestParseInstall(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name string
		args []string
		want service.InstallOptions
		ok   bool
	}{
		{"what the app passes",
			[]string{"--core", "/Apps/Lanflow.app/mihomo", "--core-sha256", sum, "--data-dir", "/Users/a b/Lanflow", "--uid", "501"},
			service.InstallOptions{CoreBin: "/Apps/Lanflow.app/mihomo", CoreSHA256: sum, DataDir: "/Users/a b/Lanflow", UID: "501", Version: version}, true},
		{"allow downgrade, single dashes",
			[]string{"-core", "c", "-core-sha256", sum, "-data-dir", "d", "-uid", "S-1-5-21-1", "-allow-downgrade"},
			service.InstallOptions{CoreBin: "c", CoreSHA256: sum, DataDir: "d", UID: "S-1-5-21-1", AllowDowngrade: true, Version: version}, true},
		// An app from before --core-sha256: parsed, with an empty digest
		// that service.Install refuses.
		{"no digest",
			[]string{"--core", "c", "--data-dir", "d", "--uid", "501"},
			service.InstallOptions{CoreBin: "c", DataDir: "d", UID: "501", Version: version}, true},
		{"unknown flag", []string{"--core", "c", "--run-as", "root"}, service.InstallOptions{}, false},
		{"flag without a value", []string{"--core"}, service.InstallOptions{}, false},
		{"stray argument", []string{"--core", "c", "extra"}, service.InstallOptions{}, false},
	} {
		var errOut bytes.Buffer
		got, err := parseInstall(tc.args, &errOut)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%s: %+v, %v", tc.name, got, err)
		}
		if !tc.ok && errOut.Len() == 0 {
			t.Errorf("%s: nothing was said about the error", tc.name)
		}
	}
}

// Under launchd stderr already is the helper log (a regular file): the
// logger writes there once and opens nothing else. The other branch
// (stderr is not a file) creates the system log directory, which a test
// must not do.
func TestNewLoggerStderrIsTheLog(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "helper.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old }()

	newLogger(service.LogPath).Printf("hello %d", 1)
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "hello 1"); n != 1 {
		t.Fatalf("the line was written %d times: %q", n, b)
	}
}
