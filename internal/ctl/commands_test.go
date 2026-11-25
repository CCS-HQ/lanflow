package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
	"lanflow/internal/rpc/rpctest"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want opts
		err  string
	}{
		{"defaults", []string{"status"}, opts{format: "json", timeout: 120 * time.Second, args: []string{"status"}}, ""},
		{"key=value", []string{"sub", "add", "x", "--name=我的", "--format=table", "--timeout=5"},
			opts{format: "table", timeout: 5 * time.Second, name: "我的", args: []string{"sub", "add", "x"}}, ""},
		{"key value", []string{"--format", "table", "--name", "a b", "sub", "list"},
			opts{format: "table", timeout: 120 * time.Second, name: "a b", args: []string{"sub", "list"}}, ""},
		{"flags", []string{"--reveal", "--dry-run", "--all", "--use", "--run", "diag"},
			opts{format: "json", timeout: 120 * time.Second, reveal: true, dryRun: true, all: true, use: true, run: true, args: []string{"diag"}}, ""},
		{"help", []string{"status", "--help"}, opts{format: "json", timeout: 120 * time.Second, args: []string{"help", "status"}}, ""},
		{"a lone dash is an argument", []string{"sub", "add", "-"}, opts{format: "json", timeout: 120 * time.Second, args: []string{"sub", "add", "-"}}, ""},
		{"missing value", []string{"status", "--format"}, opts{}, "--format 需要一个值"},
		{"bad format", []string{"--format", "yaml"}, opts{}, "json 或 table"},
		{"bad timeout", []string{"--timeout", "0"}, opts{}, "正整数"},
		{"timeout not a number", []string{"--timeout=soon"}, opts{}, "正整数"},
		{"unknown option", []string{"status", "--frobnicate"}, opts{}, "未知选项 --frobnicate"},
	}
	for _, c := range cases {
		got, err := parseArgs(c.args)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, %v\nwant %+v", c.name, got, err, c.want)
		}
	}
}

func TestPathPatchAndLookup(t *testing.T) {
	if got, want := PathPatch("DNS.TakeOver", true), map[string]any{"DNS": map[string]any{"TakeOver": true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("PathPatch = %v", got)
	}
	if got, want := PathPatch("Mode", "rule"), map[string]any{"Mode": "rule"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PathPatch = %v", got)
	}
	v := map[string]any{"DNS": map[string]any{"TakeOver": true, "Nameserver": []any{"a"}}, "MixedPort": 7890.0}
	for path, want := range map[string]any{"dns.takeover": true, "MixedPort": 7890.0, "DNS.Nameserver": []any{"a"}} {
		if got, ok := lookupPath(v, path); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("lookupPath(%s) = %v, %v", path, got, ok)
		}
	}
	for _, path := range []string{"Nope", "DNS.Nope", "MixedPort.X"} {
		if _, ok := lookupPath(v, path); ok {
			t.Errorf("lookupPath(%s) found something", path)
		}
	}
}

func TestJSONOrString(t *testing.T) {
	for in, want := range map[string]any{"7999": 7999.0, "true": true, `"x"`: "x", "rule": "rule", `{"a":1}`: map[string]any{"a": 1.0}, "香港 01": "香港 01"} {
		if got := jsonOrString(in); !reflect.DeepEqual(got, want) {
			t.Errorf("jsonOrString(%s) = %#v, want %#v", in, got, want)
		}
	}
}

func TestCleanHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://Chat.Example.ai/path?q=1": "chat.example.ai", "example.com:443": "example.com", " example.com. ": "example.com",
		"http://[::1]:8080/x": "::1", "1.2.3.4": "1.2.3.4", "example.com#frag": "example.com", "": "",
	} {
		if got := CleanHost(in); got != want {
			t.Errorf("CleanHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteKV(t *testing.T) {
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 0, 4, 1, ' ', 0)
	writeKV(tw, "", map[string]any{"b": []any{}, "a": map[string]any{"y": 1.0, "x": "s"}, "c": []any{true, nil}})
	tw.Flush()
	want := "a.x s\na.y 1\nb   []\nc.0 true\nc.1 null\n"
	if b.String() != want {
		t.Errorf("writeKV:\n%s\nwant:\n%s", b.String(), want)
	}
	b.Reset()
	writeKV(&b, "", "plain")
	if b.String() != "plain\n" {
		t.Errorf("scalar: %q", b.String())
	}
}

// runIn is run with the given standard input.
func runIn(t *testing.T, s *rpctest.Server, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(args, Env{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(stdin), Version: "test",
		Dial: func(_ context.Context, reveal bool) (*remote.Client, error) { return s.Dial("ctl", reveal) }})
	return out.String(), errb.String(), code
}

func TestSubCommands(t *testing.T) {
	s := rpctest.Start(t, nil)
	n := len(s.Fake.Subscriptions())

	// add: the link as an argument, never echoed back.
	out, errs, code := run(t, s, "sub", "add", "https://sub.example.com/link/abcdef", "--name", "新订阅")
	if code != 0 {
		t.Fatalf("sub add: %d %s", code, errs)
	}
	if strings.Contains(out, "abcdef") || !strings.Contains(out, "新订阅") {
		t.Fatalf("sub add output: %s", out)
	}
	// add -: the link from standard input (not in the shell history or ps).
	out, errs, code = runIn(t, s, "  https://sub.example.com/link/fromstdin  \nignored\n", "sub", "add", "-", "--name", "管道")
	if code != 0 || strings.Contains(out, "fromstdin") {
		t.Fatalf("sub add -: %d %s %s", code, out, errs)
	}
	subs := s.Fake.Subscriptions()
	if len(subs) != n+2 {
		t.Fatalf("%d subscriptions, want %d", len(subs), n+2)
	}
	found := false
	for _, sub := range subs {
		found = found || (sub.Name == "管道" && sub.URL == "https://sub.example.com/link/fromstdin")
	}
	if !found {
		t.Fatalf("the link from stdin was not imported as given: %+v", subs)
	}
	if _, errs, code = runIn(t, s, "\n", "sub", "add", "-"); code != ExitError || !strings.Contains(errs, "标准输入里没有订阅链接") {
		t.Fatalf("sub add - with empty input: %d %s", code, errs)
	}
	if _, _, code = run(t, s, "sub", "add"); code != ExitUsage {
		t.Fatalf("sub add without a link: %d", code)
	}
	if _, errs, code = run(t, s, "sub", "add", "ftp://x"); code != ExitError || errs == "" {
		t.Fatalf("sub add with a bad link: %d %s", code, errs)
	}

	// use: by name and by id; an unknown one fails.
	if out, errs, code = run(t, s, "sub", "use", "新订阅"); code != 0 || !strings.Contains(out, "新订阅") {
		t.Fatalf("sub use: %d %s %s", code, out, errs)
	}
	cur := s.Fake.Status().CurrentSubscription
	if _, errs, code = run(t, s, "sub", "use", cur); code != 0 {
		t.Fatalf("sub use <id>: %s", errs)
	}
	if _, errs, code = run(t, s, "sub", "use", "没有这个"); code != ExitError || !strings.Contains(errs, "找不到订阅") {
		t.Fatalf("sub use unknown: %d %s", code, errs)
	}

	// update: the current one by default, one by name, all.
	out, errs, code = run(t, s, "sub", "update")
	if code != 0 || !strings.Contains(out, cur) {
		t.Fatalf("sub update: %d %s %s", code, out, errs)
	}
	if _, errs, code = run(t, s, "sub", "update", "新订阅"); code != 0 {
		t.Fatalf("sub update <name>: %s", errs)
	}
	// --all reaches every subscription: the fake's second one fails.
	if _, errs, code = run(t, s, "sub", "update", "--all"); code != ExitError || !strings.Contains(errs, "更新失败") {
		t.Fatalf("sub update --all: %d %s", code, errs)
	}
	if out, _, code = run(t, s, "sub", "list", "--format", "table"); code != 0 || !strings.Contains(out, "新订阅") || !strings.Contains(out, "名称") {
		t.Fatalf("sub list as a table: %d %s", code, out)
	}
	if _, _, code = run(t, s, "sub", "frobnicate"); code != ExitUsage {
		t.Fatalf("unknown sub command: %d", code)
	}
}

func TestRuleCallAuditMethods(t *testing.T) {
	s := rpctest.Start(t, nil)
	n := len(s.Fake.CustomRules())
	out, errs, code := run(t, s, "rule", "add", "domain-suffix", "a.example.org", "DIRECT")
	if code != 0 || !strings.Contains(out, `"applied": true`) || len(s.Fake.CustomRules()) != n+1 {
		t.Fatalf("rule add: %d %s %s", code, out, errs)
	}
	if _, _, code = run(t, s, "rule", "add", "GEOIP", "CN", "DIRECT"); code != ExitUsage {
		t.Fatalf("rule add with an unknown type: %d", code)
	}
	if _, _, code = run(t, s, "rule", "add", "DOMAIN", "x"); code != ExitUsage {
		t.Fatalf("rule add without a target: %d", code)
	}
	if out, _, code = run(t, s, "rule", "test", "a.example.org"); code != 0 || !strings.Contains(out, "DIRECT") {
		t.Fatalf("rule test: %d %s", code, out)
	}
	if out, _, code = run(t, s, "rule", "list"); code != 0 || !strings.Contains(out, "a.example.org") {
		t.Fatalf("rule list: %d %s", code, out)
	}

	// call: JSON arguments, strings as they are; errors of the server.
	if out, errs, code = run(t, s, "call", "SetMode", "global"); code != 0 {
		t.Fatalf("call SetMode: %d %s %s", code, out, errs)
	}
	if string(s.Fake.Settings().Mode) != "global" && string(s.Fake.Status().Mode) != "global" {
		t.Fatal("call SetMode did not apply")
	}
	if out, _, code = run(t, s, "call", "TestRule", `"a.example.org"`); code != 0 || !strings.Contains(out, "DIRECT") {
		t.Fatalf("call TestRule: %d %s", code, out)
	}
	if _, errs, code = run(t, s, "call", "NoSuchMethod"); code != ExitError || !strings.Contains(errs, "没有这个方法") {
		t.Fatalf("call of an unknown method: %d %s", code, errs)
	}
	if _, errs, code = run(t, s, "call", "SetMode"); code != ExitError || !strings.Contains(errs, "参数") {
		t.Fatalf("call with too few arguments: %d %s", code, errs)
	}
	if _, errs, code = run(t, s, "call", "TakeNotices"); code != ExitError || !strings.Contains(errs, "只供澜流界面调用") {
		t.Fatalf("call of a window method: %d %s", code, errs)
	}
	if _, _, code = run(t, s, "call"); code != ExitUsage {
		t.Fatalf("call without a method: %d", code)
	}

	// audit: the writes above, as JSON and as a table; a count.
	out, _, code = run(t, s, "audit", "2")
	var es []rpc.AuditEntry
	if err := json.Unmarshal([]byte(out), &es); err != nil || code != 0 || len(es) != 2 {
		t.Fatalf("audit 2: %d %v %s", code, err, out)
	}
	if out, _, _ = run(t, s, "audit", "--format", "table"); !strings.Contains(out, "SaveCustomRule") || !strings.Contains(out, "ctl") {
		t.Fatalf("audit table:\n%s", out)
	}
	if _, _, code = run(t, s, "audit", "many"); code != ExitUsage {
		t.Fatalf("audit with a bad count: %d", code)
	}

	// methods, with their levels.
	if out, _, code = run(t, s, "methods", "--format", "table"); code != 0 || !strings.Contains(out, "InstallTUNHelper") || !strings.Contains(out, "danger") {
		t.Fatalf("methods:\n%s", out)
	}
}

func TestUsageErrors(t *testing.T) {
	s := rpctest.Start(t, nil)
	for _, args := range [][]string{
		{}, {"frobnicate"}, {"proxies", "select", "组"}, {"proxies", "frobnicate"}, {"mode", "set"}, {"mode"},
		{"config", "set", "MixedPort"}, {"config", "apply"}, {"config", "frobnicate"}, {"rule", "test"},
		{"status", "--format", "xml"}, {"status", "--nope"},
	} {
		if _, errs, code := run(t, s, args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, ExitUsage, errs)
		}
	}
	if out, _, code := run(t, s, "help"); code != ExitOK || !strings.Contains(out, "用法") {
		t.Errorf("help: %d", code)
	}
	if _, errs, code := run(t, s, "proxies", "list", "没有这个组"); code != ExitError || !strings.Contains(errs, "找不到代理组") {
		t.Errorf("unknown group: %d %s", code, errs)
	}
}

