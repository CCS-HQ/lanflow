package view

import (
	"errors"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
)

// TestRecoveryPromptCannotBeDismissed: Escape closed the "系统代理未恢复"
// dialog for good, with the system proxy still pointing at a dead port
// and nothing else in the window to answer the question with. It stays
// up until one of its two buttons is chosen.
func TestRecoveryPromptCannotBeDismissed(t *testing.T) {
	api, s, tt := newTest(t)
	api.SetPendingRecovery(true)
	tt.Frame()
	if !tt.HasText("恢复为关闭") {
		t.Fatalf("recovery prompt not shown: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("恢复为关闭") || !s.recovery {
		t.Fatalf("Escape closed the recovery prompt: %q", tt.Texts())
	}
	tt.ClickAt(5, 5) // outside the dialog
	tt.Frame()
	tt.Frame()
	if !tt.HasText("恢复为关闭") || !s.recovery {
		t.Fatalf("a click outside closed the recovery prompt: %q", tt.Texts())
	}
	click(t, tt, "恢复为关闭")
	tt.Frame()
	tt.Frame()
	if api.Status().PendingRecovery || tt.HasText("恢复为关闭") || s.recovery {
		t.Fatal("the answer did not resolve the recovery")
	}
}

// failRecovery is the fake with a ResolveRecovery that fails.
type failRecovery struct{ *fake.Fake }

func (failRecovery) ResolveRecovery(bool) error { return errors.New("无法修改系统代理") }

// TestRecoveryPromptReturnsWhenResolvingFails: the question is still
// open after a failed answer, so the dialog comes back.
func TestRecoveryPromptReturnsWhenResolvingFails(t *testing.T) {
	f := fake.NewInstant()
	s := New(failRecovery{f}, Options{Mac: true, Sync: true})
	t.Cleanup(s.Close)
	tt := ui.NewTester(s.View, DefaultWidth, DefaultHeight)
	f.SetPendingRecovery(true)
	tt.Frame()
	click(t, tt, "恢复为关闭")
	tt.Frame()
	tt.Frame()
	if !s.recovery || !tt.HasText("保持开启并启动内核") {
		t.Fatalf("the prompt did not return after the failure: %q", tt.Texts())
	}
	if !tt.HasText("无法修改系统代理") {
		t.Fatalf("the failure was not reported: %q", tt.Texts())
	}
}

// TestConfirmationsQueue: a confirmation asked while another is open
// waits for it; before, it replaced the open one, whose onOK never ran.
func TestConfirmationsQueue(t *testing.T) {
	_, s, tt := newTest(t)
	var got []string
	s.ask("放弃修改？", "一", "放弃", true, func() { got = append(got, "discard") })
	tt.Frame()
	s.AskQuit(func() { got = append(got, "quit") })
	s.AskQuit(func() { got = append(got, "quit again") }) // 退出 chosen twice: one question
	tt.Frame()
	if !tt.HasText("放弃修改？") || tt.HasText(i18n.T("退出后将关闭系统代理。")) {
		t.Fatalf("the first confirmation should still show: %q", tt.Texts())
	}
	click(t, tt, "放弃")
	tt.Frame()
	if len(got) != 1 || got[0] != "discard" {
		t.Fatalf("answers %q, want the first confirmation's", got)
	}
	if !tt.HasText(i18n.T("退出后将关闭系统代理。")) {
		t.Fatalf("the queued confirmation did not come up: %q", tt.Texts())
	}
	click(t, tt, "取消")
	tt.Frame()
	tt.Frame()
	if len(got) != 1 || s.confirm != nil || len(s.confirms) != 0 {
		t.Fatalf("after cancelling: answers %q, %d waiting", got, len(s.confirms))
	}
	if tt.HasText(i18n.T("退出后将关闭系统代理。")) {
		t.Fatalf("the duplicate question was asked again: %q", tt.Texts())
	}
}

// TestAskFromAnotherGoroutine: the quit question of the split comes on
// the RPC event goroutine while the UI thread builds frames (run with
// -race); it shows, and its answer runs on the UI thread.
func TestAskFromAnotherGoroutine(t *testing.T) {
	_, s, tt := newTest(t)
	var wg sync.WaitGroup
	quit := false
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.AskQuit(func() { quit = true })
	}()
	for i := 0; i < 20; i++ {
		tt.Frame()
	}
	wg.Wait()
	tt.Frame()
	if !tt.HasText(i18n.T("退出后将关闭系统代理。")) {
		t.Fatalf("quit confirmation not shown: %q", tt.Texts())
	}
	click(t, tt, "退出")
	tt.Frame()
	if !quit {
		t.Fatal("quit did not run")
	}
}

// TestRunThenOffTheUIThread: without Sync the job runs on its own
// goroutine and done on the UI thread, in the next build.
func TestRunThenOffTheUIThread(t *testing.T) {
	api := fake.NewInstant()
	s := New(api, Options{Mac: true})
	t.Cleanup(s.Close)
	tt := ui.NewTester(s.View, DefaultWidth, DefaultHeight)
	jobDone := make(chan struct{})
	state := "" // as a page's field: written by done only, read by the view
	var result string
	s.env.RunThen(func() error {
		result = "computed"
		close(jobDone)
		return errors.New("失败")
	}, func(err error) {
		state = result + ": " + err.Error()
	})
	<-jobDone
	for i := 0; i < 100 && state == ""; i++ {
		tt.Frame() // drains what the job posted
	}
	if state != "computed: 失败" {
		t.Fatalf("state %q", state)
	}
	if tt.HasText("失败") {
		t.Fatal("RunThen toasts nothing by itself")
	}
	// A nil job only queues done for the UI thread.
	ran := false
	s.env.OnUI(func() { ran = true })
	if ran {
		t.Fatal("OnUI ran at once instead of on the UI thread's next build")
	}
	tt.Frame()
	if !ran {
		t.Fatal("OnUI did not run")
	}
	var _ app.API = api
}
