package profile

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

// Aggregate subscriptions (docs/aggregate.md): Lanflow merges the nodes of
// several sources into one profile and generates its proxy groups and
// rules. Everything here is pure; the caller downloads the sources.

// Names of the generated groups. They are part of the generated profile
// (like ManualGroupName), not of the interface.
const (
	AggGroupSelect = "节点选择"
	AggGroupAuto   = "自动选择"
	AggGroupFinal  = "漏网之鱼"
)

// aggTestURL and aggTestInterval are the health check of the url-test groups.
const (
	aggTestURL       = "https://www.gstatic.com/generate_204"
	aggTestInterval  = 300
	aggTestTolerance = 50
)

// Limits of a spec: a profile is built on every update, and the regular
// expressions come from the user.
const (
	AggMaxSources = 32
	AggMaxRenames = 32
	aggMaxPattern = 512
	aggMaxNodes   = 5000
	aggMaxLinks   = 1 << 20
)

// aggRegionMatch is the regexp on node names of each region (AggregateRegions).
var aggRegionMatch = map[string]string{
	"hk": `(?i)港|香港|🇭🇰|\bHK\b|\bHKG\b|Hong ?Kong`,
	"tw": `(?i)台|臺|🇹🇼|\bTW\b|\bTWN\b|Taiwan`,
	"jp": `(?i)日本|东京|東京|大阪|🇯🇵|\bJP\b|\bJPN\b|Japan|Tokyo|Osaka`,
	"sg": `(?i)新加坡|狮城|獅城|🇸🇬|\bSG\b|\bSGP\b|Singapore`,
	"us": `(?i)美国|美國|洛杉矶|硅谷|西雅图|纽约|🇺🇸|\bUS\b|\bUSA\b|United States|America|Los Angeles|San Jose|Seattle`,
	"kr": `(?i)韩国|韓國|首尔|🇰🇷|\bKR\b|\bKOR\b|Korea|Seoul`,
	"gb": `(?i)英国|英國|伦敦|🇬🇧|\bUK\b|\bGB\b|United Kingdom|London`,
	"de": `(?i)德国|德國|法兰克福|🇩🇪|\bDE\b|Germany|Frankfurt`,
}

// aggPrivateRules always come first: LAN and private addresses go direct.
var aggPrivateRules = []string{
	"GEOSITE,private,DIRECT",
	"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
	"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
	"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
	"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
	"IP-CIDR6,fc00::/7,DIRECT,no-resolve",
	"IP-CIDR6,fe80::/10,DIRECT,no-resolve",
}

// DefaultAggregateExclude drops the informational entries many providers
// list as nodes (remaining traffic, expiry, website).
const DefaultAggregateExclude = `(?i)剩余|到期|流量|过期|官网|重置|套餐|expire|traffic|website`

// DefaultAggregateSpec is the spec a new aggregate subscription starts with.
func DefaultAggregateSpec() model.AggregateSpec {
	return model.AggregateSpec{
		Exclude:   DefaultAggregateExclude,
		AutoGroup: true,
		Regions:   []string{"hk", "tw", "jp", "sg", "us"},
		Modules:   []string{"ads", "ai", "telegram", "youtube", "netflix", "github", "google", "microsoft", "apple", "global", "cn"},
	}
}

// Translator formats a text of aggregate building in the interface's
// language (i18n.Tf); nil keeps the Chinese (fmt.Sprintf).
type Translator func(format string, args ...any) string

// text formats the text code of aggFormats.
func (tr Translator) text(code string, args ...any) string {
	f, ok := aggFormats[code]
	if !ok {
		panic("profile: unknown aggregate text " + code)
	}
	if tr == nil {
		return fmt.Sprintf(f, args...)
	}
	return tr(f, args...)
}

func (tr Translator) err(code string, args ...any) error { return errors.New(tr.text(code, args...)) }

// AggregateInput is the content of one source, gathered by the caller:
// the downloaded body of a link, the stored content of a subscription or
// the typed links. Err is set when it could not be had.
type AggregateInput struct {
	Source  model.AggregateSource
	Data    []byte
	Fetched Fetched // a link source's headers (subscription-userinfo)
	Err     error
}

