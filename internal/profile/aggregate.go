package profile

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

// Aggregate subscriptions (docs/aggregate.md): Lanflow merges the nodes of
// several sources into one profile and generates its proxy groups and
// rules. Everything here is pure; the caller downloads the sources.

// aggTestURL and aggTestInterval are the health check of the url-test,
// fallback and load-balance groups.
const (
	aggTestURL       = "https://www.gstatic.com/generate_204"
	aggTestInterval  = 300
	aggTestTolerance = 50
	aggRuleInterval  = 86400 // rule-provider refresh, seconds
)

// Limits of a spec: a profile is built on every update, and the regular
// expressions come from the user.
const (
	AggMaxSources  = 32
	AggMaxRenames  = 32
	AggMaxRules    = 500
	AggMaxGroups   = 120
	AggMaxRelays   = 16
	AggMaxListen   = 65000 // highest first listener port
	aggMaxPattern  = 512
	aggMaxNodes    = 5000
	aggMaxLinks    = 1 << 20
	aggMaxBaseYAML = 64 << 10
)

// aggTelegramCIDRs stand in for geoip:telegram with the bundled database,
// which only knows countries.
var aggTelegramCIDRs = []string{
	"IP-CIDR,91.108.4.0/22", "IP-CIDR,91.108.8.0/22", "IP-CIDR,91.108.12.0/22",
	"IP-CIDR,91.108.16.0/22", "IP-CIDR,91.108.20.0/22", "IP-CIDR,91.108.56.0/22",
	"IP-CIDR,95.161.64.0/20", "IP-CIDR,149.154.160.0/20",
	"IP-CIDR6,2001:67c:4e8::/48", "IP-CIDR6,2001:b28:f23d::/48", "IP-CIDR6,2001:b28:f23f::/48",
}

// aggRegionFilter is the node filter of each region module, by ID: the
// names it matches. (Kept here: the catalog file holds only texts the
// interface translates.)
var aggRegionFilter = map[string]string{
	"hk": `(?i)港|香港|🇭🇰|\bHK\b|\bHKG\b|Hong ?Kong`,
	"tw": `(?i)台|臺|🇹🇼|\bTW\b|\bTWN\b|Taiwan`,
	"jp": `(?i)日本|东京|東京|大阪|🇯🇵|\bJP\b|\bJPN\b|Japan|Tokyo|Osaka`,
	"sg": `(?i)新加坡|狮城|獅城|🇸🇬|\bSG\b|\bSGP\b|Singapore`,
	"us": `(?i)美国|美國|洛杉矶|硅谷|西雅图|纽约|🇺🇸|\bUS\b|\bUSA\b|United States|America|Los Angeles|San Jose|Seattle`,
	"kr": `(?i)韩国|韓國|首尔|🇰🇷|\bKR\b|\bKOR\b|Korea|Seoul`,
	"gb": `(?i)英国|英國|伦敦|🇬🇧|\bUK\b|\bGB\b|United Kingdom|London`,
	"de": `(?i)德国|德國|法兰克福|🇩🇪|\bDE\b|Germany|Frankfurt`,
}

func init() {
	for i := range AggregateModules {
		if f, ok := aggRegionFilter[AggregateModules[i].ID]; ok {
			AggregateModules[i].Filter = f
		}
	}
}

// AggRelayPresets are the relay groups 添加中转组 offers: a name and the
// relay nodes of a region; the user fills in the landing nodes.
var AggRelayPresets = []model.AggregateRelay{
	{Name: "🔗 香港中转", Relay: "香港.*(中转|IPLC|IEPL)"},
	{Name: "🔗 日本中转", Relay: "日本.*(中转|IPLC|IEPL)"},
	{Name: "🔗 新加坡中转", Relay: "新加坡.*(中转|IPLC|IEPL)"},
	{Name: "🔗 美国中转", Relay: "美国.*(中转|IPLC|IEPL)"},
	{Name: "🔗 台湾中转", Relay: "台湾.*(中转|IPLC|IEPL)"},
}

// aggReservedKeys are the top-level keys the generator writes; Base
// cannot set them.
var aggReservedKeys = map[string]bool{"proxies": true, "proxy-groups": true, "rules": true, "rule-providers": true,
	"listeners": true, "proxy-providers": true, "sub-rules": true}

// DefaultAggregateExclude drops the informational entries many providers
// list as nodes (remaining traffic, expiry, website).
const DefaultAggregateExclude = `(?i)剩余|到期|流量|过期|官网|重置|套餐|expire|traffic|website`

// DefaultAggregateSpec is the spec a new aggregate subscription starts with.
func DefaultAggregateSpec() model.AggregateSpec {
	return model.AggregateSpec{
		Version:  model.AggregateSpecVersion,
		Exclude:  DefaultAggregateExclude,
		Template: "standard",
		Groups:   AggTemplate("standard").Groups(),
	}
}

// aggV1Modules maps the modules of a version 1 spec to the catalog's.
var aggV1Modules = map[string][]string{
	"ads": {"ads"}, "ai": {"ai"}, "telegram": {"telegram"}, "youtube": {"youtube"}, "netflix": {"netflix"},
	"disney": {"disney"}, "spotify": {"spotify"}, "tiktok": {"tiktok"}, "social": {"twitter", "meta"},
	"github": {"github"}, "google": {"google"}, "microsoft": {"microsoft"}, "apple": {"apple"},
	"games": {"steam", "games"}, "global": {"global"}, "cn": {"cn"},
}

