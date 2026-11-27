// Package stage turns the runtime config the app hands over into the one
// the core in the packet tunnel runs: the app's config, with what only the
// tunnel knows (the TUN's file descriptor, the controller's socket) and
// what an iOS extension cannot afford or do (a TCP controller, a web UI,
// finding processes, geodata kept whole in memory) overridden.
//
// It is applied at every start and before every reload: the controller's
// PUT /configs re-applies the tun section of the file it loads, so a file
// without the descriptor would take the TUN down.
package stage

import (
	"errors"
	"fmt"
	"net/netip"

	"go.yaml.in/yaml/v3"
)

// Params is what the tunnel knows of itself.
type Params struct {
	FD         int    // the utun file descriptor NetworkExtension opened; 0 while the tunnel is not up yet
	Controller string // unix socket of the controller
}

// Network is what the tunnel's network settings must match.
type Network struct {
	Inet4 string `json:"inet4"`           // TUN address, as mihomo derives it from dns.fake-ip-range
	DNS   string `json:"dns"`             // a DNS server inside the TUN, which dns-hijack answers
	Inet6 string `json:"inet6,omitempty"` // TUN IPv6 address, when the config enables IPv6
	MTU   int    `json:"mtu"`
}

// MTU of the TUN, here and in the tunnel's network settings. A larger MTU
// grows every packet buffer of the stack, which the extension's memory
// limit cannot afford.
const MTU = 1500

// defaultFakeIPRange is mihomo's default dns.fake-ip-range, from which it
// takes the TUN's IPv4 address.
const defaultFakeIPRange = "198.18.0.1/16"

// defaultInet6 is mihomo's default tun.inet6-address.
const defaultInet6 = "fdfe:dcba:9876::1"

// Apply returns src with the tunnel's overrides, and the network the
// tunnel must set up for it.
func Apply(src []byte, p Params) ([]byte, Network, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, Network{}, fmt.Errorf("config is not valid YAML: %w", err)
	}
	if doc == nil {
		return nil, Network{}, errors.New("config is empty")
	}

	tun, _ := doc["tun"].(map[string]any)
	if tun == nil {
		tun = map[string]any{}
	}
	tun["enable"] = true
	// gVisor for UDP, the system stack for TCP: sing-box's choice on Apple
	// platforms, lighter than gVisor alone.
	tun["stack"] = "mixed"
	tun["file-descriptor"] = p.FD
	tun["mtu"] = MTU
	// NetworkExtension owns the interface, its routes and its DNS; the
	// extension's own sockets never go through the tunnel, so nothing needs
	// binding to the physical interface either.
	tun["auto-route"] = false
	tun["auto-redirect"] = false
	tun["auto-detect-interface"] = false
	tun["strict-route"] = false
	tun["dns-hijack"] = []any{"any:53", "tcp://any:53"}
	delete(tun, "device")
	doc["tun"] = tun

	// Only the unix socket the app reaches through the App Group.
	doc["external-controller"] = ""
	doc["external-controller-tls"] = ""
	doc["external-controller-pipe"] = ""
	doc["external-controller-unix"] = p.Controller
	delete(doc, "external-ui")
	delete(doc, "external-ui-url")
	delete(doc, "external-ui-name")
	// An extension may not look up other apps' processes, and the
	// interface binding is NetworkExtension's business.
	doc["find-process-mode"] = "off"
	delete(doc, "interface-name")
	delete(doc, "routing-mark")
	// GeoSite and GeoIP .dat files kept whole in memory overrun the
	// extension's 50 MB; the app updates geodata, not the tunnel.
	doc["geodata-loader"] = "memconservative"
	doc["geo-auto-update"] = false

	net, err := network(doc)
	if err != nil {
		return nil, Network{}, err
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, Network{}, err
	}
	return out, net, nil
}

// network derives the TUN's addresses as mihomo does (config.parseTun):
// IPv4 is the first address of dns.fake-ip-range, as a /30.
func network(doc map[string]any) (Network, error) {
	rng := defaultFakeIPRange
	if dns, ok := doc["dns"].(map[string]any); ok {
		if s, ok := dns["fake-ip-range"].(string); ok && s != "" {
			rng = s
		}
	}
	pfx, err := netip.ParsePrefix(rng)
	if err != nil || !pfx.Addr().Is4() {
		return Network{}, fmt.Errorf("dns.fake-ip-range %q is not an IPv4 range", rng)
	}
	addr := pfx.Addr()
	n := Network{Inet4: addr.String(), DNS: addr.Next().String(), MTU: MTU}
	if v6, _ := doc["ipv6"].(bool); v6 {
		n.Inet6 = defaultInet6
		if tun, ok := doc["tun"].(map[string]any); ok {
			if list, ok := tun["inet6-address"].([]any); ok && len(list) > 0 {
				if s, ok := list[0].(string); ok {
					if p, err := netip.ParsePrefix(s); err == nil {
						n.Inet6 = p.Addr().String()
					}
				}
			}
		}
	}
	return n, nil
}
