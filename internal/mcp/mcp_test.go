package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
	"lanflow/internal/rpc/rpctest"
)

func TestToolName(t *testing.T) {
	for in, want := range map[string]string{
		"SelectProxy": "select_proxy", "DNSQuery": "dns_query", "ImportSubscriptionURL": "import_subscription_url",
		"TUNHelperInstalled": "tun_helper_installed", "ProbeIP": "probe_ip", "Status": "status",
		"TestWebDAV": "test_webdav", "ATrustReference": "atrust_reference",
	} {
		if got := ToolName(in); got != want {
			t.Errorf("ToolName(%s) = %s, want %s", in, got, want)
		}
	}
}

// TestToolList: tools come from the manifest filtered by level; names
// are unique and every schema is an object.
func TestToolList(t *testing.T) {
	tools := Tools()
	seen := map[string]bool{}
	for _, tl := range tools {
		if seen[tl.Name] {
			t.Errorf("duplicate tool %s", tl.Name)
		}
		seen[tl.Name] = true
		if tl.InputSchema["type"] != "object" {
			t.Errorf("%s: schema type %v", tl.Name, tl.InputSchema["type"])
		}
		if _, err := json.Marshal(tl); err != nil {
			t.Errorf("%s: %v", tl.Name, err)
		}
	}
	for _, m := range rpc.Manifest() {
		if (m.Level == rpc.LevelDanger || m.Level == rpc.LevelUI) && seen[ToolName(m.Name)] {
			t.Errorf("%s (%s) is offered", m.Name, m.Level)
		}
	}
	for _, want := range []string{"status", "proxies", "select_proxy", "test_rule", "patch_settings", "undo",
		"route_domain", "diagnose_site", "switch_node", "diagnose"} {
		if !seen[want] {
			t.Errorf("tool %s missing", want)
		}
	}
	if !seen["build_feedback"] {
		t.Error("tool build_feedback missing: agents may draft a report")
	}
	for _, never := range []string{"install_tun_helper", "delete_subscription", "save_settings", "take_notices", "shutdown", "export_settings",
		"send_feedback", "set_feedback_token", "save_feedback"} {
		if seen[never] {
			t.Errorf("tool %s must not be offered", never)
		}
	}
}

type session struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
	id  int
}

func newSession(t *testing.T, s *rpctest.Server) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := &Server{Version: "test", Dial: func(ctx context.Context, client string) (*remote.Client, error) {
		return s.Dial(client, false)
	}}
	go func() { _ = srv.Serve(context.Background(), inR, outW); outW.Close() }()
	t.Cleanup(func() { inW.Close() })
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &session{t: t, in: inW, out: sc}
}

func (s *session) call(method string, params any) map[string]any {
	s.t.Helper()
	s.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method, "params": params})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() {
		if !s.out.Scan() {
			done <- nil
			return
		}
		var m map[string]any
		_ = json.Unmarshal(s.out.Bytes(), &m)
		done <- m
	}()
	select {
	case m := <-done:
		if m == nil {
			s.t.Fatal("server closed")
		}
		if m["error"] != nil {
			s.t.Fatalf("%s: %v", method, m["error"])
		}
		return m["result"].(map[string]any)
	case <-time.After(15 * time.Second):
		s.t.Fatalf("%s: no answer", method)
	}
	return nil
}

func (s *session) tool(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	res := s.call("tools/call", map[string]any{"name": name, "arguments": args})
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"] == true
}

