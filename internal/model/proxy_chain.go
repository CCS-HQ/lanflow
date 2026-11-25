package model

// ProxyChain is a user-defined chain of proxies (docs/chain-proxy.md):
// traffic goes through Hops in order, entry first, exit (落地) last.
// Hops are referenced by name: the entry may be a node or a proxy group,
// the others must be nodes of the configuration's `proxies`. Composition
// clones the second and later hops with dialer-proxy pointing at the
// previous hop and exposes the chain as a select group named Name.
type ProxyChain struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`    // display name, also the generated group's name
	Hops    []string `json:"hops"`    // 2–5 names, entry → … → exit
	Enabled bool     `json:"enabled"` // false: the chain is not generated (a REJECT placeholder stands in when referenced)
}

// Chain limits.
const (
	ProxyChainMinHops = 2
	ProxyChainMaxHops = 5
)

// CloneProxyChains returns a deep copy of chains.
func CloneProxyChains(chains []ProxyChain) []ProxyChain {
	if chains == nil {
		return nil
	}
	out := make([]ProxyChain, len(chains))
	for i, c := range chains {
		c.Hops = append([]string(nil), c.Hops...)
		out[i] = c
	}
	return out
}