// UpgradeAggregateSpec returns spec in the current version: a version 1
// spec (auto group, regions, routing modules) becomes the catalog groups
// it had, with 私有网络 and 漏网之鱼, which version 1 always generated.
func UpgradeAggregateSpec(spec model.AggregateSpec) model.AggregateSpec {
	if spec.Version >= model.AggregateSpecVersion {
		return spec
	}
	spec = spec.Clone()
	if len(spec.Groups) > 0 { // written by this version without its number
		spec.AutoGroup, spec.Regions, spec.Modules = false, nil, nil
		spec.Version = model.AggregateSpecVersion
		return spec
	}
	want := map[string]bool{AggModSelect: true, "private": true, AggModFinal: true, AggModAuto: spec.AutoGroup}
	for _, r := range spec.Regions {
		want[r] = true
	}
	for _, m := range spec.Modules {
		for _, id := range aggV1Modules[m] {
			want[id] = true
		}
	}
	spec.Groups = nil
	for _, m := range AggregateModules {
		if want[m.ID] {
			spec.Groups = append(spec.Groups, model.AggregateGroup{ID: m.ID})
		}
	}
	spec.AutoGroup, spec.Regions, spec.Modules = false, nil, nil
	spec.Version = model.AggregateSpecVersion
	return spec
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
// the typed text. Err is set when it could not be had.
type AggregateInput struct {
	Source  model.AggregateSource
	Data    []byte
	Fetched Fetched // a link source's headers (subscription-userinfo)
	Err     error
}

// AggGroupName is the name group g has in the profile.
func AggGroupName(g model.AggregateGroup) string {
	if g.Name != "" {
		return g.Name
	}
	if m := AggModule(g.ID); m != nil {
		return m.Name()
	}
	return g.ID
}

// AggGroupType is the type group g has (model.AggType…).
func AggGroupType(g model.AggregateGroup) string {
	if g.Type != "" {
		return g.Type
	}
	if m := AggModule(g.ID); m != nil {
		return m.Type
	}
	return model.AggTypeSelect
}

// aggGroupFilter is the node filter of g: a custom group's own, else
// the module's.
func aggGroupFilter(g model.AggregateGroup) string {
	if g.Custom {
		return g.Filter
	}
	if m := AggModule(g.ID); m != nil {
		return m.Filter
	}
	return ""
}

// AggGroupRules are the rule sets of g: a custom group's own, else its
// module's, with the extended ones when g.Extended.
func AggGroupRules(g model.AggregateGroup) []string {
	if g.Custom {
		return g.Rules
	}
	if m := AggModule(g.ID); m != nil {
		if g.Extended {
			return append(append([]string(nil), m.Rules...), m.Extra...)
		}
		return m.Rules
	}
	return nil
}

var aggTypes = []string{model.AggTypeSelect, model.AggTypeURLTest, model.AggTypeFallback, model.AggTypeLoadBalance, model.AggTypeRejectFirst, model.AggTypeDirectFirst}

var aggRuleTypes = []string{model.AggRuleDomainSuffix, model.AggRuleDomain, model.AggRuleDomainKeyword, model.AggRuleIPCIDR, model.AggRuleIPCIDR6,
	model.AggRuleProcessName, model.AggRuleGeoSite, model.AggRuleGeoIP}

// AggregateTypes are the group types, in the editor's order.
func AggregateTypes() []string { return append([]string(nil), aggTypes...) }

// AggregateRuleTypes are the rule types of a custom rule, in the editor's order.
func AggregateRuleTypes() []string { return append([]string(nil), aggRuleTypes...) }

// patternLines are the non-empty lines of a multi-line pattern field.
func patternLines(text string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// compileLines compiles every line of text; line is the 1-based line of
// the first error.
func compileLines(text string, tr Translator) (res []*regexp.Regexp, line int, err error) {
	for i, l := range patternLines(text) {
		re, err := compileAggPattern(l, tr)
		if err != nil {
			return nil, i + 1, err
		}
		res = append(res, re)
	}
	return res, 0, nil
}

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// withRequired returns groups with 节点选择 first and 漏网之鱼 present,
// which every profile needs.
func withRequired(groups []model.AggregateGroup) []model.AggregateGroup {
	has := func(id string) bool {
		for _, g := range groups {
			if g.ID == id && !g.Custom {
				return true
			}
		}
		return false
	}
	out := append([]model.AggregateGroup(nil), groups...)
	if !has(AggModSelect) {
		out = append([]model.AggregateGroup{{ID: AggModSelect}}, out...)
	}
	if !has(AggModFinal) {
		out = append(out, model.AggregateGroup{ID: AggModFinal})
	}
	return out
}

// ValidateAggregateSpec checks a spec as the editor saves it.
func ValidateAggregateSpec(spec model.AggregateSpec, tr Translator) error {
	spec = UpgradeAggregateSpec(spec)
	enabled := 0
	if len(spec.Sources) > AggMaxSources {
		return tr.err("tooManySources", AggMaxSources)
	}
	if len(spec.Renames) > AggMaxRenames {
		return tr.err("tooManyRenames", AggMaxRenames)
	}
	if len(spec.Rules) > AggMaxRules {
		return tr.err("tooManyRules", AggMaxRules)
	}
	if len(spec.Groups) > AggMaxGroups {
		return tr.err("tooManyGroups", AggMaxGroups)
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
			if !isHTTPURL(src.URL) {
				return tr.err("badURL", n)
			}
		case model.AggSourceYAML:
			if strings.TrimSpace(src.YAML) == "" {
				return tr.err("noYAML", n)
			}
			if len(src.YAML) > aggMaxLinks {
				return tr.err("linksTooLong", n)
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
	if _, line, err := compileLines(spec.Include, tr); err != nil {
		return tr.err("badInclude", line, err)
	}
	if _, line, err := compileLines(spec.Exclude, tr); err != nil {
		return tr.err("badExclude", line, err)
	}
	for i, r := range spec.Renames {
		if strings.TrimSpace(r.Pattern) == "" {
			return tr.err("emptyRename", i+1)
		}
		if _, err := compileAggPattern(r.Pattern, tr); err != nil {
			return tr.err("badRename", i+1, err)
		}
	}
	if spec.ListenBase != 0 && (spec.ListenBase < 1024 || spec.ListenBase > AggMaxListen) {
		return tr.err("badListen", AggMaxListen)
	}

	// Groups: catalog ones once each, custom ones named; names unique
	// (relay groups included) and not a built-in policy.
	names := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true}
	seen := map[string]bool{}
	customN := 0
	for _, g := range withRequired(spec.Groups) {
		if g.Custom {
			customN++
			if strings.TrimSpace(g.Name) == "" {
				return tr.err("noGroupName", customN)
			}
		} else if AggModule(g.ID) == nil {
			return tr.err("badModule", g.ID)
		}
		if seen[g.ID] {
			return tr.err("dupGroup", g.ID)
		}
		seen[g.ID] = true
		name := AggGroupName(g)
		if names[name] {
			return tr.err("dupGroupName", name)
		}
		names[name] = true
		if !contains(aggTypes, AggGroupType(g)) {
			return tr.err("badGroupType", name, g.Type)
		}
		if _, err := compileAggPattern(g.Filter, tr); err != nil {
			return tr.err("badGroupFilter", name, err)
		}
		for _, r := range g.Rules {
			if !validRuleSetRef(r) {
				return tr.err("badGroupRule", name, r)
			}
		}
	}
	if len(spec.Relays) > AggMaxRelays {
		return tr.err("badRelay", AggMaxRelays+1, tr.text("relayNoName"))
	}
	for i, r := range spec.Relays {
		switch {
		case strings.TrimSpace(r.Name) == "":
			return tr.err("badRelay", i+1, tr.text("relayNoName"))
		case names[strings.TrimSpace(r.Name)]:
			return tr.err("dupGroupName", r.Name)
		case strings.TrimSpace(r.Relay) == "":
			return tr.err("badRelay", i+1, tr.text("relayNoRelay"))
		case strings.TrimSpace(r.Landing) == "":
			return tr.err("badRelay", i+1, tr.text("relayNoLanding"))
		}
		names[strings.TrimSpace(r.Name)] = true
		for _, p := range []string{r.Relay, r.Landing} {
			if _, err := compileAggPattern(p, tr); err != nil {
				return tr.err("badRelay", i+1, tr.text("relayBadRe", err))
			}
		}
	}
	for i, r := range spec.Rules {
		if msg := checkAggRule(r, seen, tr); msg != "" {
			return tr.err("badRule", i+1, msg)
		}
	}
	switch spec.RuleSource {
	case model.AggRulesGeo:
	case model.AggRulesRemote:
		if spec.RuleBaseURL != "" && !isHTTPURL(spec.RuleBaseURL) {
			return tr.err("badRuleBase")
		}
	default:
		return tr.err("badRuleSource", spec.RuleSource)
	}
	if _, err := aggBase(spec.Base); err != nil {
		return tr.err("badBase", err)
	}
	return nil
}

// checkAggRule returns what is wrong with a custom rule, "" if nothing;
// groups are the IDs of the spec's groups.
func checkAggRule(r model.AggregateRule, groups map[string]bool, tr Translator) string {
	v := strings.TrimSpace(r.Value)
	switch {
	case !contains(aggRuleTypes, r.Type):
		return tr.text("ruleBadType", r.Type)
	case v == "":
		return tr.text("ruleNoValue")
	case r.Target != "DIRECT" && r.Target != "REJECT" && !groups[r.Target]:
		return tr.text("ruleBadTarget", r.Target)
	}
	switch r.Type {
	case model.AggRuleGeoSite, model.AggRuleGeoIP:
		if !validRuleSetRef(AggregateRuleKey(r.Type, v)) {
			return tr.text("ruleBadSet", v)
		}
	case model.AggRuleIPCIDR, model.AggRuleIPCIDR6:
		if _, err := netip.ParsePrefix(v); err != nil {
			return tr.text("ruleBadCIDR", v)
		}
	}
	if strings.ContainsAny(v, ",\n") {
		return tr.text("ruleBadType", v)
	}
	return ""
}

func isHTTPURL(s string) bool {
	u := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(u, "http://") && len(u) > 7 || strings.HasPrefix(u, "https://") && len(u) > 8
}

// aggBase parses the base YAML: a mapping, or nil when empty.
func aggBase(text string) (*yaml.Node, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	if len(text) > aggMaxBaseYAML {
		return nil, fmt.Errorf("> %d KB", aggMaxBaseYAML>>10)
	}
	root, err := parseDoc([]byte(text))
	if err != nil {
		return nil, err
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, errors.New("not a mapping")
	}
	return root, nil
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
	spec = UpgradeAggregateSpec(spec)
	if err := ValidateAggregateSpec(spec, tr); err != nil {
		return pv, nil, err
	}
	includes, _, _ := compileLines(spec.Include, tr)
	excludes, _, _ := compileLines(spec.Exclude, tr)
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
			if len(includes) > 0 && !matchAny(includes, n.name) {
				continue
			}
			if matchAny(excludes, n.name) {
				continue
			}
			name := n.name
			for _, r := range renames {
				name = r.re.ReplaceAllString(name, r.rep)
			}
			name = strings.Join(strings.Fields(in.Source.Prefix+name), " ")
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
	reserved := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true}
	for _, g := range withRequired(spec.Groups) {
		reserved[AggGroupName(g)] = true
	}
	for _, r := range spec.Relays {
		reserved[strings.TrimSpace(r.Name)] = true
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
	}

	// The user's order first, then the rest as they came.
	if len(spec.NodeOrder) > 0 {
		rank := map[string]int{}
		for i, n := range spec.NodeOrder {
			if _, ok := rank[n]; !ok {
				rank[n] = i
			}
		}
		sort.SliceStable(nodes, func(i, j int) bool {
			ri, oki := rank[nodes[i].name]
			rj, okj := rank[nodes[j].name]
			if oki != okj {
				return oki
			}
			return oki && ri < rj
		})
	}
	hidden := map[string]bool{}
	for _, n := range spec.Hidden {
		hidden[n] = true
	}
	var visible []aggNode
	for _, n := range nodes {
		if !hidden[n.name] {
			visible = append(visible, n)
		}
	}
	if len(visible) == 0 {
		return pv, nil, tr.err("allHidden")
	}

	g := aggGen{spec: spec, nodes: visible, tr: tr}
	root := g.profile()
	pv.Warnings = append(pv.Warnings, g.warnings...)
	pv.Groups, pv.GroupViews, pv.RuleViews = g.groupNames, g.groupViews, g.ruleViews
	pv.Rules, pv.RuleSets = g.ruleLines, len(g.sets)
	for _, n := range nodes {
		pv.Nodes = append(pv.Nodes, model.AggregateNode{Name: n.name, Type: n.typ, Source: n.source, Hidden: hidden[n.name],
			Port: g.ports[n.name], Relay: g.relayOf[n.name]})
	}
	data, err := encodeDoc(root)
	if err != nil {
		return pv, nil, err
	}
	data = unescapeAstral(data)
	data = append([]byte("# 澜流聚合订阅：由澜流根据聚合设置生成，请在订阅页「编辑聚合…」中修改\n"), data...)
	pv.YAML = string(data)
	return pv, data, nil
}

// unescapeAstral writes the \UXXXXXXXX escapes the YAML encoder uses for
// characters beyond the BMP (emoji in group and node names) as the
// characters themselves, inside the same double-quoted scalars, so the
// profile reads as it will be shown. An escaped backslash before a U is
// left alone.
func unescapeAstral(b []byte) []byte {
	if !bytes.Contains(b, []byte(`\U`)) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' && i+9 < len(b) && b[i+1] == 'U' {
			// Count the backslashes before: an odd run means this one is escaped.
			k := 0
			for j := i - 1; j >= 0 && b[j] == '\\'; j-- {
				k++
			}
			if k%2 == 0 {
				if r, err := strconv.ParseUint(string(b[i+2:i+10]), 16, 32); err == nil && r >= 0x10000 && r <= 0x10FFFF {
					out = append(out, string(rune(r))...)
					i += 9
					continue
				}
			}
		}
		out = append(out, b[i])
	}
	return out
}

