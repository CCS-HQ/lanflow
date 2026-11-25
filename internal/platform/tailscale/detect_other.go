//go:build !darwin && !windows

package tailscale

import (
	"context"
	"os/exec"
)

var cliPaths = []string{"/usr/bin/tailscale", "/usr/local/bin/tailscale"}

func hideWindow(*exec.Cmd) {}

func extras(context.Context, string) ([]string, []Resolver, bool, bool) {
	return nil, nil, false, false
}
