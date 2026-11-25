//go:build darwin

package tailscale

import (
	"context"
	"os/exec"
	"sync"
	"time"

	"lanflow/internal/platform/atrust"
)

var cliPaths = []string{
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	"/opt/homebrew/bin/tailscale",
	"/usr/local/bin/tailscale",
}

func hideWindow(*exec.Cmd) {}

func run(ctx context.Context, name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err == nil
}

// last are the extras of the last detection whose commands all answered,
// reused for the same interface when one of them fails (a timeout under
// load), so a hiccup does not change the configuration twice.
var last struct {
	mu     sync.Mutex
	iface  string
	routes []string
	rs     []Resolver
	magic  bool
	exit   bool
}

func extras(ctx context.Context, iface string) (routes []string, rs []Resolver, magic, exit bool) {
	table, ok1 := run(ctx, "/usr/sbin/netstat", "-rn", "-f", "inet")
	dns, ok2 := run(ctx, "/usr/sbin/scutil", "--dns")
	last.mu.Lock()
	defer last.mu.Unlock()
	if !(ok1 && ok2) && last.iface == iface {
		return last.routes, last.rs, last.magic, last.exit
	}
	routes, exit = parseRoutes(table, iface)
	rs, magic = resolvers(atrust.ParseResolvers(dns), iface)
	if ok1 && ok2 {
		last.iface, last.routes, last.rs, last.magic, last.exit = iface, routes, rs, magic, exit
	}
	return routes, rs, magic, exit
}