func aggSourceLabel(s model.AggregateSource, tr Translator) string {
	if s.Name != "" {
		return s.Name
	}
	switch s.Kind {
	case model.AggSourceURL:
		return tr.text("labelURL")
	case model.AggSourceYAML:
		return tr.text("labelYAML")
	case model.AggSourceLinks:
		return tr.text("labelLinks")
	}
	return tr.text("labelSub")
}

// aggGen generates the profile for the final nodes.
type aggGen struct {
	spec  model.AggregateSpec
	nodes []aggNode
	tr    Translator

	warnings   []string
	groupNames []string
	groupViews []model.AggregateGroupView
	ruleViews  []model.AggregateRuleView
	ruleLines  int
	sets       map[string]bool
	ports      map[string]int
	relayOf    map[string]string
}

// aggGroupPlan is a group being generated.
type aggGroupPlan struct {
	g       model.AggregateGroup
	name    string
	typ     string
	members []string
	skip    bool // a filtered group with no node
}

func (g *aggGen) profile() *yaml.Node {
	spec := g.spec
	names := make([]string, len(g.nodes))
	byName := map[string]*aggNode{}
	for i := range g.nodes {
		names[i] = g.nodes[i].name
		byName[names[i]] = &g.nodes[i]
	}
	g.sets, g.ports, g.relayOf = map[string]bool{}, map[string]int{}, map[string]string{}

	// Relay groups: their members cannot be landing nodes of any relay.
	type relay struct {
		name    string
		members []string
	}
	var relays []relay
	inRelay := map[string]bool{}
	for _, r := range spec.Relays {
		re, _ := compileAggPattern(r.Relay, g.tr)
		var members []string
		for _, n := range names {
			if re.MatchString(n) {
				members = append(members, n)
				inRelay[n] = true
			}
		}
		name := strings.TrimSpace(r.Name)
		if len(members) == 0 {
			g.warnings = append(g.warnings, g.tr.text("relayEmpty", name))
			continue
		}
		relays = append(relays, relay{name, members})
	}
	for _, r := range spec.Relays {
		name := strings.TrimSpace(r.Name)
		ok := false
		for _, rl := range relays {
			ok = ok || rl.name == name
		}
		if !ok {
			continue
		}
		re, _ := compileAggPattern(r.Landing, g.tr)
		for _, n := range names {
			if re.MatchString(n) && !inRelay[n] && g.relayOf[n] == "" {
				g.relayOf[n] = name
				mapSet(byName[n].m, "dialer-proxy", strNode(name))
			}
		}
	}

	// Plans: the members of each group by its type.
	groups := withRequired(spec.Groups)
	plans := make([]aggGroupPlan, len(groups))
	nameOf := map[string]string{}
	for i, gr := range groups {
		plans[i] = aggGroupPlan{g: gr, name: AggGroupName(gr), typ: AggGroupType(gr)}
		nameOf[gr.ID] = plans[i].name
	}
	selectName := nameOf[AggModSelect]
	// pool is the nodes a group may use: those its filter matches.
	pool := func(gr model.AggregateGroup) ([]string, bool) {
		f := aggGroupFilter(gr)
		if f == "" {
			return names, false
		}
		re, _ := compileAggPattern(f, g.tr)
		var out []string
		for _, n := range names {
			if re.MatchString(n) {
				out = append(out, n)
			}
		}
		return out, true
	}
	// head is what the other groups offer before the nodes: 自动选择,
	// the region groups that have nodes, the relay groups.
	var head []string
	for i := range plans {
		p := &plans[i]
		if p.g.ID == AggModAuto && !p.g.Custom {
			head = append(head, p.name)
			continue
		}
		if nodes, filtered := pool(p.g); filtered {
			if len(nodes) == 0 {
				p.skip = true
			} else if !p.g.Custom && AggModule(p.g.ID).Category == AggCatRegion {
				head = append(head, p.name)
			}
		}
	}
	for _, r := range relays {
		head = append(head, r.name)
	}
	without := func(list []string, name string) []string {
		var out []string
		for _, s := range list {
			if s != name {
				out = append(out, s)
			}
		}
		return out
	}
	for i := range plans {
		p := &plans[i]
		if p.skip {
			continue
		}
		nodes, filtered := pool(p.g)
		h := without(head, p.name)
		switch {
		case p.g.ID == AggModSelect && !p.g.Custom && p.typ == model.AggTypeSelect:
			p.members = append(append(append([]string(nil), h...), names...), "DIRECT")
		case p.g.ID == AggModFinal && !p.g.Custom && p.typ == model.AggTypeSelect:
			p.members = append([]string{selectName, "DIRECT"}, h...)
		case p.typ == model.AggTypeURLTest || p.typ == model.AggTypeFallback || p.typ == model.AggTypeLoadBalance:
			p.members = append([]string(nil), nodes...)
		case p.typ == model.AggTypeRejectFirst:
			p.members = []string{"REJECT", "DIRECT", selectName}
		case p.typ == model.AggTypeDirectFirst:
			p.members = append([]string{"DIRECT", selectName}, h...)
		case filtered:
			p.members = append([]string(nil), nodes...)
		default:
			p.members = append(append([]string{selectName}, h...), "DIRECT")
		}
		p.members = without(p.members, p.name)
	}

	// Rules: the custom ones first, then each group's rule sets in group
	// order; a rule key only once.
	type ruleItem struct {
		view  model.AggregateRuleView
		lines []string
	}
	var items []ruleItem
	seen := map[string]bool{}
	providers := mapNode()
	var geoSkipped []string
	target := func(id string) string {
		if id == "DIRECT" || id == "REJECT" {
			return id
		}
		for _, p := range plans {
			if p.g.ID == id && !p.skip {
				return p.name
			}
		}
		return selectName
	}
	add := func(typ, value, tgt string, noResolve, custom bool) {
		key := AggregateRuleKey(typ, value)
		if seen[key] {
			return
		}
		seen[key] = true
		var lines []string
		label := value
		switch typ {
		case model.AggRuleGeoSite, model.AggRuleGeoIP:
			ip := typ == model.AggRuleGeoIP
			name := strings.ToLower(value)
			if spec.RuleSource == model.AggRulesRemote {
				kind := "geosite"
				behavior := "domain"
				if ip {
					kind, behavior = "geoip", "ipcidr"
				}
				prov := kind + "-" + name
				if mapGet(providers, prov) == nil {
					base := strings.TrimRight(spec.RuleBaseURL, "/")
					if base == "" {
						base = DefaultRuleBaseURL
					}
					mapSet(providers, prov, mapNode("type", strNode("http"), "behavior", strNode(behavior), "format", strNode("mrs"),
						"url", strNode(base+"/"+kind+"/"+name+".mrs"), "path", strNode("./ruleset/"+prov+".mrs"), "interval", intNode(aggRuleInterval)))
				}
				line := "RULE-SET," + prov + "," + tgt
				if ip {
					line += ",no-resolve"
				}
				lines = []string{line}
			} else if !ip {
				lines = []string{"GEOSITE," + name + "," + tgt}
			} else {
				switch {
				case name == "private":
					lines = []string{"GEOIP,LAN," + tgt + ",no-resolve"}
				case name == "telegram":
					for _, c := range aggTelegramCIDRs {
						lines = append(lines, c+","+tgt+",no-resolve")
					}
				case len(name) == 2:
					lines = []string{"GEOIP," + strings.ToUpper(name) + "," + tgt + ",no-resolve"}
				default:
					// A module's extra IP set is optional; a rule the user
					// added is worth a warning.
					if custom {
						geoSkipped = append(geoSkipped, "geoip:"+name)
					}
					return
				}
			}
			g.sets[key] = true
			label = key
		default:
			line := typ + "," + strings.TrimSpace(value) + "," + tgt
			if noResolve && (typ == model.AggRuleIPCIDR || typ == model.AggRuleIPCIDR6) {
				line += ",no-resolve"
			}
			lines = []string{line}
		}
		items = append(items, ruleItem{model.AggregateRuleView{Key: key, Line: lines[0], Label: label, Group: tgt, Custom: custom}, lines})
	}
	for _, r := range spec.Rules {
		add(r.Type, strings.TrimSpace(r.Value), target(r.Target), r.NoResolve, true)
	}
	for _, p := range plans {
		if p.skip {
			continue
		}
		for _, ref := range AggGroupRules(p.g) {
			kind, name, _ := strings.Cut(ref, ":")
			typ := model.AggRuleGeoSite
			if kind == "geoip" {
				typ = model.AggRuleGeoIP
			}
			add(typ, name, p.name, kind == "geoip", false)
		}
	}
	if len(geoSkipped) > 0 {
		g.warnings = append(g.warnings, g.tr.text("geoSkipped", strings.Join(geoSkipped, "、")))
	}
	if len(spec.RuleOrder) > 0 {
		keys := make([]string, len(items))
		for i, it := range items {
			keys[i] = it.view.Key
		}
		order := orderRuleKeys(keys, spec.RuleOrder)
		byKey := map[string]ruleItem{}
		for _, it := range items {
			byKey[it.view.Key] = it
		}
		items = items[:0]
		for _, k := range order {
			items = append(items, byKey[k])
		}
	}
	finalName := nameOf[AggModFinal]
	rules := seqNode()
	for _, it := range items {
		for _, l := range it.lines {
			rules.Content = append(rules.Content, strNode(l))
		}
		g.ruleViews = append(g.ruleViews, it.view)
	}
	rules.Content = append(rules.Content, strNode("MATCH,"+finalName))
	g.ruleViews = append(g.ruleViews, model.AggregateRuleView{Key: "MATCH", Line: "MATCH," + finalName, Label: "MATCH", Group: finalName})
	g.ruleLines = len(rules.Content)

	// Groups in the spec's order, the relay groups last.
	proxies := seqNode()
	for _, n := range g.nodes {
		proxies.Content = append(proxies.Content, n.m)
	}
	pg := seqNode()
	for _, p := range plans {
		if p.skip {
			continue
		}
		pg.Content = append(pg.Content, aggGroupNode(p.name, p.typ, p.members))
		g.groupNames = append(g.groupNames, p.name)
		view := model.AggregateGroupView{ID: p.g.ID, Name: p.name, Type: p.typ, Members: len(p.members)}
		if len(p.members) > 0 {
			view.Default = p.members[0]
		}
		for _, rv := range g.ruleViews {
			if rv.Group == p.name {
				view.Rules = append(view.Rules, rv.Label)
			}
		}
		g.groupViews = append(g.groupViews, view)
	}
	for _, r := range relays {
		pg.Content = append(pg.Content, aggGroupNode(r.name, model.AggTypeSelect, r.members))
		g.groupNames = append(g.groupNames, r.name)
		g.groupViews = append(g.groupViews, model.AggregateGroupView{Name: r.name, Type: model.AggTypeSelect, Default: r.members[0], Members: len(r.members)})
	}

	root := mapNode()
	if base, _ := aggBase(spec.Base); base != nil {
		for i := 0; i+1 < len(base.Content); i += 2 {
			if k := base.Content[i].Value; !aggReservedKeys[k] {
				root.Content = append(root.Content, strNode(k), flattenNode(base.Content[i+1], 0))
			}
		}
	}
	mapSet(root, "proxies", proxies)
	mapSet(root, "proxy-groups", pg)
	if len(providers.Content) > 0 {
		mapSet(root, "rule-providers", providers)
	}
	mapSet(root, "rules", rules)
	if spec.ListenBase > 0 {
		ls := seqNode()
		for i, n := range names {
			port := spec.ListenBase + i
			if port > 65535 {
				break
			}
			g.ports[n] = port
			ls.Content = append(ls.Content, mapNode("name", strNode("aggregate-"+strconv.Itoa(port)), "type", strNode("mixed"),
				"port", intNode(port), "proxy", strNode(n)))
		}
		mapSet(root, "listeners", ls)
	}
	return root
}

