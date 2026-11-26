//go:build windows

package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"lanflow/internal/split"
)

func isElevated() bool { return split.IsElevated() }

func readRunAsAdmin(dataDir string) bool {
	data, err := os.ReadFile(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		RunAsAdmin bool `json:"RunAsAdmin"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return cfg.RunAsAdmin
}

// maybeElevate relaunches Lanflow elevated on Windows when Settings.RunAsAdmin
// is enabled and the current process is not yet elevated.
func maybeElevate(args split.Args, dataDir string) bool {
	if args.NoElevate || isElevated() || os.Getenv("LANFLOW_FAKE") == "1" || os.Getenv("LANFLOW_NO_ELEVATE") == "1" {
		return false
	}
	if !readRunAsAdmin(dataDir) {
		return false
	}
	elevArgs := append(append([]string(nil), os.Args[1:]...), split.NoElevateFlag)
	if err := split.RunElevated(executable(), elevArgs...); err != nil {
		log.Printf("lanflow: elevate at startup: %v", err)
		return false
	}
	return true
}
