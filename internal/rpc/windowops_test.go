package rpc_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"lanflow/internal/app/fake"
	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
)

type testWindowHost struct{}

func (testWindowHost) Split() bool                  { return true }
func (testWindowHost) ShowWindow(string)            {}
func (testWindowHost) SetBackground(bool)           {}
func (testWindowHost) Quit()                        {}
func (testWindowHost) SetShortcut(string, string) error { return nil }

func TestSendUIResult(t *testing.T) {
	dir := shortDir(t)
	if runtime.GOOS == "windows" {
		t.Setenv("LANFLOW_SOCKET", `\\.\pipe\lanflow-test-`+filepath.Base(dir))
	}
	srv, err := rpc.Start(dir, rpc.Options{
		API:      fake.NewInstant(),
		Version:  "test",
		AuditDir: filepath.Join(dir, "logs"),
		Host:     testWindowHost{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	if srv.SendUI(rpc.UIAskQuit, "") {
		t.Fatal("SendUI without an attached window should return false")
	}

	got := make(chan rpc.UICommand, 1)
	c, err := remote.Dial(rpc.WithInteractive(context.Background()), remote.Options{
		DataDir: dir,
		Client:  "ui",
		Version: "test",
		OnUI:    func(u rpc.UICommand) { got <- u },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	res, err := c.AttachUI(nil, "")
	if err != nil || res.Forwarded {
		t.Fatalf("AttachUI = %+v, %v", res, err)
	}
	if !srv.SendUI(rpc.UIAskQuit, "now") {
		t.Fatal("SendUI with an attached window should return true")
	}
	select {
	case u := <-got:
		if u.Cmd != rpc.UIAskQuit || u.Arg != "now" {
			t.Fatalf("got UI command %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UI command")
	}

	_ = c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for srv.UIAttached() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.SendUI(rpc.UIAskQuit, "") {
		t.Fatal("SendUI after the UI session closed should return false")
	}
}