// aggGroupNode writes a proxy group of type typ (model.AggType…).
func aggGroupNode(name, typ string, members []string) *yaml.Node {
	switch typ {
	case model.AggTypeURLTest:
		return mapNode("name", strNode(name), "type", strNode("url-test"), "proxies", strSeq(members),
			"url", strNode(aggTestURL), "interval", intNode(aggTestInterval), "tolerance", intNode(aggTestTolerance), "lazy", boolNode(true))
	case model.AggTypeFallback:
		return mapNode("name", strNode(name), "type", strNode("fallback"), "proxies", strSeq(members),
			"url", strNode(aggTestURL), "interval", intNode(aggTestInterval), "lazy", boolNode(true))
	case model.AggTypeLoadBalance:
		return mapNode("name", strNode(name), "type", strNode("load-balance"), "proxies", strSeq(members),
			"url", strNode(aggTestURL), "interval", intNode(aggTestInterval), "strategy", strNode("consistent-hashing"), "lazy", boolNode(true))
	}
	return mapNode("name", strNode(name), "type", strNode("select"), "proxies", strSeq(members))
}

// AggregateRuleKey is the key of a rule in AggregateSpec.RuleOrder: the
// rule set ("geosite:youtube", "geoip:cn") or TYPE,value.
func AggregateRuleKey(typ, value string) string {
	switch typ {
	case model.AggRuleGeoSite:
		return "geosite:" + strings.ToLower(strings.TrimSpace(value))
	case model.AggRuleGeoIP:
		return "geoip:" + strings.ToLower(strings.TrimSpace(value))
	}
	return typ + "," + strings.TrimSpace(value)
}

