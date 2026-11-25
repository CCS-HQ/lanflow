package profile

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

// 链式代理 layer (docs/chain-proxy.md §3.3). A chain [H1, …, Hk] leaves
// every original node alone: hops 2…k are cloned as LANFLOW-CHAIN-<id>-<i>
// with dialer-proxy pointing at the previous hop (the entry H1, a node or
// a group, is only referenced), and the chain is exposed as one select
// group named after it whose only member is the last clone. Rules, upper
// groups and store-selected refer to that group, so the clone names never
// change with a rename. All chain groups go into the select group
// 「链式代理」, which is appended to the first visible select group the
// way the manual-node group is.
//
// A chain that is disabled or does not validate is not generated; when
// something refers to its name a select group with REJECT only stands in,
// so the configuration still loads and the traffic is refused rather than
// silently sent direct.
//
// Mihomo nodes cannot be hidden, so composition keeps the clones out of
// the groups that collect nodes by themselves (include-all*, GLOBAL and
// LANFLOW-PROBE) afterwards: excludeChainClones.

const (
	layerChains = "链式代理"

	// ChainClonePrefix starts the name of every node a chain generates.
	ChainClonePrefix = "LANFLOW-CHAIN-"
	// ChainGroupName is the select group holding every chain group.
	ChainGroupName = "链式代理"
	// ChainGroupAltName is used when the subscription already has a
	// ChainGroupName group that is not a select group.
	ChainGroupAltName = "链式代理（澜流）"

	// reservedPrefix starts every name Lanflow generates (LANFLOW-PROBE,
	// LANFLOW-MITM-*, LANFLOW-CHAIN-*); no hop or chain name may use it.
	reservedPrefix = "LANFLOW-"

	// chainCloneFilter keeps the clones out of include-all* groups: Mihomo
	// matches each `filter` regex (regexp2) against the configuration's
	// node names, and the negative lookahead refuses ours.
	chainCloneFilter = "^(?!" + ChainClonePrefix + ")"

	// ChainWarningPrefix starts every composition warning about chains.
	ChainWarningPrefix = layerChains + "："
)

// ChainCloneName is the name of the clone of hop i (1-based, i >= 2) of
// the chain with the given ID.
func ChainCloneName(id string, i int) string {
	return fmt.Sprintf("%s%s-%d", ChainClonePrefix, id, i)
}

// IsChainClone reports whether name is a node a chain generated.
func IsChainClone(name string) bool { return strings.HasPrefix(name, ChainClonePrefix) }

// ChainHopOf returns, for a clone name, the chain it belongs to and the
// position (1-based) and name of the original node it clones.
func ChainHopOf(name string, chains []model.ProxyChain) (ch model.ProxyChain, pos int, node string, ok bool) {
	rest, found := strings.CutPrefix(name, ChainClonePrefix)
	if !found {
		return ch, 0, "", false
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 {
		return ch, 0, "", false
	}
	id := rest[:i]
	n, err := strconv.Atoi(rest[i+1:])
	if err != nil {
		return ch, 0, "", false
	}
	for _, c := range chains {
		if c.ID == id && n >= 2 && n <= len(c.Hops) {
			return c, n, c.Hops[n-1], true
		}
	}
	return ch, 0, "", false
}

// DisplayName is how the interface names a proxy: a chain clone by the
// node it clones, anything else as it is.
func DisplayName(name string, chains []model.ProxyChain) string {
	if _, _, node, ok := ChainHopOf(name, chains); ok {
		return node
	}
	return name
}

// IsReservedName reports whether name uses the prefix of the names
// Lanflow generates.
func IsReservedName(name string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(name)), reservedPrefix)
}