// ValidateAggregateSpec checks a spec as the editor saves it.
func ValidateAggregateSpec(spec model.AggregateSpec, tr Translator) error {
	enabled := 0
	if len(spec.Sources) > AggMaxSources {
		return tr.err("tooManySources", AggMaxSources)
	}
	if len(spec.Renames) > AggMaxRenames {
		return tr.err("tooManyRenames", AggMaxRenames)
	}
	ids := map[string]bool{}
	for i, src := range spec.Sources {
		n := i + 1
		if src.ID != "" {
			if ids[src.ID] {
				return tr.err("dupSource", n)
			}
			ids[src.ID] = true
		}
		switch src.Kind {
		case model.AggSourceURL:
			u := strings.TrimSpace(src.URL)
			if !strings.HasPrefix(strings.ToLower(u), "http://") && !strings.HasPrefix(strings.ToLower(u), "https://") {
				return tr.err("badURL", n)
			}
		case model.AggSourceLinks:
			if strings.TrimSpace(src.Links) == "" {
				return tr.err("noLinks", n)
			}
			if len(src.Links) > aggMaxLinks {
				return tr.err("linksTooLong", n)
			}
		case model.AggSourceSub:
			if src.SubID == "" {
				return tr.err("noSub", n)
			}
		default:
			return tr.err("badKind", n, src.Kind)
		}
		if !src.Disabled {
			enabled++
		}
	}
	if enabled == 0 {
		return tr.err("noSources")
	}
	if _, err := compileAggPattern(spec.Include, tr); err != nil {
		return tr.err("badInclude", err)
	}
	if _, err := compileAggPattern(spec.Exclude, tr); err != nil {
		return tr.err("badExclude", err)
	}
	for i, r := range spec.Renames {
		if strings.TrimSpace(r.Pattern) == "" {
			return tr.err("emptyRename", i+1)
		}
		if _, err := compileAggPattern(r.Pattern, tr); err != nil {
			return tr.err("badRename", i+1, err)
		}
	}
	for _, id := range spec.Regions {
		if regionByID(id) == nil {
			return tr.err("badRegion", id)
		}
	}
	for _, id := range spec.Modules {
		if moduleByID(id) == nil {
			return tr.err("badModule", id)
		}
	}
	return nil
}

func compileAggPattern(p string, tr Translator) (*regexp.Regexp, error) {
	if p == "" {
		return nil, nil
	}
	if len(p) > aggMaxPattern {
		return nil, tr.err("patternTooLong", aggMaxPattern)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, errors.New(strings.TrimPrefix(err.Error(), "error parsing regexp: "))
	}
	return re, nil
}

func regionByID(id string) *AggregateRegion {
	for i := range AggregateRegions {
		if AggregateRegions[i].ID == id {
			return &AggregateRegions[i]
		}
	}
	return nil
}

func moduleByID(id string) *AggregateModule {
	for i := range AggregateModules {
		if AggregateModules[i].ID == id {
			return &AggregateModules[i]
		}
	}
	return nil
}

// aggNode is a node on its way into the profile.
type aggNode struct {
	m      *yaml.Node // the proxy mapping (a private copy)
	name   string
	typ    string
	source string
}

