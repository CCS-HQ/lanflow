//go:build windows

package tailscale

import (
	"context"
	"os/exec"
	"syscall"
)

var cliPaths = []string{`C:\Program Files\Tailscale\tailscale.exe`, `C:\Program Files (x86)\Tailscale\tailscale.exe`}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// extras: Windows only reports the interface; the CLI adds the MagicDNS
// suffix and the exit node.
func extras(context.Context, string) ([]string, []Resolver, bool, bool) {
	return nil, nil, false, false
}