func TestConfigApplyFromStdin(t *testing.T) {
	s := rpctest.Start(t, nil)
	out, errs, code := runIn(t, s, `{"MixedPort": 7999, "DNS": {"TakeOver": true}}`, "config", "apply", "-", "--format", "table")
	if code != 0 || !strings.Contains(out, "applied: true") || !strings.Contains(out, "DNS, MixedPort") {
		t.Fatalf("config apply -: %d %s %s", code, out, errs)
	}
	if st := s.Fake.Settings(); st.MixedPort != 7999 || !st.DNS.TakeOver {
		t.Fatal("patch not applied")
	}
	if _, errs, code = runIn(t, s, `[1]`, "config", "apply", "-"); code != ExitError || !strings.Contains(errs, "JSON 对象") {
		t.Fatalf("a patch that is not an object: %d %s", code, errs)
	}
	if _, errs, code = run(t, s, "config", "get", "Nope"); code != ExitError || !strings.Contains(errs, "没有设置项") {
		t.Fatalf("config get of an unknown field: %d %s", code, errs)
	}
}

// TestDiagnoseSiteFindings: the findings follow the state.
func TestDiagnoseSiteFindings(t *testing.T) {
	s := rpctest.Start(t, nil)
	c, err := s.Dial("ctl", false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := s.Fake.StopCore(); err != nil {
		t.Fatal(err)
	}
	rep, err := DiagnoseSite(c, "https://www.example.com/x")
	if err != nil || rep.Host != "www.example.com" {
		t.Fatalf("report %+v, %v", rep, err)
	}
	if !strings.Contains(strings.Join(rep.Findings, "\n"), "内核没有运行") {
		t.Fatalf("findings with the core stopped: %v", rep.Findings)
	}
	// An earlier failed call of the session does not fail the diagnosis.
	if err := c.SelectProxy("没有这个组", "x"); err == nil {
		t.Fatal("SelectProxy of an unknown group succeeded")
	}
	if _, err := DiagnoseSite(c, "example.com"); err != nil {
		t.Fatalf("DiagnoseSite after a failed call: %v", err)
	}
	if _, err := Diagnose(c, false); err != nil {
		t.Fatalf("Diagnose after a failed call: %v", err)
	}
}

// TestScriptWritesAsk: ctl run by a script or an agent (no terminal) needs
// the user's consent for a write, like MCP, and each high-impact command
// is confirmed.
func TestScriptWritesAsk(t *testing.T) {
	var asked []rpc.Consent
	answer := rpc.AnswerDeny
	s := rpctest.Start(t, func(_ context.Context, c rpc.Consent) (rpc.Answer, error) {
		asked = append(asked, c)
		return answer, nil
	})
	script := func(args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := Main(args, Env{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""), Version: "test",
			Dial: func(ctx context.Context, _ bool) (*remote.Client, error) { return s.DialScript() }})
		return out.String(), errb.String(), code
	}
	before := s.Fake.Status().Mode
	if _, errs, code := script("mode", "set", "global"); code != ExitError || !strings.Contains(errs, "拒绝") {
		t.Fatalf("refused write: %d %s", code, errs)
	}
	if len(asked) != 1 || asked[0].Client != "ctl" || asked[0].Kind != rpc.LevelWrite || s.Fake.Status().Mode != before {
		t.Fatalf("asked %+v, mode %v", asked, s.Fake.Status().Mode)
	}
	// Reads need nothing.
	if _, errs, code := script("status"); code != 0 || len(asked) != 1 {
		t.Fatalf("status: %d %s, asked %d", code, errs, len(asked))
	}
	// Allowed for good (after the refusal is revoked in the settings):
	// small writes pass, high-impact commands are confirmed each time.
	st := s.Fake.Settings()
	st.AgentGrants = nil
	if err := s.Fake.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	answer = rpc.AnswerAlways
	if _, errs, code := script("mode", "set", "global"); code != 0 {
		t.Fatalf("allowed write: %s", errs)
	}
	if _, errs, code := script("mode", "set", "rule"); code != 0 || len(asked) != 2 {
		t.Fatalf("after always: %s, asked %d", errs, len(asked))
	}
	// A high-impact command is asked about apart from the small writes.
	// Answered "once" it is asked again at every run (each run is a
	// connection); answered "always" the script's later runs pass
	// (rpc.CtlHighGrant), so an unattended script can be allowed for good.
	answer = rpc.AnswerOnce
	for i := 0; i < 2; i++ {
		if _, errs, code := script("call", "StopCore"); code != 0 {
			t.Fatalf("StopCore: %s", errs)
		}
	}
	if len(asked) != 4 || asked[3].Kind != rpc.LevelHigh || asked[3].Method != "StopCore" {
		t.Fatalf("asked %d times, last %+v", len(asked), asked[len(asked)-1])
	}
	answer = rpc.AnswerAlways
	for i := 0; i < 3; i++ {
		if _, errs, code := script("call", "StopCore"); code != 0 {
			t.Fatalf("StopCore: %s", errs)
		}
	}
	if len(asked) != 5 {
		t.Fatalf("after always for high-impact writes: asked %d times, want 5", len(asked))
	}
	// At a terminal (run) nothing is asked.
	if _, errs, code := run(t, s, "call", "StartCore"); code != 0 || len(asked) != 5 {
		t.Fatalf("at a terminal: %s, asked %d", errs, len(asked))
	}
}