// orderRuleKeys orders keys (the generated order) by order: the keys it
// lists come in its order, and each other key stays right after the key
// that preceded it in the generated order.
func orderRuleKeys(keys, order []string) []string {
	rank := map[string]int{}
	for i, k := range order {
		if _, ok := rank[k]; !ok {
			rank[k] = i
		}
	}
	type kw struct {
		key string
		w   float64
		i   int
	}
	ws := make([]kw, len(keys))
	last := -1.0
	for i, k := range keys {
		if r, ok := rank[k]; ok {
			last = float64(r)
			ws[i] = kw{k, last, i}
		} else {
			ws[i] = kw{k, last + 0.5, i}
		}
	}
	sort.SliceStable(ws, func(a, b int) bool {
		if ws[a].w != ws[b].w {
			return ws[a].w < ws[b].w
		}
		return ws[a].i < ws[b].i
	})
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.key
	}
	return out
}

// AggregateRuleViews lists the rules of spec as BuildAggregate generates
// them, without any node: the editor's 规则管理 shows them before a
// preview. Filtered groups count as having nodes.
func AggregateRuleViews(spec model.AggregateSpec, tr Translator) []model.AggregateRuleView {
	spec = UpgradeAggregateSpec(spec)
	spec.Relays, spec.ListenBase, spec.Base = nil, 0, ""
	// One node matching every filter keeps the filtered groups.
	m := mapNode("name", strNode("x"), "type", strNode("direct"))
	var groups []model.AggregateGroup
	for _, gr := range spec.Groups {
		gr.Filter = ""
		if !gr.Custom {
			if mod := AggModule(gr.ID); mod != nil && mod.Filter != "" {
				gr = model.AggregateGroup{ID: gr.ID, Custom: true, Name: AggGroupName(gr), Type: AggGroupType(gr), Rules: AggGroupRules(gr)}
			}
		}
		groups = append(groups, gr)
	}
	spec.Groups = groups
	var valid []model.AggregateRule
	ids := map[string]bool{"DIRECT": true, "REJECT": true}
	for _, gr := range withRequired(spec.Groups) {
		ids[gr.ID] = true
	}
	for _, r := range spec.Rules {
		if checkAggRule(r, ids, tr) == "" {
			valid = append(valid, r)
		}
	}
	spec.Rules = valid
	g := aggGen{spec: spec, nodes: []aggNode{{m: m, name: "x", typ: "direct"}}, tr: tr}
	g.profile()
	return g.ruleViews
}

