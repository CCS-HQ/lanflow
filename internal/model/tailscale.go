package model

// TailscaleConfig is the user's Tailscale 兼容 configuration, applied on
// top of what is detected automatically (Settings.Tailscale). Detection
// already covers the tailnet ranges (100.64.0.0/10, fd7a:115c:a1e0::/48),
// the subnet routes Tailscale installs (macOS), the tailnet's MagicDNS
// suffix and resolvers; these lists add what detection cannot see, such as
// split DNS domains on Windows or subnet routes of another platform.
// Entries are normalised when the settings are stored (invalid ones are
// dropped); agents patching them get an error for an invalid entry.
type TailscaleConfig struct {
	// Domains are extra domain suffixes served by MagicDNS / split DNS:
	// sent to Tailscale's interface, never given a fake IP, not sniffed,
	// resolved by DNS (or the detected MagicDNS resolver).
	Domains []string `json:",omitempty"`
	// Routes are extra ranges reachable through Tailscale (subnet routes):
	// kept out of Lanflow's TUN and sent to Tailscale's interface.
	Routes []string `json:",omitempty"`
	// DNS are the resolvers for Domains (IP addresses); empty = the
	// detected MagicDNS resolver (100.100.100.100).
	DNS []string `json:",omitempty"`
}

// Empty reports whether nothing is configured.
func (c TailscaleConfig) Empty() bool {
	return len(c.Domains) == 0 && len(c.Routes) == 0 && len(c.DNS) == 0
}

// Clone returns a deep copy.
func (c TailscaleConfig) Clone() TailscaleConfig {
	cp := func(s []string) []string {
		if s == nil {
			return nil
		}
		return append([]string(nil), s...)
	}
	return TailscaleConfig{Domains: cp(c.Domains), Routes: cp(c.Routes), DNS: cp(c.DNS)}
}

// TailscaleReport is API.TailscaleReport.
type TailscaleReport struct {
	Compat bool            // Settings.TailscaleCompat
	Config TailscaleConfig // Settings.Tailscale
	State  TailscaleState  // detection and what the running config does
	Notes  []string        // what needs attention, in the UI language
}

// TailscaleState is a running Tailscale client found beside Lanflow and
// what the 兼容 mode does about it (Status.Tailscale). Zero = not running
// (or the setting is off).
type TailscaleState struct {
	Running   bool
	Interface string   // Tailscale's tunnel interface (utunN on macOS, "Tailscale" on Windows)
	Addresses []string // this machine's tailnet addresses
	Suffix    string   // the tailnet's MagicDNS suffix (e.g. tail1234.ts.net), "" when unknown
	// ExitNode: Tailscale routes all traffic through an exit node. It then
	// owns the default route, and Lanflow's TUN cannot run beside it.
	ExitNode bool
	CIDRs    []string // ranges sent to Tailscale (kept out of the TUN unless they hold a DNS server)
	Domains  []string // domain suffixes sent to Tailscale and resolved by DNS
	DNS      []string // MagicDNS resolvers used for Domains
	// Outbound is the hidden outbound bound to Interface that the rules
	// for CIDRs and Domains use ("" when the layer is not applied).
	Outbound string
	// Applied: the Tailscale layer is in the running config.
	Applied bool
}
