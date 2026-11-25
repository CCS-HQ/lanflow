package tailscale

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Detect looks for Tailscale. own reports whether an interface is
// Lanflow's own TUN; nil means none. It runs commands only once Tailscale's
// interface is found, so the common case (no Tailscale) costs one
// interface listing.
func Detect(ctx context.Context, own func(iface string) bool) Info {
	if own == nil {
		own = func(string) bool { return false }
	}
	name, addrs := pickIface(interfaces(), own)
	if name == "" {
		return Info{}
	}
	info := Info{Iface: name, Addrs: addrs}
	info.Routes, info.Resolvers, info.MagicDNS, info.ExitNode = extras(ctx, name)
	if st, ok := cachedCLI(ctx, name); ok {
		info.Suffix = st.Suffix
		info.MagicDNS = info.MagicDNS || st.MagicDNS
		info.ExitNode = info.ExitNode || st.ExitNode
	}
	return info
}

func interfaces() []Iface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]Iface, 0, len(ifs))
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 {
			continue
		}
		it := Iface{Name: i.Name, Up: i.Flags&net.FlagUp != 0}
		as, _ := i.Addrs()
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					it.Addrs = append(it.Addrs, ip.Unmap())
				}
			}
		}
		out = append(out, it)
	}
	return out
}

// cliTTL is how long one answer of the CLI is reused for the same interface.
const cliTTL = time.Minute

var cli struct {
	mu    sync.Mutex
	iface string
	at    time.Time
	st    cliStatus
	ok    bool
}

// cachedCLI asks `tailscale status --json` at most once per cliTTL (and
// again when the interface changes, i.e. Tailscale restarted).
func cachedCLI(ctx context.Context, iface string) (cliStatus, bool) {
	cli.mu.Lock()
	defer cli.mu.Unlock()
	if cli.iface == iface && time.Since(cli.at) < cliTTL {
		return cli.st, cli.ok
	}
	st, ok := askCLI(ctx)
	if !ok && cli.iface == iface && cli.ok {
		// a slow or failed answer keeps the last good one for the same
		// interface: flapping between known and unknown would reload the
		// core each time
		st, ok = cli.st, true
	}
	cli.iface, cli.at, cli.st, cli.ok = iface, time.Now(), st, ok
	return st, ok
}

func askCLI(ctx context.Context) (cliStatus, bool) {
	bin := findCLI()
	if bin == "" {
		return cliStatus{}, false
	}
	for _, args := range [][]string{{"status", "--json", "--peers=false"}, {"status", "--json"}} {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		cmd := exec.CommandContext(c, bin, args...)
		hideWindow(cmd)
		out, err := cmd.Output()
		cancel()
		if err != nil {
			continue
		}
		if st, ok := parseStatus(out); ok {
			return st, true
		}
	}
	return cliStatus{}, false
}

// findCLI returns the first tailscale CLI found: on PATH, then where the
// installers put it.
func findCLI() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	for _, p := range cliPaths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
