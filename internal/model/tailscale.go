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

// TailscaleBuiltin is 内置 Tailscale (Settings.TailscaleBuiltin): the
// core joins a tailnet itself (Mihomo's tailscale outbound, built on
// tsnet), so the Tailscale app need not run. Only traffic that passes
// through Lanflow (TUN, system proxy, apps set to use the proxy) reaches
// the tailnet; the machine is not a tailnet node of its own, so other
// devices cannot reach its services, and Taildrop and Tailscale SSH are
// not available. It is machine-local (never synced) and gives way to the
// Tailscale app: while that runs, the built-in node is not started.
type TailscaleBuiltin struct {
	Enabled bool
	// Hostname is the device name in the tailnet; "" = "<this computer's
	// name>-lanflow".
	Hostname string `json:",omitempty"`
	// ControlURL is the coordination server (Headscale); "" = Tailscale's.
	ControlURL string `json:",omitempty"`
	// AcceptRoutes uses the subnet routes other nodes advertise. The
	// ranges themselves go in Settings.Tailscale.Routes, so that rules send
	// them to the node.
	AcceptRoutes bool `json:",omitempty"`
	// ExitNode is an exit node's tailnet IP or name, or "auto:any". Only
	// what custom rules send to the node (TailscaleOutbound) uses it.
	ExitNode string `json:",omitempty"`
	// StateGen names the node's state directory (tailscale, tailscale-N);
	// 退出登录 moves to the next one, which starts without a login.
	StateGen int `json:",omitempty"`
}

// Tailscale built-in node states (TailscaleBuiltinState.State), as the
// core's tailnet client reports them.
const (
	TSStateOff          = ""                   // not in the running config
	TSStateStarting     = "starting"           // configured, waiting for the core's client
	TSStateNeedsLogin   = "needs-login"        // LoginURL is set (or comes soon)
	TSStateMachineAuth  = "needs-machine-auth" // logged in, the tailnet admin must approve the device
	TSStateRunning      = "running"            // connected to the tailnet
	TSStateStopped      = "stopped"
	TSStatePausedForApp = "paused" // the Tailscale app runs: the built-in node is left out
)

// TailscaleBuiltinState is what the built-in node is doing
// (TailscaleState.Builtin). Zero = not configured.
type TailscaleBuiltinState struct {
	State     string   // TSState*
	LoginURL  string   // State == TSStateNeedsLogin: open it to add the device
	Hostname  string   // the device name asked for
	Addresses []string // the node's tailnet addresses, once known
	LoginName string   // the account logged in, once known
	Error     string   // the last problem the core's client reported
}

// TailscaleReport is API.TailscaleReport.
type TailscaleReport struct {
	Compat  bool             // Settings.TailscaleCompat
	Config  TailscaleConfig  // Settings.Tailscale
	Builtin TailscaleBuiltin // Settings.TailscaleBuiltin
	// AuthKey: an auth key for the built-in node is in the keychain.
	AuthKey bool
	State   TailscaleState // detection and what the running config does
	Notes   []string       // what needs attention, in the UI language
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
	// Builtin is the built-in node (Settings.TailscaleBuiltin).
	Builtin TailscaleBuiltinState
}