// NormalizeProxyChain trims a chain's name and hops and checks what does
// not depend on the configuration: the name, 2–5 hops, no hop twice and
// no reserved names. The service validates with it before storing;
// composition checks the rest (validateChain).
func NormalizeProxyChain(ch model.ProxyChain) (model.ProxyChain, error) {
	ch.Name = strings.TrimSpace(ch.Name)
	hops := make([]string, 0, len(ch.Hops))
	for _, h := range ch.Hops {
		hops = append(hops, strings.TrimSpace(h))
	}
	ch.Hops = hops
	if ch.Name == "" {
		return ch, chainNote("empty-name")
	}
	if IsReservedName(ch.Name) {
		return ch, chainNote("reserved-name", reservedPrefix)
	}
	if n := len(ch.Hops); n < model.ProxyChainMinHops || n > model.ProxyChainMaxHops {
		return ch, chainNote("hop-count", model.ProxyChainMinHops, model.ProxyChainMaxHops, n)
	}
	seen := map[string]bool{}
	for i, h := range ch.Hops {
		if h == "" {
			return ch, chainNote("hop-empty", i+1)
		}
		if IsReservedName(h) {
			return ch, chainNote("hop-reserved", i+1, h)
		}
		if seen[h] {
			return ch, chainNote("hop-twice", h)
		}
		seen[h] = true
		if h == ch.Name {
			return ch, chainNote("hop-self")
		}
	}
	return ch, nil
}

// chainEnv is what validation needs to know of the configuration.
type chainEnv struct {
	proxies map[string]*yaml.Node // configuration-level nodes by name
	groups  map[string]*yaml.Node // proxy groups by name
	// firstSelect is the group the chain group is appended to ("" = none).
	firstSelect string
	container   string
	chainNames  map[string]bool
}

// groupMembers lists the explicit members of group g.
func groupMembers(g *yaml.Node) []string {
	var out []string
	if ps := deref(mapGet(g, "proxies")); ps != nil && ps.Kind == yaml.SequenceNode {
		for _, p := range ps.Content {
			if s := deref(p); s != nil && s.Kind == yaml.ScalarNode {
				out = append(out, s.Value)
			}
		}
	}
	return out
}

// entryLoop reports the reason when the entry group may contain, at any
// depth, a chain group or the chain container (directly or by being the
// group the container is appended to): the chain would then dial through
// itself. Nodes collected by include-all* or providers are never groups,
// and the clones are kept out of include-all* (excludeChainClones).
func (e *chainEnv) entryLoop(entry string) *ChainNote {
	seen := map[string]bool{}
	var visit func(name string) *ChainNote
	visit = func(name string) *ChainNote {
		if seen[name] {
			return nil
		}
		seen[name] = true
		switch {
		case e.chainNames[name]:
			return chainNoteP("loop-chain", entry, name)
		case name == e.container:
			return chainNoteP("loop-container", entry, name)
		case name == e.firstSelect && e.firstSelect != "":
			return chainNoteP("loop-first", entry, name, e.container)
		}
		g, ok := e.groups[name]
		if !ok {
			return nil
		}
		for _, m := range groupMembers(g) {
			if r := visit(m); r != nil {
				return r
			}
		}
		return nil
	}
	return visit(entry)
}

// validateChain checks ch against the configuration; "" = valid. warns
// are notes on a valid chain.
func (e *chainEnv) validateChain(ch model.ProxyChain) (reason *ChainNote, warns []ChainNote) {
	ch, err := NormalizeProxyChain(ch)
	var n ChainNote
	if errors.As(err, &n) {
		return &n, nil
	}
	entry := ch.Hops[0]
	_, entryNode := e.proxies[entry]
	_, entryGroup := e.groups[entry]
	switch {
	case entryNode:
	case entryGroup:
		if r := e.entryLoop(entry); r != nil {
			return r, nil
		}
	case e.chainNames[entry]:
		return chainNoteP("entry-chain", entry), nil
	default:
		return chainNoteP("entry-missing", entry), nil
	}
	for i, h := range ch.Hops[1:] {
		pos := i + 2
		n, ok := e.proxies[h]
		if !ok {
			if _, isGroup := e.groups[h]; isGroup || e.chainNames[h] {
				return chainNoteP("hop-group", pos, h), nil
			}
			return chainNoteP("hop-missing", pos, h), nil
		}
		if t := deref(mapGet(n, "type")); t != nil && strings.EqualFold(t.Value, "wireguard") {
			warns = append(warns, chainNote("wireguard", ch.Name, pos, h))
		}
		if d := deref(mapGet(n, "dialer-proxy")); d != nil && strings.TrimSpace(d.Value) != "" {
			warns = append(warns, chainNote("own-dialer", ch.Name, pos, h, d.Value))
		}
	}
	return nil, warns
}

