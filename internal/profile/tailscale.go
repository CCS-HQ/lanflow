package profile

import (
	"net/netip"
	"strings"

	"gopkg.in/yaml.v3"
)

// layerTailscale attributes the lines added for Tailscale running beside
// Lanflow.
const layerTailscale = "Tailscale 兼容"

// TailscaleOutbound is the hidden outbound that reaches the tailnet: a
// direct outbound bound to Tailscale's interface. Bound, because with the
// TUN on the core binds every other outbound to the physical interface,
// where the tailnet is unreachable; this one reaches it whatever the core
// binds to. It is kept out of the groups that collect proxies by
// themselves and out of the window's lists (app.hideInternalProxies).
const TailscaleOutbound = "LANFLOW-TAILSCALE"

// Tailscale describes Tailscale running beside Lanflow and how the
// tailnet is kept out of Lanflow's TUN, fake-ip DNS and remote nodes. The
// app works it out from detection and the user's configuration; the zero
// value (no Iface) adds nothing.
type Tailscale struct {
	Iface string // Tailscale's interface: TailscaleOutbound binds to it
	// ExcludeCIDRs never enter the TUN (route-exclude-address): the system
	// routes them to Tailscale. Rules send them to TailscaleOutbound when
	// they reach the core some other way (the system proxy, an app set to
	// use the proxy).
	ExcludeCIDRs []string
	// DirectCIDRs go to TailscaleOutbound but stay in the TUN: they hold a
	// DNS server the TUN's DNS hijack must keep seeing.
	DirectCIDRs []string
	// Domains are names that resolve into the tailnet (the MagicDNS suffix
	// and the user's list): rules send them to TailscaleOutbound.
	Domains []string
	// DNS are the domains resolved by Tailscale's DNS (all of Domains, and
	// split DNS domains that may well resolve to public addresses, so they
	// get no rule): no fake IP, not sniffed, and the nameserver-policy
	// entry, ahead of every other, sends them to Servers through
	// TailscaleOutbound.
	DNS []DomainDNS
}

// DomainDNS is a domain suffix and the servers that resolve it.
type DomainDNS struct {
	Domain  string
	Servers []string
}

func (ts Tailscale) empty() bool { return ts.Iface == "" }

// applyTailscale adds the Tailscale layer. It runs after the aTrust layer,
// so its rules come first.
func applyTailscale(c *composer, ts Tailscale, tunOn bool) {
	// an outbound or group of the same name from elsewhere never stays:
	// the rules below must reach this one
	removeNamed(deref(mapGet(c.root, "proxies")), TailscaleOutbound)
	removeNamed(deref(mapGet(c.root, "proxy-groups")), TailscaleOutbound)
	if ts.empty() {
		scrubTailscaleRefs(c)
		return
	}
	c.layer = layerTailscale

	ps := c.seq("proxies")
	ps.Content = append(ps.Content, c.mark(mapNode(
		"name", strNode(TailscaleOutbound),
		"type", strNode("direct"),
		"udp", boolNode(true),
		"interface-name", strNode(ts.Iface),
	)))
	excludeFromAutoGroups(c, "^"+TailscaleOutbound+"$")

	// rules: ahead of everything else
	var items []*yaml.Node
	seen := map[string]bool{}
	add := func(r string) {
		if !seen[r] {
			seen[r] = true
			items = append(items, c.mark(strNode(r)))
		}
	}
	for _, d := range ts.Domains {
		add("DOMAIN-SUFFIX," + d + "," + TailscaleOutbound)
	}
	for _, p := range prefixes6(append(append([]string(nil), ts.ExcludeCIDRs...), ts.DirectCIDRs...)) {
		kind := "IP-CIDR"
		if p.Addr().Is6() {
			kind = "IP-CIDR6"
		}
		add(kind + "," + p.String() + "," + TailscaleOutbound + ",no-resolve")
	}
	rs := c.seq("rules")
	kept := rs.Content[:0:0]
	for _, n := range rs.Content {
		if !seen[n.Value] {
			kept = append(kept, n)
		}
	}
	rs.Content = append(items, kept...)

	// DNS
	var names []string
	for _, e := range ts.DNS {
		names = append(names, e.Domain)
	}
	if d := deref(mapGet(c.root, "dns")); d != nil && d.Kind == yaml.MappingNode && len(ts.DNS) > 0 {
		appendUnique(c, d, "fake-ip-filter", prefixAll("+.", names))
		policy := deref(mapGet(d, "nameserver-policy"))
		if policy == nil || policy.Kind != yaml.MappingNode {
			policy = mapNode()
			mapSet(d, "nameserver-policy", policy)
		}
		// first: Mihomo takes the first policy that matches, and a broad
		// one such as geosite:geolocation-!cn would send these elsewhere
		for i := len(ts.DNS) - 1; i >= 0; i-- {
			e := ts.DNS[i]
			var servers []string
			for _, s := range e.Servers {
				servers = append(servers, s+"#"+TailscaleOutbound)
			}
			mapPrepend(policy, "+."+e.Domain, c.mark(strSeq(servers)))
		}
	}
	if len(names) > 0 {
		if sn := deref(mapGet(c.root, "sniffer")); sn != nil && sn.Kind == yaml.MappingNode {
			appendUnique(c, sn, "skip-domain", prefixAll("+.", names))
		}
	}

	if !tunOn || len(ts.ExcludeCIDRs) == 0 {
		return
	}
	tun := deref(mapGet(c.root, "tun"))
	if tun == nil || tun.Kind != yaml.MappingNode {
		return
	}
	var ex []string
	for _, p := range prefixes6(ts.ExcludeCIDRs) {
		ex = append(ex, p.String())
	}
	appendUnique(c, tun, "route-exclude-address", ex)
}