// parseAggSource reads the nodes of one source: a Clash/Mihomo profile
// (its proxies; proxy-providers cannot be merged and are reported), Clash
// proxies on their own (a list of them, or one), or share links one per
// line, raw or Base64-encoded; share links and Clash proxies may also be
// mixed in one paste. skipped counts the entries that could not be used.
func parseAggSource(data []byte, tr Translator) (nodes []aggNode, skipped int, warn string, err error) {
	text := strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
	if text == "" {
		return nil, 0, "", tr.err("empty")
	}
	if items, providers, ok := aggYAMLProxies(text); ok {
		nodes, skipped = aggProxyItems(items)
		if providers {
			warn = tr.text("providers")
		}
		return nodes, skipped, warn, nil
	}
	lines := shareLinkLines(text)
	// Share links mixed with Clash proxies: the lines that are links are
	// links, the others the YAML.
	if links, rest := splitLinkLines(text); len(links) > 0 && strings.TrimSpace(rest) != "" {
		if items, providers, ok := aggYAMLProxies(rest); ok {
			nodes, skipped = aggProxyItems(items)
			if providers {
				warn = tr.text("providers")
			}
			lines = links
		}
	}
	if lines == nil {
		return nil, 0, "", tr.err("unknownContent")
	}
	for _, line := range lines {
		if len(line) > 6 && strings.EqualFold(line[:6], "ssr://") {
			m, name, serr := parseSSRLink(line[6:])
			if serr != nil {
				skipped++
				continue
			}
			nodes = append(nodes, aggNode{m: m, name: name, typ: "ssr"})
			continue
		}
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
	return nodes, skipped, warn, nil
}

// parseSSRLink reads a ShadowsocksR share link (what follows ssr://):
// Base64 of server:port:protocol:method:obfs:Base64(password), then
// /?obfsparam=…&protoparam=…&remarks=… with Base64 values.
func parseSSRLink(payload string) (*yaml.Node, string, error) {
	dec, err := decodeBase64Flex(strings.TrimRight(strings.TrimSpace(payload), "="))
	if err != nil {
		if dec, err = decodeBase64Flex(strings.TrimSpace(payload)); err != nil {
			return nil, "", err
		}
	}
	text := strings.TrimSpace(string(dec))
	head, query, _ := strings.Cut(text, "?")
	head = strings.TrimSuffix(head, "/")
	// The server may be an IPv6 address: the five other fields are the
	// last ones.
	parts := strings.Split(head, ":")
	if len(parts) < 6 {
		return nil, "", errors.New("ssr: not enough fields")
	}
	n := len(parts)
	server := strings.Trim(strings.Join(parts[:n-5], ":"), "[]")
	port, perr := strconv.Atoi(parts[n-5])
	protocol, method, obfs := parts[n-4], parts[n-3], parts[n-2]
	pass, berr := decodeBase64Flex(parts[n-1])
	if server == "" || perr != nil || port <= 0 || port > 65535 || berr != nil || method == "" {
		return nil, "", errors.New("ssr: bad server, port or password")
	}
	param := func(v url.Values, key string) string {
		if raw := v.Get(key); raw != "" {
			if b, err := decodeBase64Flex(strings.ReplaceAll(raw, " ", "+")); err == nil {
				return strings.TrimSpace(string(b))
			}
		}
		return ""
	}
	v, _ := url.ParseQuery(query)
	name := param(v, "remarks")
	if name == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}
	m := mapNode("name", strNode(name), "type", strNode("ssr"), "server", strNode(server), "port", intNode(port),
		"cipher", strNode(method), "password", strNode(string(pass)), "obfs", strNode(obfs), "protocol", strNode(protocol))
	if op := param(v, "obfsparam"); op != "" {
		m.Content = append(m.Content, strNode("obfs-param"), strNode(op))
	}
	if pp := param(v, "protoparam"); pp != "" {
		m.Content = append(m.Content, strNode("protocol-param"), strNode(pp))
	}
	m.Content = append(m.Content, strNode("udp"), boolNode(true))
	return m, name, nil
}

