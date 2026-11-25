// Package tailscale detects a running Tailscale client so Lanflow can keep
// the tailnet out of its TUN, fake-ip DNS and remote nodes.
//
// What it looks at (all read-only):
//   - the network interfaces: Tailscale's tunnel carries an address of the
//     tailnet ranges (100.64.0.0/10 and fd7a:115c:a1e0::/48), which no
//     physical adapter has under a tunnel name;
//   - on macOS, the routes through that interface (`netstat -rn -f inet`):
//     subnet routes, and whether it holds the default route (an exit
//     node), and the resolvers bound to it (`scutil --dns`): MagicDNS and
//     split DNS domains with their servers;
//   - when the tailscale CLI is installed, `tailscale status --json` for
//     the tailnet's MagicDNS suffix and whether an exit node is in use
//     (asked at most once a minute).
package tailscale

import (
	"encoding/json"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"lanflow/internal/platform/atrust"
)

// The tailnet's fixed ranges and MagicDNS resolver.
var (
	CGNAT     = netip.MustParsePrefix("100.64.0.0/10")
	ULA       = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	Quad100   = netip.MustParseAddr("100.100.100.100")
	quad100v6 = netip.MustParseAddr("fd7a:115c:a1e0::53")
)

// Resolver is a domain Tailscale's DNS serves and the servers for it.
type Resolver struct {
	Domain  string
	Servers []string // IPv4 addresses
}

// Info is one detection result. The zero value means "not running".
type Info struct {
	Iface     string     // Tailscale's interface ("" = not running)
	Addrs     []string   // this machine's tailnet addresses
	Routes    []string   // ranges routed into Iface beyond the tailnet ranges (subnet routes, macOS)
	Resolvers []Resolver // domains served by resolvers on Iface (macOS)
	Suffix    string     // the tailnet's MagicDNS suffix, "" when unknown
	MagicDNS  bool       // a MagicDNS resolver (100.100.100.100) is in use
	ExitNode  bool       // all traffic goes through an exit node
}

// Running reports whether Tailscale's interface was found.
func (i Info) Running() bool { return i.Iface != "" }

// Key is a stable fingerprint of what the configuration depends on (the
// addresses are left out: they do not change the configuration).
func (i Info) Key() string {
	if !i.Running() {
		return ""
	}
	var rs []string
	for _, r := range i.Resolvers {
		rs = append(rs, r.Domain+"="+strings.Join(r.Servers, "+"))
	}
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return strings.Join([]string{i.Iface, strings.Join(i.Routes, ","), strings.Join(rs, ","), i.Suffix, b(i.MagicDNS), b(i.ExitNode)}, "|")
}

// Iface is a network interface as the detector sees it.
type Iface struct {
	Name  string
	Up    bool
	Addrs []netip.Addr
}

// tunnelName reports whether an interface name is one Tailscale's tunnel
// may have: utunN (macOS), "Tailscale" (Windows), tailscale0 (Linux).
func tunnelName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "utun") || strings.HasPrefix(n, "tun") || strings.Contains(n, "tailscale")
}

// pickIface finds Tailscale's interface: one that is up, not Lanflow's
// own, and holds a tailnet IPv6 address, or a 100.64.0.0/10 address under
// a tunnel name (a physical adapter behind carrier-grade NAT has such an
// address too, under its own name). Ties go to the lowest name.
func pickIface(ifs []Iface, own func(string) bool) (string, []string) {
	type cand struct {
		name  string
		addrs []string
	}
	var cs []cand
	for _, i := range ifs {
		if !i.Up || own(i.Name) {
			continue
		}
		var v4, v6 bool
		var addrs []string
		for _, a := range i.Addrs {
			a = a.Unmap()
			switch {
			case a.Is4() && CGNAT.Contains(a) && a != Quad100:
				v4 = true
				addrs = append(addrs, a.String())
			case a.Is6() && ULA.Contains(a):
				v6 = true
				addrs = append(addrs, a.String())
			}
		}
		if v6 || (v4 && tunnelName(i.Name)) {
			sort.Strings(addrs)
			cs = append(cs, cand{i.Name, addrs})
		}
	}
	if len(cs) == 0 {
		return "", nil
	}
	sort.Slice(cs, func(a, b int) bool { return cs[a].name < cs[b].name })
	return cs[0].name, cs[0].addrs
}