// scrubTailscaleRefs keeps a config that names TailscaleOutbound valid
// while Tailscale is not running (a custom rule or override written for
// it): such rules go DIRECT and groups drop it, where the core would
// otherwise refuse the whole config for an unknown proxy.
func scrubTailscaleRefs(c *composer) {
	if rs := deref(mapGet(c.root, "rules")); rs != nil && rs.Kind == yaml.SequenceNode {
		for _, n := range rs.Content {
			if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, ","+TailscaleOutbound) {
				n.Value = replaceTarget(n.Value)
			}
		}
	}
	if sr := deref(mapGet(c.root, "sub-rules")); sr != nil && sr.Kind == yaml.MappingNode {
		for i := 1; i < len(sr.Content); i += 2 {
			if seq := deref(sr.Content[i]); seq != nil && seq.Kind == yaml.SequenceNode {
				for _, n := range seq.Content {
					if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, ","+TailscaleOutbound) {
						n.Value = replaceTarget(n.Value)
					}
				}
			}
		}
	}
	if gs := deref(mapGet(c.root, "proxy-groups")); gs != nil && gs.Kind == yaml.SequenceNode {
		for _, it := range gs.Content {
			if ps := deref(mapGet(deref(it), "proxies")); ps != nil && ps.Kind == yaml.SequenceNode {
				kept := ps.Content[:0]
				for _, p := range ps.Content {
					if p.Value != TailscaleOutbound {
						kept = append(kept, p)
					}
				}
				ps.Content = kept
			}
		}
	}
}

// replaceTarget turns a rule's TailscaleOutbound target into DIRECT.
func replaceTarget(rule string) string {
	parts := strings.Split(rule, ",")
	for i, p := range parts {
		if i > 0 && strings.TrimSpace(p) == TailscaleOutbound {
			parts[i] = "DIRECT"
		}
	}
	return strings.Join(parts, ",")
}

// prefixes6 parses ranges of both families (prefixes only takes IPv4 bare
// addresses), canonical and deduplicated, in the order given.
func prefixes6(ss []string) []netip.Prefix {
	var out []netip.Prefix
	seen := map[netip.Prefix]bool{}
	for _, s := range ss {
		s = strings.TrimSpace(s)
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, err := netip.ParseAddr(s)
			if err != nil {
				continue
			}
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = p.Masked()
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// mapPrepend puts key: v first in the mapping m, replacing an entry of
// the same key wherever it was.
func mapPrepend(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			break
		}
	}
	m.Content = append([]*yaml.Node{strNode(key), v}, m.Content...)
}

// excludeFromAutoGroups keeps the outbounds matching the regex ex out of
// the groups that collect proxies by themselves (include-all,
// include-all-proxies, filter), appending to any exclude-filter already
// there (Mihomo separates regexes with `).
func excludeFromAutoGroups(c *composer, ex string) {
	gs := deref(mapGet(c.root, "proxy-groups"))
	if gs == nil || gs.Kind != yaml.SequenceNode {
		return
	}
	for _, it := range gs.Content {
		m := deref(it)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}
		if !truthy(mapGet(m, "include-all")) && !truthy(mapGet(m, "include-all-proxies")) && deref(mapGet(m, "filter")) == nil {
			continue
		}
		if old := deref(mapGet(m, "exclude-filter")); old != nil && old.Kind == yaml.ScalarNode && old.Value != "" {
			if !strings.Contains(old.Value, ex) {
				mapSet(m, "exclude-filter", strNode(old.Value+"`"+ex))
			}
		} else {
			mapSet(m, "exclude-filter", strNode(ex))
		}
	}
}