// BuildAggregate builds the profile of an aggregate subscription from its
// spec and the content of its sources (one input per enabled source, in
// order). It fails only when the spec is invalid or no node is left; a
// source that fails is reported and skipped.
func BuildAggregate(spec model.AggregateSpec, inputs []AggregateInput, tr Translator) (model.AggregatePreview, []byte, error) {
	var pv model.AggregatePreview
	if err := ValidateAggregateSpec(spec, tr); err != nil {
		return pv, nil, err
	}
	include, _ := compileAggPattern(spec.Include, tr)
	exclude, _ := compileAggPattern(spec.Exclude, tr)
	type rename struct {
		re  *regexp.Regexp
		rep string
	}
	var renames []rename
	for _, r := range spec.Renames {
		re, _ := compileAggPattern(r.Pattern, tr)
		renames = append(renames, rename{re, r.Replace})
	}

	var nodes []aggNode
	for _, in := range inputs {
		rep := model.AggregateSourceReport{ID: in.Source.ID, Upload: in.Fetched.Upload, Download: in.Fetched.Download, Total: in.Fetched.Total, Expire: in.Fetched.Expire}
		if in.Err != nil {
			rep.Error = in.Err.Error()
			pv.Sources = append(pv.Sources, rep)
			continue
		}
		found, skipped, warn, err := parseAggSource(in.Data, tr)
		if err != nil {
			rep.Error = err.Error()
			pv.Sources = append(pv.Sources, rep)
			continue
		}
		if warn != "" {
			pv.Warnings = append(pv.Warnings, tr.text("sourceWarning", aggSourceLabel(in.Source, tr), warn))
		}
		rep.Found, rep.Skipped = len(found), skipped
		for _, n := range found {
			if include != nil && !include.MatchString(n.name) {
				continue
			}
			if exclude != nil && exclude.MatchString(n.name) {
				continue
			}
			name := n.name
			for _, r := range renames {
				name = r.re.ReplaceAllString(name, r.rep)
			}
			name = strings.TrimSpace(in.Source.Prefix + name)
			if name == "" {
				name = n.typ
			}
			n.name, n.source = name, in.Source.ID
			nodes = append(nodes, n)
			rep.Kept++
		}
		pv.Sources = append(pv.Sources, rep)
	}
	if len(nodes) > aggMaxNodes {
		pv.Warnings = append(pv.Warnings, tr.text("tooManyNodes", aggMaxNodes))
		nodes = nodes[:aggMaxNodes]
	}
	if len(nodes) == 0 {
		for _, r := range pv.Sources {
			if r.Error != "" {
				return pv, nil, tr.err("noNodesBecause", r.Error)
			}
		}
		return pv, nil, tr.err("noNodes")
	}

	// Group names are taken first; node names that collide with them, with
	// a built-in policy or with each other get a number.
	reserved := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true,
		AggGroupSelect: true, AggGroupAuto: true, AggGroupFinal: true}
	for _, r := range AggregateRegions {
		reserved[r.Group] = true
	}
	for _, m := range AggregateModules {
		reserved[m.Group] = true
	}
	used := map[string]bool{}
	for i := range nodes {
		// LANFLOW- names are Lanflow's own (probe, capture, chains).
		base := strings.TrimPrefix(nodes[i].name, "LANFLOW-")
		if base == "" {
			base = nodes[i].typ
		}
		name := base
		for k := 2; reserved[name] || used[name]; k++ {
			name = base + " " + strconv.Itoa(k)
		}
		used[name] = true
		nodes[i].name = name
		mapSet(nodes[i].m, "name", strNode(name))
		pv.Nodes = append(pv.Nodes, model.AggregateNode{Name: name, Type: nodes[i].typ, Source: nodes[i].source})
	}

	root, groups, rules := aggProfile(spec, nodes)
	pv.Groups, pv.Rules = groups, rules
	data, err := encodeDoc(root)
	if err != nil {
		return pv, nil, err
	}
	data = append([]byte("# 澜流聚合订阅：由澜流根据聚合设置生成，请在订阅页「编辑聚合…」中修改\n"), data...)
	pv.YAML = string(data)
	return pv, data, nil
}

func aggSourceLabel(s model.AggregateSource, tr Translator) string {
	if s.Name != "" {
		return s.Name
	}
	switch s.Kind {
	case model.AggSourceURL:
		return tr.text("labelURL")
	case model.AggSourceLinks:
		return tr.text("labelLinks")
	}
	return tr.text("labelSub")
}