// chainReferenced collects every name the configuration (and the custom
// rules, composed next) refers to as a policy or group member.
func chainReferenced(root *yaml.Node, custom []model.CustomRule) map[string]bool {
	refs := map[string]bool{}
	for _, r := range custom {
		refs[strings.TrimSpace(r.Target)] = true
	}
	addRule := func(s string) {
		// TYPE,payload,TARGET[,no-resolve] or MATCH,TARGET: any field
		// after the type may be the target.
		parts := strings.Split(s, ",")
		for _, p := range parts[1:] {
			refs[strings.TrimSpace(p)] = true
		}
	}
	addRules := func(seq *yaml.Node) {
		seq = deref(seq)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return
		}
		for _, it := range seq.Content {
			if s := deref(it); s != nil && s.Kind == yaml.ScalarNode {
				addRule(s.Value)
			}
		}
	}
	addRules(mapGet(root, "rules"))
	if sr := deref(mapGet(root, "sub-rules")); sr != nil && sr.Kind == yaml.MappingNode {
		for i := 1; i < len(sr.Content); i += 2 {
			addRules(sr.Content[i])
		}
	}
	if gs := deref(mapGet(root, "proxy-groups")); gs != nil && gs.Kind == yaml.SequenceNode {
		for _, it := range gs.Content {
			for _, m := range groupMembers(deref(it)) {
				refs[m] = true
			}
		}
	}
	for _, key := range []string{"listeners", "proxies"} {
		if seq := deref(mapGet(root, key)); seq != nil && seq.Kind == yaml.SequenceNode {
			for _, it := range seq.Content {
				for _, k := range []string{"proxy", "dialer-proxy"} {
					if p := deref(mapGet(deref(it), k)); p != nil && p.Kind == yaml.ScalarNode {
						refs[p.Value] = true
					}
				}
			}
		}
	}
	return refs
}