func TestRouteDomainEndToEnd(t *testing.T) {
	asked := 0
	srv := rpctest.Start(t, func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		asked++
		if c.Agent != "claude-code" || c.Kind != rpc.LevelWrite {
			t.Errorf("consent = %+v", c)
		}
		return rpc.AnswerAlways, nil
	})
	s := newSession(t, srv)
	init := s.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": "claude-code"}})
	if init["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize = %v", init)
	}
	list := s.call("tools/list", map[string]any{})
	if len(list["tools"].([]any)) < 20 {
		t.Fatalf("only %d tools", len(list["tools"].([]any)))
	}

	// Preview first: nothing changes, nobody is asked.
	text, isErr := s.tool("route_domain", map[string]any{"domain": "https://chat.example.ai/path", "target": "香港 01", "dry_run": true})
	if isErr {
		t.Fatal(text)
	}
	var dry RouteResult
	if err := json.Unmarshal([]byte(text), &dry); err != nil || dry.Applied || len(dry.Diff) == 0 || !strings.HasPrefix(dry.Diff[0], "+ DOMAIN-SUFFIX,chat.example.ai,香港 01") {
		t.Fatalf("dry run: %s", text)
	}
	if asked != 0 {
		t.Fatal("dry run asked for consent")
	}

	// Apply: asks once, adds the rule, verifies it.
	text, isErr = s.tool("route_domain", map[string]any{"domain": "chat.example.ai", "target": "香港 01"})
	if isErr {
		t.Fatal(text)
	}
	var res RouteResult
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Applied || !res.Verified || res.Exit != "香港 01" || res.Delay == nil {
		t.Fatalf("route_domain: %s", text)
	}
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
	rules := srv.Fake.CustomRules()
	if len(rules) == 0 || rules[0].Payload != "chat.example.ai" || rules[0].Target != "香港 01" {
		t.Fatalf("custom rules = %+v", rules)
	}

	// The other composites.
	if text, isErr = s.tool("diagnose_site", map[string]any{"host": "chat.example.ai"}); isErr || !strings.Contains(text, "香港 01") {
		t.Fatalf("diagnose_site: %s", text)
	}
	if text, isErr = s.tool("switch_node", map[string]any{"group": "节点选择"}); isErr || !strings.Contains(text, `"selected"`) {
		t.Fatalf("switch_node: %s", text)
	}

	// Undo twice: the selection, then the rule.
	if text, isErr = s.tool("undo", nil); isErr || !strings.Contains(text, "SelectProxy") {
		t.Fatalf("undo: %s", text)
	}
	if text, isErr = s.tool("undo", nil); isErr || !strings.Contains(text, "SaveCustomRule") {
		t.Fatalf("undo: %s", text)
	}
	for _, r := range srv.Fake.CustomRules() {
		if r.Payload == "chat.example.ai" {
			t.Fatal("undo left the rule")
		}
	}

	// Dangerous methods are not tools, and calling them by name fails.
	if text, isErr = s.tool("status", nil); isErr || !strings.Contains(text, "Core") {
		t.Fatalf("status: %s", text)
	}
}

// TestToolHints: tools that delete, stop or empty something, and the
// high-impact writes, are declared destructive; reads are read-only; no
// tool takes a local file of the agent's choice except apply_config_file,
// which the server keeps to the sync folder.
func TestToolHints(t *testing.T) {
	byName := map[string]Tool{}
	for _, tl := range Tools() {
		byName[tl.Name] = tl
	}
	for _, name := range []string{"stop_core", "delete_custom_rule", "delete_override_entry", "clear_logs", "close_all_connections",
		"close_connection", "clear_app_logs", "clear_capture_flows", "flush_dns_cache", "save_override", "use_subscription",
		"import_subscription_url", "set_system_proxy", "set_tun", "restart_core", "apply_config_file"} {
		tl, ok := byName[name]
		if !ok {
			t.Errorf("tool %s missing", name)
			continue
		}
		if tl.Annotations["destructiveHint"] != true || tl.Annotations["readOnlyHint"] != false {
			t.Errorf("%s: annotations %v, want destructive", name, tl.Annotations)
		}
	}
	for _, name := range []string{"select_proxy", "set_mode", "save_custom_rule", "patch_settings", "undo"} {
		if tl := byName[name]; tl.Annotations["destructiveHint"] != false {
			t.Errorf("%s: annotations %v, want not destructive", name, tl.Annotations)
		}
	}
	for _, name := range []string{"status", "proxies", "connections", "test_rule"} {
		if tl := byName[name]; tl.Annotations["readOnlyHint"] != true {
			t.Errorf("%s: annotations %v, want read-only", name, tl.Annotations)
		}
	}
	for _, name := range []string{"stop_core", "import_subscription_url", "save_override", "set_tun"} {
		if !strings.Contains(byName[name].Description, "high-impact") {
			t.Errorf("%s: the description does not say it is confirmed every session: %s", name, byName[name].Description)
		}
	}
	if strings.Contains(byName["select_proxy"].Description, "high-impact") {
		t.Error("select_proxy is described as high-impact")
	}
	for _, never := range []string{"import_subscription_file", "import_atrust_config"} {
		if _, ok := byName[never]; ok {
			t.Errorf("tool %s reads a local file of the agent's choice: it must not be offered", never)
		}
	}
}

func TestClipRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{{"abc", 5, "abc"}, {"abcdef", 3, "abc"}, {"节点选择", 4, "节"}, {"节点选择", 6, "节点"}, {"a节", 2, "a"}, {"😀x", 3, ""}} {
		if got := clipRunes(c.in, c.n); got != c.want {
			t.Errorf("clipRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
	// A long client name is cut at a character, not inside one.
	s := &Server{}
	s.initialize(json.RawMessage(`{"clientInfo":{"name":"` + strings.Repeat("澜", 30) + `"}}`))
	if s.name != strings.Repeat("澜", 21) {
		t.Errorf("name = %q", s.name)
	}
}

// TestHighImpactAndStaleErrors: an "always" for writes does not cover a
// high-impact tool, and a refused call does not fail the composite tools
// that follow in the session.
func TestHighImpactAndStaleErrors(t *testing.T) {
	var kinds []rpc.Level
	srv := rpctest.Start(t, func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		kinds = append(kinds, c.Kind)
		if c.Kind == rpc.LevelHigh {
			return rpc.AnswerDeny, nil
		}
		return rpc.AnswerAlways, nil
	})
	s := newSession(t, srv)
	s.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": "claude-code"}})
	if text, isErr := s.tool("set_mode", map[string]any{"m": "global"}); isErr {
		t.Fatal(text)
	}
	text, isErr := s.tool("stop_core", nil)
	if !isErr || !strings.Contains(text, "the user did not approve") {
		t.Fatalf("stop_core: %s", text)
	}
	if len(kinds) != 2 || kinds[0] != rpc.LevelWrite || kinds[1] != rpc.LevelHigh {
		t.Fatalf("asked %v", kinds)
	}
	if srv.Fake.Status().Core != "running" {
		t.Fatalf("the core is %s after a refused stop_core", srv.Fake.Status().Core)
	}
	// The refusal is over: these read the state anew.
	if text, isErr = s.tool("diagnose", nil); isErr {
		t.Fatalf("diagnose after a refused call: %s", text)
	}
	if text, isErr = s.tool("switch_node", map[string]any{"group": "节点选择", "node": "香港 01"}); isErr || !strings.Contains(text, `"selected": "香港 01"`) {
		t.Fatalf("switch_node after a refused call: %s", text)
	}
	if text, isErr = s.tool("route_domain", map[string]any{"domain": "x.example.org", "target": "DIRECT", "dry_run": true}); isErr {
		t.Fatalf("route_domain after a refused call: %s", text)
	}
	// A file outside the sync folder is refused by the server.
	if text, isErr = s.tool("apply_config_file", map[string]any{"path": "/etc/hosts", "dryRun": true}); !isErr || !strings.Contains(text, "permission denied") {
		t.Fatalf("apply_config_file /etc/hosts: %s", text)
	}
}

// TestCancelled: notifications/cancelled ends a call that waits (here for
// the user's approval); no response is sent for it.
func TestCancelled(t *testing.T) {
	asked := make(chan struct{}, 1)
	srv := rpctest.Start(t, func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		asked <- struct{}{}
		<-ctx.Done() // the user never answers
		return "", ctx.Err()
	})
	s := newSession(t, srv)
	s.call("initialize", map[string]any{"clientInfo": map[string]any{"name": "t"}})
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "tools/call", "params": map[string]any{"name": "set_mode", "arguments": map[string]any{"m": "global"}}})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the user was not asked")
	}
	b, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 99}})
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	// The next answer is the ping's: the cancelled call sent none.
	s.id = 100
	start := time.Now()
	if res := s.call("ping", nil); len(res) != 0 {
		t.Fatalf("ping = %v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("ping answered after %s", d)
	}
}