// aggProfile generates the profile for the final nodes; it returns the
// group names in order and the number of rules.
func aggProfile(spec model.AggregateSpec, nodes []aggNode) (*yaml.Node, []string, int) {
	names := make([]string, len(nodes))
	proxies := seqNode()
	for i, n := range nodes {
		names[i] = n.name
		proxies.Content = append(proxies.Content, n.m)
	}

	urlTest := func(name string, members []string) *yaml.Node {
		return mapNode("name", strNode(name), "type", strNode("url-test"), "proxies", strSeq(members),
			"url", strNode(aggTestURL), "interval", intNode(aggTestInterval), "tolerance", intNode(aggTestTolerance), "lazy", boolNode(true))
	}
	selectGroup := func(name string, members []string) *yaml.Node {
		return mapNode("name", strNode(name), "type", strNode("select"), "proxies", strSeq(members))
	}

	var regionGroups []*yaml.Node
	var regionNames []string
	for _, r := range AggregateRegions {
		if !contains(spec.Regions, r.ID) {
			continue
		}
		re := regexp.MustCompile(aggRegionMatch[r.ID])
		var members []string
		for _, n := range names {
			if re.MatchString(n) {
				members = append(members, n)
			}
		}
		if len(members) == 0 {
			continue
		}
		regionGroups = append(regionGroups, urlTest(r.Group, members))
		regionNames = append(regionNames, r.Group)
	}

	var head []string // what the main group and the module groups offer before the nodes
	if spec.AutoGroup {
		head = append(head, AggGroupAuto)
	}
	head = append(head, regionNames...)

	mainMembers := append(append(append([]string(nil), head...), names...), "DIRECT")
	groups := seqNode(selectGroup(AggGroupSelect, mainMembers))
	groupNames := []string{AggGroupSelect}
	if spec.AutoGroup {
		groups.Content = append(groups.Content, urlTest(AggGroupAuto, names))
		groupNames = append(groupNames, AggGroupAuto)
	}

	rules := strSeq(aggPrivateRules)
	var moduleGroups []*yaml.Node
	var moduleNames []string
	for _, m := range AggregateModules {
		if !contains(spec.Modules, m.ID) {
			continue
		}
		target := m.Target
		if m.Policy != aggNoGroup {
			target = m.Group
			var members []string
			switch m.Policy {
			case aggRejectFirst:
				members = []string{"REJECT", "DIRECT", AggGroupSelect}
			case aggDirectFirst:
				members = append([]string{"DIRECT", AggGroupSelect}, head...)
			default:
				members = append(append([]string{AggGroupSelect}, head...), "DIRECT")
			}
			moduleGroups = append(moduleGroups, selectGroup(m.Group, members))
			moduleNames = append(moduleNames, m.Group)
		}
		for _, r := range m.Rules {
			line := r + "," + target
			if base, ok := strings.CutSuffix(r, ",no-resolve"); ok {
				line = base + "," + target + ",no-resolve"
			}
			rules.Content = append(rules.Content, strNode(line))
		}
	}
	groups.Content = append(groups.Content, moduleGroups...)
	groupNames = append(groupNames, moduleNames...)
	groups.Content = append(groups.Content, selectGroup(AggGroupFinal, append([]string{AggGroupSelect, "DIRECT"}, head...)))
	groupNames = append(groupNames, AggGroupFinal)
	groups.Content = append(groups.Content, regionGroups...)
	groupNames = append(groupNames, regionNames...)
	rules.Content = append(rules.Content, strNode("MATCH,"+AggGroupFinal))

	root := mapNode("proxies", proxies, "proxy-groups", groups, "rules", rules)
	return root, groupNames, len(rules.Content)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// parseAggSource reads the nodes of one source: a Clash/Mihomo profile
// (its proxies; proxy-providers cannot be merged and are reported), or
// share links one per line, raw or Base64-encoded. skipped counts the
// entries that could not be used.
func parseAggSource(data []byte, tr Translator) (nodes []aggNode, skipped int, warn string, err error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
	if text == "" {
		return nil, 0, "", tr.err("empty")
	}
	if root, perr := parseDoc([]byte(text)); perr == nil && (mapGet(root, "proxies") != nil || mapGet(root, "proxy-providers") != nil) {
		if seq := deref(mapGet(root, "proxies")); seq != nil && seq.Kind == yaml.SequenceNode {
			for _, item := range seq.Content {
				m := flattenNode(item, 0)
				if m == nil || m.Kind != yaml.MappingNode {
					skipped++
					continue
				}
				name, typ := scalarOf(mapGet(m, "name")), scalarOf(mapGet(m, "type"))
				if name == "" || typ == "" || scalarOf(mapGet(m, "server")) == "" && typ != "direct" && typ != "dns" {
					skipped++
					continue
				}
				nodes = append(nodes, aggNode{m: m, name: name, typ: typ})
			}
		}
		if pp := deref(mapGet(root, "proxy-providers")); pp != nil && pp.Kind == yaml.MappingNode && len(pp.Content) > 0 {
			warn = tr.text("providers")
		}
		return nodes, skipped, warn, nil
	}
	lines := shareLinkLines(text)
	if lines == nil {
		return nil, 0, "", tr.err("unknownContent")
	}
	for _, line := range lines {
		mn, perr := parseNodeURI(line)
		if perr != nil {
			skipped++
			continue
		}
		m, berr := BuildManualProxyNode(mn)
		if berr != nil {
			skipped++
			continue
		}
		nodes = append(nodes, aggNode{m: m, name: mn.Name, typ: scalarOf(mapGet(m, "type"))})
	}
	if skipped > 0 && len(nodes) == 0 {
		return nil, skipped, "", tr.err("noLinkUsable", skipped)
	}
	return nodes, skipped, "", nil
}

// shareLinkLines returns the non-empty lines of text when it is a list of
// URI-like lines, decoding Base64 first when needed; nil otherwise. Lines
// of an unknown scheme are kept so that they are counted as skipped.
func shareLinkLines(text string) []string {
	// split keeps every non-empty line once one of them looks like a
	// link: the others are counted as skipped, not the whole source lost
	// to one stray line.
	split := func(t string) []string {
		var out []string
		links := false
		for _, l := range strings.Split(strings.ReplaceAll(t, "\r\n", "\n"), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "//") {
				continue
			}
			links = links || strings.Contains(l, "://")
			out = append(out, l)
		}
		if !links {
			return nil
		}
		return out
	}
	if out := split(text); len(out) > 0 {
		return out
	}
	compact := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, text)
	if dec, err := decodeBase64Flex(compact); err == nil && len(bytes.TrimSpace(dec)) > 0 {
		if out := split(string(dec)); len(out) > 0 {
			return out
		}
	}
	return nil
}