// parseRoutes reads `netstat -rn -f inet` for the routes through iface:
// the ranges beyond the tailnet's (subnet routes) and whether it holds the
// default route or a split of it (an exit node). Interface-scoped routes
// (flag I) only serve sockets bound to iface and are left out.
func parseRoutes(table, iface string) (routes []string, exit bool) {
	var ps []netip.Prefix
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != iface || strings.Contains(f[2], "I") {
			continue
		}
		if atrust.IsSplitDefault(f[0]) {
			exit = true
			continue
		}
		p, ok := atrust.NetstatCIDR(f[0], f[2])
		if !ok || p.Bits() == 0 {
			continue
		}
		a := p.Addr()
		if a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			continue
		}
		if CGNAT.Contains(a) && p.Bits() >= CGNAT.Bits() { // the tailnet itself: covered as a whole
			continue
		}
		ps = append(ps, p)
	}
	for _, p := range atrust.Collapse(ps) {
		routes = append(routes, p.String())
	}
	return routes, exit
}

// isQuad100 reports whether a resolver address is Tailscale's own.
func isQuad100(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && (a.Unmap() == Quad100 || a == quad100v6)
}

// resolvers picks the `scutil --dns` resolvers that are Tailscale's: bound
// to iface, or pointing at 100.100.100.100. It returns the domains they
// serve (merged per domain, IPv4 servers only) and whether MagicDNS is in
// use. A default resolver (no domain) on 100.100.100.100 means Tailscale
// answers every lookup (its DNS overrides the local one).
func resolvers(rs []atrust.Resolver, iface string) ([]Resolver, bool) {
	byDomain := map[string][]string{}
	magic := false
	for _, r := range rs {
		quad := slices.ContainsFunc(r.Servers, isQuad100)
		if r.Iface != iface && !quad {
			continue
		}
		if quad {
			magic = true
		}
		d := strings.TrimSuffix(strings.ToLower(r.Domain), ".")
		if d == "" || d == "local" || strings.HasSuffix(d, ".arpa") {
			continue
		}
		for _, s := range r.Servers {
			a, err := netip.ParseAddr(s)
			if err != nil || !a.Unmap().Is4() || a.IsLoopback() {
				continue
			}
			if v := a.Unmap().String(); !slices.Contains(byDomain[d], v) {
				byDomain[d] = append(byDomain[d], v)
			}
		}
		if _, ok := byDomain[d]; !ok {
			byDomain[d] = nil
		}
	}
	out := make([]Resolver, 0, len(byDomain))
	for d, ss := range byDomain {
		if len(ss) == 0 {
			ss = []string{Quad100.String()}
		}
		out = append(out, Resolver{Domain: d, Servers: ss})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, magic
}

// cliStatus is what is read from `tailscale status --json`.
type cliStatus struct {
	Running  bool
	Suffix   string
	MagicDNS bool
	ExitNode bool
}

// parseStatus reads the JSON of `tailscale status --json`.
func parseStatus(data []byte) (cliStatus, bool) {
	var v struct {
		BackendState   string
		MagicDNSSuffix string
		CurrentTailnet *struct {
			MagicDNSSuffix  string
			MagicDNSEnabled bool
		}
		ExitNodeStatus *struct {
			ID     string
			Online bool
		}
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return cliStatus{}, false
	}
	st := cliStatus{Running: v.BackendState == "Running", Suffix: v.MagicDNSSuffix}
	if v.CurrentTailnet != nil {
		if v.CurrentTailnet.MagicDNSSuffix != "" {
			st.Suffix = v.CurrentTailnet.MagicDNSSuffix
		}
		st.MagicDNS = v.CurrentTailnet.MagicDNSEnabled
	}
	st.Suffix = strings.TrimSuffix(strings.ToLower(st.Suffix), ".")
	st.ExitNode = v.ExitNodeStatus != nil && v.ExitNodeStatus.ID != ""
	return st, true
}
