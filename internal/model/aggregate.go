package model

import "time"

// SourceAggregate is the Subscription.Source of an aggregate subscription
// (docs/aggregate.md): Lanflow builds its content itself from the
// AggregateSpec stored with it, merging the nodes of several sources and
// generating the proxy groups and rules.
const SourceAggregate = "aggregate"

// Kinds of an aggregate source.
const (
	AggSourceURL   = "url"   // a subscription link: Clash/Mihomo YAML or share links (raw / Base64)
	AggSourceLinks = "links" // share links typed or pasted by the user, one per line
	AggSourceSub   = "sub"   // the stored content of another subscription of this Lanflow
)

// AggregateSource is one place the nodes of an aggregate subscription come from.
type AggregateSource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`   // label shown in the editor
	URL      string `json:"url,omitempty"`    // AggSourceURL
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

// AggregateSpec is how an aggregate subscription is built.
type AggregateSpec struct {
	Sources []AggregateSource `json:"sources"`
	// Include keeps only the nodes whose original name matches (regexp,
	// empty = all); Exclude then drops the matching ones.
	Include string       `json:"include,omitempty"`
	Exclude string       `json:"exclude,omitempty"`
	Renames []RenameRule `json:"renames,omitempty"`
	// AutoGroup adds the url-test group 自动选择 over every node.
	AutoGroup bool `json:"autoGroup,omitempty"`
	// Regions are the IDs of the region groups to generate
	// (profile.AggregateRegions); a region with no node is left out.
	Regions []string `json:"regions,omitempty"`
	// Modules are the IDs of the routing modules to generate
	// (profile.AggregateModules), in catalog order.
	Modules []string `json:"modules,omitempty"`
}

// Clone returns a deep copy.
func (s AggregateSpec) Clone() AggregateSpec {
	s.Sources = append([]AggregateSource(nil), s.Sources...)
	s.Renames = append([]RenameRule(nil), s.Renames...)
	s.Regions = append([]string(nil), s.Regions...)
	s.Modules = append([]string(nil), s.Modules...)
	return s
}

// AggregateNode is one node of a built aggregate subscription.
type AggregateNode struct {
	Name   string // final name, after renaming and de-duplication
	Type   string // Mihomo proxy type
	Source string // AggregateSource.ID
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
	Nodes    []AggregateNode
	Sources  []AggregateSourceReport
	Groups   []string // generated proxy groups, in order
	Rules    int      // generated rules
	Warnings []string
	YAML     string
}
