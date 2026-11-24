package env

import "fmt"

// Terminal proxy commands: what a user pastes into a terminal to point
// it at the proxy, one variant per shell family (docs/design.md §7). The
// tray's 复制终端代理命令 submenu lists them all; the overview shows the
// one of the system's usual shell.

// ShellCommand is one variant of the terminal proxy command.
type ShellCommand struct {
	Label string // shown in a menu
	Cmd   string // copied to the clipboard
}

// ShellCommands returns the variants for port (7890 when not positive):
// bash/zsh, fish, PowerShell and cmd, in that order.
func ShellCommands(port int) []ShellCommand {
	if port <= 0 {
		port = 7890
	}
	h := fmt.Sprintf("http://127.0.0.1:%d", port)
	s := fmt.Sprintf("socks5://127.0.0.1:%d", port)
	return []ShellCommand{
		{"bash / zsh", fmt.Sprintf("export https_proxy=%s http_proxy=%s all_proxy=%s", h, h, s)},
		{"fish", fmt.Sprintf("set -gx https_proxy %s; set -gx http_proxy %s; set -gx all_proxy %s", h, h, s)},
		{"PowerShell", fmt.Sprintf(`$env:HTTPS_PROXY="%s"; $env:HTTP_PROXY="%s"; $env:ALL_PROXY="%s"`, h, h, s)},
		{"cmd", fmt.Sprintf(`set "https_proxy=%s" && set "http_proxy=%s" && set "all_proxy=%s"`, h, h, s)},
	}
}

// ShellCommandFor returns the variant for the usual shell of the system
// goos names (a runtime.GOOS value): PowerShell on Windows, bash/zsh on
// macOS, Linux and the rest.
func ShellCommandFor(port int, goos string) ShellCommand {
	cmds := ShellCommands(port)
	if goos == "windows" {
		return cmds[2]
	}
	return cmds[0]
}
