package view

import "testing"

// TestAgentConsentDialog: a request shows the dialog and a notification;
// the button pressed is the answer; queued requests follow one by one.
func TestAgentConsentDialog(t *testing.T) {
	_, s, tt := newTest(t)
	var notified []string
	s.notify = func(title, body string, onClick func()) { notified = append(notified, body) }
	var answers []string
	s.AskAgentConsent(AgentConsent{Client: "mcp:claude", Agent: "claude", Kind: "write", Summary: `SetMode("global")`},
		func(a string) { answers = append(answers, a) })
	s.AskAgentConsent(AgentConsent{Client: "ctl", Kind: "sensitive", Summary: "Connections()"},
		func(a string) { answers = append(answers, a) })
	tt.Frame()
	if len(notified) != 2 || !tt.HasText("允许「claude」修改澜流的配置吗？") || !tt.HasText(`SetMode("global")`) {
		t.Fatalf("dialog: notified %q, texts %q", notified, tt.Texts())
	}
	click(t, tt, "始终允许")
	tt.Frame()
	if !tt.HasText("允许「lanflow ctl」读取敏感内容吗？") || tt.HasText("始终允许") {
		t.Fatalf("second dialog: %q", tt.Texts())
	}
	click(t, tt, "拒绝")
	tt.Frame()
	if len(answers) != 2 || answers[0] != "always" || answers[1] != "deny" {
		t.Fatalf("answers = %q", answers)
	}
	// A withdrawn request is dropped without an answer.
	withdraw := s.AskAgentConsent(AgentConsent{Client: "mcp:x", Agent: "x", Kind: "write"}, func(a string) { answers = append(answers, a) })
	tt.Frame()
	withdraw()
	tt.Frame()
	if tt.HasText("允许「x」修改澜流的配置吗？") || len(answers) != 2 {
		t.Fatalf("withdrawn request: answers %q, texts %q", answers, tt.Texts())
	}
}

// TestLongDialogsKeepButtons: a dialog with a long text (an agent's batch
// of changes, a validation error) scrolls the text and keeps its buttons
// inside the window at the minimum size, and enlarges to the window.
func TestLongDialogsKeepButtons(t *testing.T) {
	_, s, tt := newTest(t)
	s.notify = func(string, string, func()) {}
	tt.SetSize(MinWidth, MinHeight)
	s.AskAgentConsent(AgentConsent{Client: "mcp:claude", Agent: "claude", Kind: "write", Summary: longText(`rules.add {"type":"DOMAIN-SUFFIX","value":"example.com"}`, 80)},
		func(string) {})
	tt.Frame()
	inWindow := func(label string) {
		t.Helper()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("no %q: %q", label, tt.Texts())
		}
		if r.Y < 0 || r.Y+r.H > MinHeight || r.X < 0 || r.X+r.W > MinWidth {
			t.Fatalf("%q at %+v, outside the %dx%d window", label, r, MinWidth, MinHeight)
		}
	}
	inWindow("始终允许")
	inWindow("拒绝")
	click(t, tt, "放大窗口")
	tt.Frame()
	if !s.consentBig {
		t.Fatal("放大窗口 did not enlarge the dialog")
	}
	inWindow("始终允许")
	inWindow("还原大小")
}
