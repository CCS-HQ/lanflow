package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/view"
)

// TestWindowOpens builds the whole window over the fake backend, as the
// app does, and visits every page.
func TestWindowOpens(t *testing.T) {
	api := fake.NewInstant()
	shell := view.New(api, view.Options{Mac: true, Sync: true})
	defer shell.Close()
	tt := ui.NewTester(shell.View, view.DefaultWidth, 820)
	if !tt.HasText("代理") || !tt.HasText("节点选择") {
		t.Fatalf("proxies not shown: %q", tt.Texts())
	}
	for _, page := range []string{"概览", "代理", "连接", "日志", "规则", "网络", "设置"} {
		if err := tt.Click(page); err != nil {
			t.Fatalf("click %s: %v", page, err)
		}
	}
	if err := tt.Click("概览"); err != nil {
		t.Fatalf("click 概览: %v", err)
	}
	if !tt.HasText("出口地图") {
		t.Fatalf("overview not shown after navigating to 概览: %q", tt.Texts())
	}
}