// TestNotATerminal: under go test the standard streams are not a
// terminal on both ends, so a real run from here counts as a script.
func TestNotATerminal(t *testing.T) {
	r, w, err := newPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(w) {
		t.Fatal("a pipe counts as a terminal")
	}
	if f := devNull(t); f != nil && isTerminal(f) {
		t.Fatal("the null device counts as a terminal")
	}
}

func TestChainsListSetUndo(t *testing.T) {
	s := rpctest.Start(t, nil)
	f := filepath.Join(t.TempDir(), "chains.json")
	if err := os.WriteFile(f, []byte(`[{"name":"中转","hops":["香港 01","东京 03"],"enabled":true}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errs, code := run(t, s, "chains", "set", f)
	if code != 0 || !strings.Contains(out, "中转") || len(s.Fake.ProxyChains()) != 1 {
		t.Fatalf("chains set: %d %s %s", code, out, errs)
	}
	if out, _, code = run(t, s, "chains", "list", "--format", "table"); code != 0 || !strings.Contains(out, "香港 01 → 东京 03") {
		t.Fatalf("chains list: %d %s", code, out)
	}
	if _, errs, code = run(t, s, "undo"); code != 0 || len(s.Fake.ProxyChains()) != 0 {
		t.Fatalf("undo: %d %s %v", code, errs, s.Fake.ProxyChains())
	}
	os.WriteFile(f, []byte(`[{"name":"坏","hops":["香港 01"],"enabled":true}]`), 0o600)
	if _, errs, code = run(t, s, "chains", "set", f); code != ExitError || !strings.Contains(errs, "跳") {
		t.Fatalf("one hop accepted: %d %s", code, errs)
	}
	if _, _, code = run(t, s, "chains", "frobnicate"); code != ExitUsage {
		t.Fatalf("unknown chains command: %d", code)
	}
}