func scalarOf(n *yaml.Node) string {
	n = deref(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(n.Value)
}

// flattenNode returns a self-contained copy of n: aliases are resolved,
// `<<` merge keys expanded (explicit keys win) and anchors dropped, so the
// node can be placed in another document.
func flattenNode(n *yaml.Node, depth int) *yaml.Node {
	n = deref(n)
	if n == nil || depth > 32 {
		return nil
	}
	cp := &yaml.Node{Kind: n.Kind, Tag: n.Tag, Value: n.Value, Style: n.Style}
	switch n.Kind {
	case yaml.MappingNode:
		cp.Tag = "!!map"
		var merged []*yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Tag == "!!merge" || k.Value == "<<" && k.Style == 0 {
				v := deref(n.Content[i+1])
				var srcs []*yaml.Node
				if v != nil && v.Kind == yaml.SequenceNode {
					srcs = v.Content
				} else {
					srcs = []*yaml.Node{v}
				}
				for _, s := range srcs {
					if f := flattenNode(s, depth+1); f != nil && f.Kind == yaml.MappingNode {
						merged = append(merged, f.Content...)
					}
				}
				continue
			}
			cp.Content = append(cp.Content, strNode(k.Value), flattenNode(n.Content[i+1], depth+1))
		}
		for i := 0; i+1 < len(merged); i += 2 {
			if mapIndex(cp, merged[i].Value) < 0 {
				cp.Content = append(cp.Content, merged[i], merged[i+1])
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if f := flattenNode(c, depth+1); f != nil {
				cp.Content = append(cp.Content, f)
			}
		}
	}
	return cp
}
