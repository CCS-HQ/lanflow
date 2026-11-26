package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"lanflow/internal/app/remote"
	"lanflow/internal/ctl"
	"lanflow/internal/model"
)

// search is the agent's counterpart of the window's global search (⌘K,
// view/palette.go): one query over the proxy groups and nodes, the
// subscriptions, the custom and runtime rules, the rule providers, the
// overrides, the settings fields (by their path in Settings, the names
// patch_settings takes) and the tools themselves, so that an agent finds
// where something lives before it reads or changes it. Only plain reads:
// connections and logs, which the user must approve, are left to their
// own tools.

// SearchHit is one match.
type SearchHit struct {
	Kind   string `json:"kind"`             // tool, group, node, subscription, custom_rule, rule, rule_provider, override, setting
	Name   string `json:"name"`             // what to pass on: the node or group name, the settings path, the tool name…
	Detail string `json:"detail,omitempty"` // type, value, target, description
	Next   string `json:"next,omitempty"`   // the tool that reads or changes it
}

// SearchResult is the result of search.
type SearchResult struct {
	Query     string      `json:"query"`
	Hits      []SearchHit `json:"hits"`
	Truncated []string    `json:"truncated,omitempty"` // kinds with more matches than shown
	Notes     []string    `json:"notes,omitempty"`
}

var searchKinds = []string{"tool", "group", "node", "subscription", "custom_rule", "rule", "rule_provider", "override", "setting"}

// searchPerKind caps the hits of each kind unless limit says otherwise.
const searchPerKind = 20

func searchTool() tool {
	return tool{Tool: Tool{Name: "search", Title: "Search everything in Lanflow",
		Description: "Find where something lives, as the window's global search (⌘K) does: matches the query (every word, case-insensitive) against tool names and descriptions, proxy groups and nodes (with type, delay, the groups they are in), subscriptions, custom rules, runtime rules, rule providers, overrides and every settings field by its path (e.g. Advanced.TUNStrictRoute, the names patch_settings takes) and value. Each hit names the tool to use next. Connections and logs are not searched (use connections / logs). Read-only.",
		InputSchema: obj(map[string]any{
			"query": str("Words to find, e.g. \"hong kong\", \"fake-ip\", \"ipv6\", \"openai\""),
			"kinds": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": searchKinds},
				"description": "Only these kinds (default: all)"},
			"limit": map[string]any{"type": "integer", "description": fmt.Sprintf("Most hits per kind (default %d)", searchPerKind)},
		}, "query"),
		Annotations: map[string]any{"readOnlyHint": true}},
		composite: runSearch}
}

