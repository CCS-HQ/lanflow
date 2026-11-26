package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"lanflow/internal/rpc"
	"lanflow/internal/rpc/rpctest"
)

func TestSearchTool(t *testing.T) {
	srv := rpctest.Start(t, func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		t.Errorf("search asked the user: %+v", c)
		return rpc.AnswerDeny, nil
	})
	s := newSession(t, srv)
	s.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": "claude-code"}})

	find := func(args map[string]any) SearchResult {
		t.Helper()
		text, isErr := s.tool("search", args)
		if isErr {
			t.Fatalf("search %v: %s", args, text)
		}
		var res SearchResult
		if err := json.Unmarshal([]byte(text), &res); err != nil {
			t.Fatalf("search %v: %v\n%s", args, err, text)
		}
		return res
	}
	has := func(res SearchResult, kind, name string) bool {
		for _, h := range res.Hits {
			if h.Kind == kind && strings.Contains(h.Name, name) {
				return true
			}
		}
		return false
	}

	res := find(map[string]any{"query": "香港"})
	if !has(res, "node", "香港 01") {
		t.Errorf("node not found: %+v", res)
	}
	res = find(map[string]any{"query": "patch settings"})
	if !has(res, "tool", "patch_settings") {
		t.Errorf("tool not found: %+v", res)
	}
	res = find(map[string]any{"query": "allowlan", "kinds": []string{"setting"}})
	if !has(res, "setting", "AllowLAN") {
		t.Errorf("setting not found: %+v", res)
	}
	for _, h := range res.Hits {
		if h.Kind != "setting" {
			t.Errorf("kinds ignored: %+v", h)
		}
	}
	// A field left at its default is not in the settings JSON, but is found.
	res = find(map[string]any{"query": "strictroute", "kinds": []string{"setting"}})
	if !has(res, "setting", "Advanced.tunStrictRoute") {
		t.Errorf("unset setting not found: %+v", res)
	}
	res = find(map[string]any{"query": "a", "limit": 1})
	if len(res.Truncated) == 0 {
		t.Errorf("limit 1 truncated nothing: %+v", res)
	}
	if text, isErr := s.tool("search", map[string]any{"query": "  "}); !isErr {
		t.Errorf("empty query accepted: %s", text)
	}
}

// Every tool a hit points to exists.
func TestSearchNextToolsExist(t *testing.T) {
	src := []string{"use_subscription / update_subscription", "save_custom_rule / delete_custom_rule / route_domain",
		"test_rule / set_rule_disabled", "update_rule_provider", "override_list / update_override_entry",
		"settings / patch_settings", "switch_node / test_group_delay", "switch_node / test_proxy_delay / route_domain"}
	for _, s := range src {
		for _, name := range strings.Split(s, " / ") {
			if _, ok := toolByName(name); !ok {
				t.Errorf("search points to %s, which is not a tool", name)
			}
		}
	}
}
