package model

import "time"

// SourceAggregate is the Subscription.Source of an aggregate subscription
// (docs/aggregate.md): Lanflow builds its content itself from the
// AggregateSpec stored with it, merging the nodes of several sources and
// generating the proxy groups and rules.
const SourceAggregate = "aggregate"

// AggregateSpecVersion is the version of AggregateSpec this build writes;
// older specs are upgraded when read (profile.UpgradeAggregateSpec).
const AggregateSpecVersion = 2

// Kinds of an aggregate source.
const (
	AggSourceURL   = "url"   // a subscription link: Clash/Mihomo YAML or share links (raw / Base64)
	AggSourceYAML  = "yaml"  // a Clash/Mihomo configuration pasted by the user (its proxies)
	AggSourceLinks = "links" // share links typed or pasted by the user, one per line
	AggSourceSub   = "sub"   // the stored content of another subscription of this Lanflow
)

// AggregateSource is one place the nodes of an aggregate subscription come from.
type AggregateSource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`   // label shown in the editor
	URL      string `json:"url,omitempty"`    // AggSourceURL
	YAML     string `json:"yaml,omitempty"`   // AggSourceYAML
	Links    string `json:"links,omitempty"`  // AggSourceLinks
	SubID    string `json:"subId,omitempty"`  // AggSourceSub
	Prefix   string `json:"prefix,omitempty"` // prepended to the names of this source's nodes
	Disabled bool   `json:"disabled,omitempty"`
}

// RenameRule replaces the matches of a regular expression in node names
// (Go regexp syntax; Replace may use $1 …).
type RenameRule struct {
	Pattern string `json:"pattern"`
	Replace string `json:"replace"`
}

// Group types of an AggregateGroup. The first four are Mihomo's; the
// last two are select groups whose first choice is REJECT or DIRECT.
const (
	AggTypeSelect      = "select"
	AggTypeURLTest     = "url-test"
	AggTypeFallback    = "fallback"
	AggTypeLoadBalance = "load-balance"
	AggTypeRejectFirst = "reject-first"
	AggTypeDirectFirst = "direct-first"
)

// AggregateGroup is a proxy group of an aggregate subscription: a module
// of the catalog (profile.AggregateModules) or one of the user's own
// (Custom). Its rules send traffic to it; the order of Groups is the
// order of the groups and of their rules.
type AggregateGroup struct {
	ID string `json:"id"`
	// Name overrides the catalog's group name ("" keeps it).
	Name string `json:"name,omitempty"`
	// Type overrides the catalog's group type ("" keeps it).
	Type string `json:"type,omitempty"`
	// Custom marks a group of the user's own: Name, Type, Rules and
	// Filter are all it has.
	Custom bool `json:"custom,omitempty"`
	// Rules are the rule sets of a custom group ("geosite:netflix",
	// "geoip:us").
	Rules []string `json:"rules,omitempty"`
	// Filter limits a custom group to the nodes whose name matches
	// (regexp, "" = every node).
	Filter string `json:"filter,omitempty"`
	// Extended adds the catalog module's extended rule sets (扩展规则集,
	// the 完整版 template's) to its own.
	Extended bool `json:"extended,omitempty"`
}

// AggregateRelay is a relay group (中转代理组): the nodes matching Landing
// connect through the group, a select over the nodes matching Relay
// (Mihomo dialer-proxy).
type AggregateRelay struct {
	Name    string `json:"name"`
	Relay   string `json:"relay"`   // regexp on node names: the relay nodes
	Landing string `json:"landing"` // regexp on node names: the nodes that go through the relay
}

// Kinds of an AggregateRule.
const (
	AggRuleDomain        = "DOMAIN"
	AggRuleDomainSuffix  = "DOMAIN-SUFFIX"
	AggRuleDomainKeyword = "DOMAIN-KEYWORD"
	AggRuleIPCIDR        = "IP-CIDR"
	AggRuleIPCIDR6       = "IP-CIDR6"
	AggRuleProcessName   = "PROCESS-NAME"
	// AggRuleGeoSite and AggRuleGeoIP name a rule set: rendered as
	// GEOSITE / GEOIP with the bundled databases, or as a remote
	// RULE-SET (AggregateSpec.RuleSource).
	AggRuleGeoSite = "GEOSITE"
	AggRuleGeoIP   = "GEOIP"
)

// AggregateRule is a rule the user added: traffic matching Type,Value goes
// to Target, the ID of a group of the spec, or DIRECT / REJECT.
type AggregateRule struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Target    string `json:"target"`
	NoResolve bool   `json:"noResolve,omitempty"`
}

// Where the rule sets of an aggregate subscription come from.
const (
	AggRulesGeo    = ""       // the GeoSite / GeoIP databases shipped with Lanflow (GEOSITE / GEOIP rules)
	AggRulesRemote = "remote" // rule-providers downloaded by the core from RuleBaseURL (.mrs)
)

// AggregateSpec is how an aggregate subscription is built.
type AggregateSpec struct {
	Version int               `json:"version,omitempty"`
	Sources []AggregateSource `json:"sources"`

	// Node management (节点管理). Include keeps only the nodes whose
	// original name matches one of its lines (regexps, empty = all);
	// Exclude then drops the ones matching one of its lines.
	Include string       `json:"include,omitempty"`
	Exclude string       `json:"exclude,omitempty"`
	Renames []RenameRule `json:"renames,omitempty"`
	// Hidden are final node names left out of the profile.
	Hidden []string `json:"hidden,omitempty"`
	// NodeOrder are final node names placed first, in this order.
	NodeOrder []string `json:"nodeOrder,omitempty"`
	// ListenBase > 0 adds a mixed listener per node, from this port on.
	ListenBase int `json:"listenBase,omitempty"`

	Relays []AggregateRelay `json:"relays,omitempty"`

	// Template is the template the groups were last set from
	// (profile.AggregateTemplates, or "my:<id>"); the quick mode marks it.
	Template string           `json:"template,omitempty"`
	Groups   []AggregateGroup `json:"groups,omitempty"`

	RuleSource  string `json:"ruleSource,omitempty"`  // AggRulesGeo or AggRulesRemote
	RuleBaseURL string `json:"ruleBaseUrl,omitempty"` // AggRulesRemote: "" = profile.DefaultRuleBaseURL

	Rules []AggregateRule `json:"rules,omitempty"`
	// RuleOrder is the order of every rule, by rule key
	// (profile.AggregateRuleKey); empty is the generated order. MATCH is
	// always last.
	RuleOrder []string `json:"ruleOrder,omitempty"`

	// Base is YAML merged at the top of the profile (ports, DNS …); keys
	// the generator writes (proxies, proxy-groups, rules, rule-providers,
	// listeners) are ignored.
	Base string `json:"base,omitempty"`

	// Version 1 fields, read only to upgrade an old spec.
	AutoGroup bool     `json:"autoGroup,omitempty"`
	Regions   []string `json:"regions,omitempty"`
	Modules   []string `json:"modules,omitempty"`
}

// Clone returns a deep copy.
func (s AggregateSpec) Clone() AggregateSpec {
	s.Sources = append([]AggregateSource(nil), s.Sources...)
	s.Renames = append([]RenameRule(nil), s.Renames...)
	s.Hidden = append([]string(nil), s.Hidden...)
	s.NodeOrder = append([]string(nil), s.NodeOrder...)
	s.Relays = append([]AggregateRelay(nil), s.Relays...)
	groups := make([]AggregateGroup, len(s.Groups))
	for i, g := range s.Groups {
		g.Rules = append([]string(nil), g.Rules...)
		groups[i] = g
	}
	if s.Groups != nil {
		s.Groups = groups
	}
	s.Rules = append([]AggregateRule(nil), s.Rules...)
	s.RuleOrder = append([]string(nil), s.RuleOrder...)
	s.Regions = append([]string(nil), s.Regions...)
	s.Modules = append([]string(nil), s.Modules...)
	return s
}

// AggregateTemplate is a template of the user's own (我的模板): the
// groups, rules and node processing of a spec, without its sources.
type AggregateTemplate struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Spec    AggregateSpec `json:"spec"`
	Created time.Time     `json:"created"`
}

// AggregateNode is one node of a built aggregate subscription.
type AggregateNode struct {
	Name   string // final name, after renaming and de-duplication
	Type   string // Mihomo proxy type
	Source string // AggregateSource.ID
	Hidden bool   // in AggregateSpec.Hidden: listed for the editor, not in the profile
	Port   int    // its listener (AggregateSpec.ListenBase), 0 if none
	Relay  string // the relay group it connects through, "" if none
}

// AggregateRuleView is one rule of a built profile, for the editor.
type AggregateRuleView struct {
	Key    string // profile.AggregateRuleKey: what RuleOrder refers to
	Line   string // the rule as written in the profile
	Label  string // what it matches, for display ("youtube", "example.com")
	Group  string // the group or policy it targets
	Custom bool   // one of AggregateSpec.Rules
}

// AggregateGroupView is one proxy group of a built profile, for the
// editor's visualisation.
type AggregateGroupView struct {
	ID      string // AggregateGroup.ID ("" for a relay group)
	Name    string
	Type    string   // the group type as configured (AggType…)
	Default string   // its first member: what it selects until changed
	Members int      // proxies and groups it offers
	Rules   []string // labels of the rules targeting it
}

// AggregateSourceReport says what one source contributed.
type AggregateSourceReport struct {
	ID       string
	Found    int    // nodes read from the source
	Kept     int    // after filtering
	Skipped  int    // lines or nodes that could not be used
	Error    string // the source failed entirely ("" if not)
	Upload   int64  // subscription-userinfo of a link source
	Download int64
	Total    int64
	Expire   time.Time
}

// AggregatePreview is the result of building an aggregate subscription
// without saving it (API.PreviewAggregate), for the editor.
type AggregatePreview struct {
	Nodes      []AggregateNode
	Sources    []AggregateSourceReport
	Groups     []string // generated proxy groups, in order
	GroupViews []AggregateGroupView
	RuleViews  []AggregateRuleView
	Rules      int // generated rules
	RuleSets   int // distinct rule sets used
	Warnings   []string
	YAML       string
}