// addProxyChains generates the chains (see the comment at the top). It
// never fails: a chain that cannot be generated is reported in the
// returned warnings and stands in as a REJECT group where referenced.
func (c *composer) addProxyChains(chains []model.ProxyChain, custom []model.CustomRule) []ChainNote {
	if len(chains) == 0 {
		return nil
	}
	var warns []ChainNote
	warn := func(code string, args ...any) {
		warns = append(warns, chainNote(code, args...))
	}

	env := &chainEnv{proxies: map[string]*yaml.Node{}, groups: map[string]*yaml.Node{}, chainNames: map[string]bool{}}
	if ps := deref(mapGet(c.root, "proxies")); ps != nil && ps.Kind == yaml.SequenceNode {
		for _, it := range ps.Content {
			m := deref(it)
			if n := deref(mapGet(m, "name")); n != nil && n.Kind == yaml.ScalarNode {
				env.proxies[n.Value] = m
			}
		}
	}
	var containerGroup *yaml.Node
	containerTaken := false
	if gs := deref(mapGet(c.root, "proxy-groups")); gs != nil && gs.Kind == yaml.SequenceNode {
		for _, it := range gs.Content {
			gm := deref(it)
			gn := deref(mapGet(gm, "name"))
			if gn == nil || gn.Kind != yaml.ScalarNode {
				continue
			}
			env.groups[gn.Value] = gm
			gt := deref(mapGet(gm, "type"))
			isSelect := gt != nil && gt.Value == "select"
			if gn.Value == ChainGroupName {
				if isSelect {
					containerGroup = gm
				} else {
					containerTaken = true
				}
				continue
			}
			if env.firstSelect == "" && isSelect && gn.Value != "GLOBAL" && gn.Value != probeGroupName && !truthy(mapGet(gm, "hidden")) {
				env.firstSelect = gn.Value
			}
		}
	}
	env.container = ChainGroupName
	if containerTaken {
		env.container = ChainGroupAltName
		containerGroup = nil
		if g, ok := env.groups[ChainGroupAltName]; ok {
			containerGroup = g // reuse ours from an earlier layer only if it is a select group
			if gt := deref(mapGet(g, "type")); gt == nil || gt.Value != "select" {
				warn("container-taken", ChainGroupName, ChainGroupAltName)
				return warns
			}
		}
	}
	builtin := map[string]bool{}
	for _, t := range builtinTargets {
		builtin[t] = true
	}
	builtin["GLOBAL"] = true
	builtin["PASS-RULE"] = true

	// Names first: a chain whose name collides is left out entirely (no
	// placeholder: the name already means something else).
	type item struct {
		ch       model.ProxyChain
		reason   *ChainNote // nil = generate (unless disabled)
		disabled bool
	}
	var items []item
	usable := map[string]bool{}
	for _, ch := range chains {
		ch.Name = strings.TrimSpace(ch.Name)
		switch {
		case ch.Name == "":
			warn("ignored-no-name")
			continue
		case IsReservedName(ch.Name):
			warn("ignored-reserved", ch.Name, reservedPrefix)
			continue
		case env.chainNames[ch.Name]:
			warn("ignored-dup", ch.Name)
			continue
		case builtin[ch.Name] || ch.Name == env.container || ch.Name == ChainGroupName:
			warn("ignored-builtin", ch.Name)
			continue
		}
		if _, ok := env.proxies[ch.Name]; ok {
			warn("ignored-node", ch.Name)
			continue
		}
		if _, ok := env.groups[ch.Name]; ok {
			warn("ignored-group", ch.Name)
			continue
		}
		env.chainNames[ch.Name] = true
		items = append(items, item{ch: ch})
	}
	for i := range items {
		it := &items[i]
		if !it.ch.Enabled {
			it.disabled = true
			continue
		}
		if r, ws := env.validateChain(it.ch); r != nil {
			it.reason = r
		} else {
			warns = append(warns, ws...)
			usable[it.ch.Name] = true
		}
	}

	refs := chainReferenced(c.root, custom)
	ps := c.seq("proxies")
	gs := c.seq("proxy-groups")
	var enabled []string
	for _, it := range items {
		ch := it.ch
		if it.reason != nil || it.disabled {
			if it.reason != nil {
				warn("disabled-because", ch.Name, *it.reason)
			}
			if refs[ch.Name] {
				gs.Content = append(gs.Content, c.mark(mapNode(
					"name", strNode(ch.Name),
					"type", strNode("select"),
					"proxies", strSeq([]string{"REJECT"}),
				)))
				warn("placeholder", ch.Name)
			}
			continue
		}
		ch, _ = NormalizeProxyChain(ch)
		prev := ch.Hops[0]
		for i := 1; i < len(ch.Hops); i++ {
			clone := cloneNode(env.proxies[ch.Hops[i]])
			clone.Anchor = ""
			name := ChainCloneName(ch.ID, i+1)
			mapSet(clone, "name", strNode(name))
			mapSet(clone, "dialer-proxy", strNode(prev))
			ps.Content = append(ps.Content, c.mark(clone))
			prev = name
		}
		gs.Content = append(gs.Content, c.mark(mapNode(
			"name", strNode(ch.Name),
			"type", strNode("select"),
			"proxies", strSeq([]string{prev}),
		)))
		enabled = append(enabled, ch.Name)
	}
	if len(enabled) == 0 {
		return warns // an empty group would fail to load (errMissProxy)
	}

	if containerGroup != nil {
		gp := deref(mapGet(containerGroup, "proxies"))
		if gp == nil || gp.Kind != yaml.SequenceNode {
			gp = seqNode()
			mapSet(containerGroup, "proxies", gp)
		}
		has := seqSet(gp)
		for _, n := range enabled {
			if !has[n] {
				gp.Content = append(gp.Content, c.mark(strNode(n)))
			}
		}
	} else {
		gs.Content = append(gs.Content, c.mark(mapNode(
			"name", strNode(env.container),
			"type", strNode("select"),
			"proxies", strSeq(enabled),
		)))
	}
	if g, ok := env.groups[env.firstSelect]; ok {
		if gp := deref(mapGet(g, "proxies")); gp != nil && gp.Kind == yaml.SequenceNode && !seqSet(gp)[env.container] {
			gp.Content = append(gp.Content, c.mark(strNode(env.container)))
		}
	}
	return warns
}

