package sysproxy

// Windows registry value formatting and parsing. Pure functions without a
// build tag so they are unit tested on every host; sysproxy_windows.go
// does the registry and WinINet calls.

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// formatProxyServer renders the WinINet ProxyServer value as bare "host:port"
// because Mihomo's mixed-port serves HTTP/HTTPS/SOCKS5 on a single port,
// whereas "socks=" makes WinINet treat it as SOCKS4 and confuses Settings UI.
func formatProxyServer(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// parseProxyServer extracts a host and port from a ProxyServer value,
// which is either a bare "host:port" or "proto=host:port;..." entries.
// Protocol preference: http, https, socks, then anything else.
func parseProxyServer(v string) (host string, port int) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", 0
	}
	byProto := map[string]string{}
	var bare string
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if proto, hp, ok := strings.Cut(part, "="); ok {
			byProto[strings.ToLower(strings.TrimSpace(proto))] = strings.TrimSpace(hp)
		} else if bare == "" {
			bare = part
		}
	}
	candidates := []string{byProto["http"], byProto["https"], byProto["socks"], bare, byProto["ftp"]}
	for _, hp := range candidates {
		if hp == "" {
			continue
		}
		h, p, err := net.SplitHostPort(hp)
		if err != nil {
			// A value without a port ("proxy.corp") is still a host.
			return hp, 0
		}
		port, _ = strconv.Atoi(p)
		return h, port
	}
	return "", 0
}

// formatProxyOverride expands IPv4 CIDRs into WinINet-compatible wildcard
// patterns, joins the bypass list with ";" and guarantees the WinINet
// "<local>" token (bypass for plain host names) is present.
func formatProxyOverride(bypass []string) string {
	seen := map[string]struct{}{}
	var parts []string
	hasLocal := false
	for _, b := range bypass {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		for _, item := range expandWindowsBypass(b) {
			key := strings.ToLower(item)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if key == "<local>" {
				hasLocal = true
			}
			parts = append(parts, item)
		}
	}
	if !hasLocal {
		parts = append(parts, "<local>")
	}
	return strings.Join(parts, ";")
}

// expandWindowsBypass converts IPv4 CIDR entries into WinINet wildcard
// patterns since Windows WinINet ProxyOverride does not accept CIDR notation.
func expandWindowsBypass(entry string) []string {
	p, err := netip.ParsePrefix(entry)
	if err != nil || !p.Addr().Is4() {
		return []string{entry}
	}
	p = p.Masked()
	a := p.Addr().As4()
	bits := p.Bits()
	switch {
	case bits == 32:
		return []string{p.Addr().String()}
	case bits == 24:
		return []string{fmt.Sprintf("%d.%d.%d.*", a[0], a[1], a[2])}
	case bits == 16:
		return []string{fmt.Sprintf("%d.%d.*", a[0], a[1])}
	case bits == 8:
		return []string{fmt.Sprintf("%d.*", a[0])}
	case bits >= 20 && bits < 24:
		n := 1 << (24 - bits)
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("%d.%d.%d.*", a[0], a[1], int(a[2])+i))
		}
		return out
	case bits >= 12 && bits < 16:
		n := 1 << (16 - bits)
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("%d.%d.*", a[0], int(a[1])+i))
		}
		return out
	case bits >= 4 && bits < 8:
		n := 1 << (8 - bits)
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("%d.*", int(a[0])+i))
		}
		return out
	default:
		return []string{entry}
	}
}

// parseProxyOverride splits a ProxyOverride value into its entries.
func parseProxyOverride(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ";") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
