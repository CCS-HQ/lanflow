package tunnel

import (
	"bytes"
	"fmt"
	"net/netip"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Defaults of Mihomo's TUN, made explicit in a config served on a
// descriptor: the VPN's interface must carry the same addresses.
const (
	defaultTUNAddress4 = "198.18.0.1/30"
	defaultTUNAddress6 = "fdfe:dcba:9876::1/126"
	defaultTUNMTU      = 9000
)

// FDInterface is the interface a system VPN must set up for a config that
// fdConfig made: Mihomo's TUN addresses, its MTU, a DNS server inside the
// TUN's subnet (which its dns-hijack answers) and the prefixes the config
// routes outside the TUN.
type FDInterface struct {
	MTU      int
	Address4 string
	Address6 string // "" when the config turns IPv6 off
	DNS      []string
	Exclude  []string
}

// fdConfig rewrites a runtime config for a TUN that a system VPN made and
// hands to Mihomo as the open descriptor fd (tun.file-descriptor), as on
// Android: the TUN is on, routing and the choice of interface are the
// VPN's (auto-route, auto-detect-interface and strict-route off, no device
// or interface lists), and the stack is gVisor's, the only one that works
// when the app's own connections are left out of the VPN (the system stack
// answers through the kernel, which routes the app around the VPN). The
// rest of the config is kept as it is.
func fdConfig(src []byte, fd int) ([]byte, FDInterface, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, FDInterface{}, fmt.Errorf("parse config: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, FDInterface{}, fmt.Errorf("config is not a mapping")
	}
	root := doc.Content[0]
	tun := child(root, "tun")
	if tun == nil || tun.Kind != yaml.MappingNode {
		tun = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		set(root, "tun", tun)
	}

	iface := FDInterface{MTU: defaultTUNMTU, Address4: defaultTUNAddress4, Address6: defaultTUNAddress6}
	if s := firstString(child(tun, "inet4-address")); s != "" {
		iface.Address4 = s
	}
	if s := firstString(child(tun, "inet6-address")); s != "" {
		iface.Address6 = s
	}
	if n := child(tun, "mtu"); n != nil {
		if m, err := strconv.Atoi(n.Value); err == nil && m >= 1280 && m <= 65535 {
			iface.MTU = m
		}
	}
	p4, err := netip.ParsePrefix(iface.Address4)
	if err != nil || !p4.Addr().Is4() {
		return nil, FDInterface{}, fmt.Errorf("tun.inet4-address %q is not an IPv4 prefix", iface.Address4)
	}
	if p6, err := netip.ParsePrefix(iface.Address6); err != nil || !p6.Addr().Is6() {
		return nil, FDInterface{}, fmt.Errorf("tun.inet6-address %q is not an IPv6 prefix", iface.Address6)
	}
	if v := child(root, "ipv6"); v != nil && v.Value == "false" {
		iface.Address6 = ""
	}
	// The DNS server apps are given: the address after the TUN's own, which
	// the VPN routes into the TUN, where dns-hijack answers it.
	dns := p4.Addr().Next()
	if !p4.Contains(dns) {
		return nil, FDInterface{}, fmt.Errorf("tun.inet4-address %q leaves no room for a DNS address", iface.Address4)
	}
	iface.DNS = []string{dns.String()}
	if n := child(tun, "route-exclude-address"); n != nil && n.Kind == yaml.SequenceNode {
		for _, x := range n.Content {
			if p, err := netip.ParsePrefix(x.Value); err == nil {
				iface.Exclude = append(iface.Exclude, p.Masked().String())
			}
		}
	}

	set(tun, "enable", scalar("!!bool", "true"))
	set(tun, "file-descriptor", scalar("!!int", strconv.Itoa(fd)))
	set(tun, "stack", scalar("!!str", "gvisor"))
	set(tun, "mtu", scalar("!!int", strconv.Itoa(iface.MTU)))
	set(tun, "inet4-address", seq(iface.Address4))
	set(tun, "inet6-address", seq(defaultIfEmpty(iface.Address6, defaultTUNAddress6)))
	set(tun, "auto-route", scalar("!!bool", "false"))
	set(tun, "auto-detect-interface", scalar("!!bool", "false"))
	set(tun, "strict-route", scalar("!!bool", "false"))
	for _, k := range []string{"device", "include-interface", "exclude-interface", "route-address",
		"route-exclude-address", "include-uid", "include-uid-range", "exclude-uid", "exclude-uid-range",
		"include-android-user", "include-package", "exclude-package", "iproute2-table-index",
		"iproute2-rule-index", "auto-redirect", "auto-redirect-input-mark", "auto-redirect-output-mark"} {
		del(tun, k)
	}
	// Interfaces are the system's to choose on a phone, not the config's.
	del(root, "interface-name")
	// The VPN gives apps a DNS server that only the TUN's dns-hijack
	// answers: without Mihomo's DNS, names do not resolve. profile.compose
	// adds this DNS only when TUN mode is on, which on a phone the VPN is
	// whatever the setting says.
	if d := child(root, "dns"); d == nil || d.Kind != yaml.MappingNode || child(d, "enable") == nil || child(d, "enable").Value != "true" {
		set(root, "dns", defaultDNS(iface.Address6 != ""))
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, FDInterface{}, err
	}
	if err := enc.Close(); err != nil {
		return nil, FDInterface{}, err
	}
	return buf.Bytes(), iface, nil
}

// defaultNameservers are profile's (compose.go), for the DNS of a config
// that has none.
var defaultNameservers = []string{"https://doh.pub/dns-query", "https://dns.alidns.com/dns-query"}

// defaultDNS is the DNS profile.compose gives a TUN config without one.
func defaultDNS(ipv6 bool) *yaml.Node {
	ns := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, s := range defaultNameservers {
		ns.Content = append(ns.Content, scalar("!!str", s))
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		scalar("!!str", "enable"), scalar("!!bool", "true"),
		scalar("!!str", "ipv6"), scalar("!!bool", strconv.FormatBool(ipv6)),
		scalar("!!str", "enhanced-mode"), scalar("!!str", "fake-ip"),
		scalar("!!str", "fake-ip-range"), scalar("!!str", "198.18.0.1/16"),
		scalar("!!str", "nameserver"), ns,
	}}
}

func defaultIfEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			n := m.Content[i+1]
			for n.Kind == yaml.AliasNode && n.Alias != nil {
				n = n.Alias
			}
			return n
		}
	}
	return nil
}

func set(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, scalar("!!str", key), v)
}

func del(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func scalar(tag, v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: v} }

func seq(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{scalar("!!str", v)}}
}

// firstString is a scalar's value, or a sequence's first.
func firstString(n *yaml.Node) string {
	switch {
	case n == nil:
		return ""
	case n.Kind == yaml.ScalarNode:
		return n.Value
	case n.Kind == yaml.SequenceNode && len(n.Content) > 0:
		return n.Content[0].Value
	}
	return ""
}