// excludeChainClones runs after every group is in place (the probe group
// included): Mihomo nodes cannot be hidden, so the clones are kept out of
// the groups that collect nodes by themselves, and the probe group gets
// the chain groups instead, so a chain can be probed as one exit.
//
//   - include-all / include-all-proxies groups get a filter that refuses
//     the clones: no filter becomes chainCloneFilter, each existing filter
//     regex F becomes ^(?!LANFLOW-CHAIN-)(?=.*?(?:F)), which matches the
//     same names except ours (Mihomo keeps a node matching any of the
//     `-separated regexes).
//   - GLOBAL, which Mihomo otherwise builds from every node and group, is
//     written out as a select group with the same members in the same
//     order (DIRECT, REJECT, nodes, groups), clones left out. A GLOBAL the
//     configuration defines itself is left as it is.
func excludeChainClones(c *composer) bool {
	ps := deref(mapGet(c.root, "proxies"))
	var nodes []string
	clones := false
	for _, n := range namesOf(ps) {
		if IsChainClone(n) {
			clones = true
			continue
		}
		nodes = append(nodes, n)
	}
	if !clones {
		return false
	}
	gs := deref(mapGet(c.root, "proxy-groups"))
	if gs == nil || gs.Kind != yaml.SequenceNode {
		return false
	}
	hasGlobal := false
	var chainGroups []string
	for _, it := range gs.Content {
		m := deref(it)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}
		name := ""
		if n := deref(mapGet(m, "name")); n != nil {
			name = n.Value
		}
		if name == "GLOBAL" {
			hasGlobal = true
		}
		if c.nodeSrc[it] == layerChains {
			if mem := groupMembers(m); len(mem) == 1 && IsChainClone(mem[0]) {
				chainGroups = append(chainGroups, name)
			}
		}
		if !truthy(mapGet(m, "include-all")) && !truthy(mapGet(m, "include-all-proxies")) {
			continue
		}
		f := deref(mapGet(m, "filter"))
		if f == nil || f.Kind != yaml.ScalarNode || strings.TrimSpace(f.Value) == "" {
			mapSet(m, "filter", strNode(chainCloneFilter))
			continue
		}
		if strings.HasPrefix(f.Value, chainCloneFilter) {
			continue
		}
		parts := strings.Split(f.Value, "`")
		for i, p := range parts {
			parts[i] = chainCloneFilter + "(?=.*?(?:" + p + "))"
		}
		mapSet(m, "filter", strNode(strings.Join(parts, "`")))
	}
	// the probe group probes chains through their groups
	for _, it := range gs.Content {
		m := deref(it)
		if n := deref(mapGet(m, "name")); n == nil || n.Value != probeGroupName {
			continue
		}
		gp := deref(mapGet(m, "proxies"))
		if gp == nil || gp.Kind != yaml.SequenceNode {
			gp = seqNode()
			mapSet(m, "proxies", gp)
		}
		has := seqSet(gp)
		for _, g := range chainGroups {
			if !has[g] {
				gp.Content = append(gp.Content, strNode(g))
			}
		}
	}
	if !hasGlobal {
		members := append([]string{"DIRECT", "REJECT"}, nodes...)
		members = append(members, namesOf(gs)...)
		gs.Content = append(gs.Content, c.mark(mapNode(
			"name", strNode("GLOBAL"),
			"type", strNode("select"),
			"proxies", strSeq(members),
		)))
	}
	return !hasGlobal
}