func runSearch(ctx context.Context, c *remote.Client, a map[string]json.RawMessage) (any, error) {
	query := argStr(a, "query")
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, errors.New("query is required")
	}
	want := map[string]bool{}
	if v, ok := a["kinds"]; ok {
		var kinds []string
		if err := json.Unmarshal(v, &kinds); err != nil {
			return nil, errors.New("kinds must be an array of strings")
		}
		for _, k := range kinds {
			want[k] = true
		}
	}
	limit := searchPerKind
	if v, ok := a["limit"]; ok {
		_ = json.Unmarshal(v, &limit)
		limit = max(limit, 1)
	}
	on := func(kind string) bool { return len(want) == 0 || want[kind] }
	matches := func(parts ...string) bool {
		hay := strings.ToLower(strings.Join(parts, " "))
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return false
			}
		}
		return true
	}
	res := SearchResult{Query: query}
	counts := map[string]int{}
	hit := func(h SearchHit) {
		if counts[h.Kind] >= limit {
			if counts[h.Kind] == limit {
				res.Truncated = append(res.Truncated, h.Kind)
			}
			counts[h.Kind]++
			return
		}
		counts[h.Kind]++
		res.Hits = append(res.Hits, h)
	}
	note := func(what string, err error) {
		res.Notes = append(res.Notes, fmt.Sprintf("%s not searched: %v", what, err))
	}

	if on("tool") {
		for _, t := range Tools() {
			if matches(t.Name, t.Title, t.Description) {
				hit(SearchHit{Kind: "tool", Name: t.Name, Detail: firstSentence(t.Description)})
			}
		}
	}

	if on("group") || on("node") {
		var px model.ProxySnapshot
		if err := ctl.ReadContext(ctx, c, "Proxies", &px); err != nil {
			note("proxies", err)
		} else {
			searchProxies(px, matches, on, hit)
		}
	}

	if on("subscription") {
		var subs []model.Subscription
		if err := ctl.ReadContext(ctx, c, "Subscriptions", &subs); err != nil {
			note("subscriptions", err)
		}
		for _, s := range subs {
			if matches(s.Name, s.ID) {
				d := s.Source
				if s.Current {
					d += ", current"
				}
				hit(SearchHit{Kind: "subscription", Name: s.Name, Detail: d + ", id " + s.ID, Next: "use_subscription / update_subscription"})
			}
		}
	}

	if on("custom_rule") {
		var rules []model.CustomRule
		if err := ctl.ReadContext(ctx, c, "CustomRules", &rules); err != nil {
			note("custom rules", err)
		}
		for _, r := range rules {
			if matches(r.Type, r.Payload, r.Target) {
				hit(SearchHit{Kind: "custom_rule", Name: r.Type + "," + r.Payload + "," + r.Target, Detail: "id " + r.ID, Next: "save_custom_rule / delete_custom_rule / route_domain"})
			}
		}
	}

	if on("rule") {
		var rules []model.Rule
		if err := ctl.ReadContext(ctx, c, "Rules", &rules); err != nil {
			note("rules", err)
		}
		for _, r := range rules {
			if matches(r.Type, r.Payload, r.Proxy) {
				d := fmt.Sprintf("#%d", r.Index)
				if r.Disabled {
					d += ", disabled"
				}
				hit(SearchHit{Kind: "rule", Name: strings.Trim(r.Type+","+r.Payload+","+r.Proxy, ","), Detail: d, Next: "test_rule / set_rule_disabled"})
			}
		}
	}

	if on("rule_provider") {
		var provs []model.RuleProvider
		if err := ctl.ReadContext(ctx, c, "RuleProviders", &provs); err != nil {
			note("rule providers", err)
		}
		for _, p := range provs {
			if matches(p.Name, p.Behavior, p.VehicleType) {
				hit(SearchHit{Kind: "rule_provider", Name: p.Name, Detail: fmt.Sprintf("%s, %d entries", p.Behavior, p.Count), Next: "update_rule_provider"})
			}
		}
	}

	if on("override") {
		var list []model.Override
		if err := ctl.ReadContext(ctx, c, "OverrideList", &list); err != nil {
			note("overrides", err)
		}
		for _, o := range list {
			if matches(o.Name, o.ID, o.Text) {
				hit(SearchHit{Kind: "override", Name: o.Name, Detail: "id " + o.ID, Next: "override_list / update_override_entry"})
			}
		}
	}

	if on("setting") {
		var raw json.RawMessage
		if err := ctl.ReadContext(ctx, c, "Settings", &raw); err != nil {
			note("settings", err)
		} else {
			var v map[string]any
			_ = json.Unmarshal(raw, &v)
			// Every field of the type, not only those set: the settings
			// leave unset fields out, and those are what an agent looks
			// for most (Advanced.tunStrictRoute, say).
			for _, path := range settingPaths(reflect.TypeOf(model.Settings{}), "") {
				val := "(default, or not shown to agents)"
				if x, ok := lookupPath(v, path); ok {
					b, _ := json.Marshal(x)
					val = clipRunes(string(b), 160)
				}
				if matches(path, val) {
					hit(SearchHit{Kind: "setting", Name: path, Detail: val, Next: "settings / patch_settings"})
				}
			}
		}
	}
	if res.Hits == nil {
		res.Hits = []SearchHit{}
	}
	return res, nil
}

func searchProxies(px model.ProxySnapshot, matches func(...string) bool, on func(string) bool, hit func(SearchHit)) {
	groups := map[string]bool{}
	memberOf := map[string][]string{}
	for _, g := range px.Groups {
		groups[g.Name] = true
		for _, n := range g.All {
			memberOf[n] = append(memberOf[n], g.Name)
		}
	}
	if on("group") {
		for _, g := range px.Groups {
			if matches(g.Name, string(g.Type)) {
				d := string(g.Type)
				if g.Now != "" {
					d += ", now " + g.Now
				}
				if g.Name == px.MainGroup {
					d += ", main group"
				}
				hit(SearchHit{Kind: "group", Name: g.Name, Detail: fmt.Sprintf("%s, %d members", d, len(g.All)), Next: "switch_node / test_group_delay"})
			}
		}
	}
	if !on("node") {
		return
	}
	names := make([]string, 0, len(px.Proxies))
	for n := range px.Proxies {
		if !groups[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p := px.Proxies[n]
		if !matches(n, p.Type, p.Provider) {
			continue
		}
		d := p.Type
		switch {
		case p.Delay > 0:
			d += fmt.Sprintf(", %d ms", p.Delay)
		case p.Delay < 0:
			d += ", delay test failed"
		}
		if in := memberOf[n]; len(in) > 0 {
			d += ", in " + strings.Join(in, " / ")
		}
		hit(SearchHit{Kind: "node", Name: n, Detail: d, Next: "switch_node / test_proxy_delay / route_domain"})
	}
}

// settingPaths lists the leaves of a settings type by their path in the
// JSON the settings tool returns ("Advanced.tunStrictRoute"): structs are
// walked, everything else (lists, maps, scalars) is one leaf.
func settingPaths(t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag == "-" {
			continue
		} else if tag != "" {
			name = tag
		}
		if f.Anonymous && f.Tag.Get("json") == "" {
			out = append(out, settingPaths(f.Type, prefix)...)
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Time{}) {
			out = append(out, settingPaths(ft, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// lookupPath reads path from decoded JSON objects.
func lookupPath(v map[string]any, path string) (any, bool) {
	var cur any = v
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	return clipRunes(s, 160)
}
