package atrust

import "net/netip"

// Helpers shared with the other split-tunnel detector (platform/tailscale),
// which reads the same macOS tables.

// Resolver is one block of `scutil --dns` (before the scoped section).
type Resolver struct {
	Domain  string   // match domain, lower case without the trailing dot ("" = a default resolver)
	Servers []string // nameserver[n]
	Iface   string   // the interface of if_index, "" when absent
}

// ParseResolvers reads the resolver blocks of `scutil --dns`.
func ParseResolvers(out string) []Resolver {
	rs := parseResolvers(out)
	res := make([]Resolver, len(rs))
	for i, r := range rs {
		res[i] = Resolver{Domain: r.domain, Servers: r.servers, Iface: r.iface}
	}
	return res
}

// NetstatCIDR turns a macOS `netstat -rn -f inet` destination ("10.1",
// "172.16/12", "2.0.0.5" with flag H) into a canonical CIDR.
func NetstatCIDR(dst, flags string) (netip.Prefix, bool) { return netstatCIDR(dst, flags) }

// IsSplitDefault reports whether a netstat destination is the default
// route or one of the halves TUN clients take it over with.
func IsSplitDefault(dst string) bool { return splitDefaults[dst] }

// Collapse sorts prefixes and drops those contained in another.
func Collapse(ps []netip.Prefix) []netip.Prefix { return collapse(ps) }