// aggYAMLProxies reads text as Clash YAML holding proxies: a profile with
// proxies or proxy-providers, a list of proxies, or one proxy. ok is false
// when it is none of these.
func aggYAMLProxies(text string) (items []*yaml.Node, providers, ok bool) {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil || len(doc.Content) == 0 {
		return nil, false, false
	}
	root := deref(doc.Content[0])
	if root == nil {
		return nil, false, false
	}
	isProxy := func(n *yaml.Node) bool {
		n = deref(n)
		return n != nil && n.Kind == yaml.MappingNode && scalarOf(mapGet(n, "type")) != "" && scalarOf(mapGet(n, "name")) != ""
	}
	switch root.Kind {
	case yaml.MappingNode:
		if mapGet(root, "proxies") != nil || mapGet(root, "proxy-providers") != nil {
			if seq := deref(mapGet(root, "proxies")); seq != nil && seq.Kind == yaml.SequenceNode {
				items = seq.Content
			}
			pp := deref(mapGet(root, "proxy-providers"))
			return items, pp != nil && pp.Kind == yaml.MappingNode && len(pp.Content) > 0, true
		}
		if isProxy(root) {
			return []*yaml.Node{root}, false, true
		}
	case yaml.SequenceNode:
		for _, it := range root.Content {
			if isProxy(it) {
				return root.Content, false, true
			}
		}
	}
	return nil, false, false
}

// aggProxyItems turns Clash proxy entries into nodes; skipped counts the
// entries that are not usable proxies.
func aggProxyItems(items []*yaml.Node) (nodes []aggNode, skipped int) {
	for _, item := range items {
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
		// A dialer-proxy of the source refers to its own groups.
		mapDelete(m, "dialer-proxy")
		nodes = append(nodes, aggNode{m: m, name: name, typ: typ})
	}
	return nodes, skipped
}

// aggLinkLine matches a line that is a share link (scheme://…).
var aggLinkLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://\S`)

// splitLinkLines separates the share-link lines of text from the other
// lines, which are kept in place (as YAML).
func splitLinkLines(text string) (links []string, rest string) {
	var other []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(l); aggLinkLine.MatchString(t) {
			links = append(links, t)
			continue
		}
		other = append(other, l)
	}
	return links, strings.Join(other, "\n")
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

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
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
