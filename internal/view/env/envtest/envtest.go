// Package envtest holds what the tests of the window shell and of the
// pages share: an Env whose jobs really run on their own goroutines, as
// the shell's do in the app (tests otherwise run them on the calling
// thread, which hides every mistake of the "job writes what the view
// reads" kind from the tests and from -race), and a click that scrolls
// its target into the window first.
package envtest

import (
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/view/env"
)

// Async makes an Env run its jobs as the shell does outside tests: Run
// and RunThen start a goroutine each, and what is meant for the UI thread
// (a job's toast, RunThen's done) is queued until Drain runs it on the
// goroutine that builds the view.
//
//	a := envtest.NewAsync(e)
//	tt := ui.NewTester(a.View(page.View), 1000, 800)
//	clickSomething(tt)   // starts a job
//	a.Settle(tt.Frame)   // waits for it and builds the frames that follow
//
// Run the package's tests with -race: a job that writes a field the view
// reads is then reported.
type Async struct {
	e     *env.Env
	toast func(msg string, isError bool)

	mu       sync.Mutex
	cond     *sync.Cond
	pending  []func()
	running  int // jobs started and not yet finished
	finished int // jobs finished since NewAsync
}

// NewAsync replaces e.Run and e.RunThen with ones that run their jobs on
// goroutines of their own; e.Toast, if set, is then only called from
// Drain, so a recorder behind it needs no lock.
func NewAsync(e *env.Env) *Async {
	a := &Async{e: e, toast: e.Toast}
	a.cond = sync.NewCond(&a.mu)
	e.Run = a.run
	e.RunThen = a.runThen
	return a
}

func (a *Async) post(fn func()) {
	a.mu.Lock()
	a.pending = append(a.pending, fn)
	a.mu.Unlock()
}

func (a *Async) start(job func()) {
	a.mu.Lock()
	a.running++
	a.mu.Unlock()
	go func() {
		job()
		a.mu.Lock()
		a.running--
		a.finished++
		a.cond.Broadcast()
		a.mu.Unlock()
	}()
}

// run is Env.Run: the job on a goroutine, its outcome a toast.
func (a *Async) run(job func() error, okMsg string) {
	a.start(func() {
		err := job()
		a.post(func() {
			if a.toast == nil {
				return
			}
			if err != nil {
				a.toast(err.Error(), true)
			} else if okMsg != "" {
				a.toast(okMsg, false)
			}
		})
	})
}

// runThen is Env.RunThen: the job on a goroutine, done on the UI thread.
func (a *Async) runThen(job func() error, done func(err error)) {
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	if job == nil {
		a.post(func() { finish(nil) })
		return
	}
	a.start(func() {
		err := job()
		a.post(func() { finish(err) })
	})
}

// Drain runs what the jobs queued for the UI thread; call it where the
// view is built (View does).
func (a *Async) Drain() {
	a.mu.Lock()
	pending := a.pending
	a.pending = nil
	a.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
}

// View wraps a page's view so that every build drains the queue first,
// as the shell's does.
func (a *Async) View(view func(c *ui.Context)) func(c *ui.Context) {
	return func(c *ui.Context) {
		a.Drain()
		view(c)
	}
}

// WaitFinished blocks until n jobs have finished since NewAsync. Their
// results are queued then, not yet applied: build a frame for that.
func (a *Async) WaitFinished(n int) {
	a.mu.Lock()
	for a.finished < n {
		a.cond.Wait()
	}
	a.mu.Unlock()
}

// Finished returns how many jobs have finished since NewAsync.
func (a *Async) Finished() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.finished
}

// Idle reports that no job runs and nothing waits for the UI thread.
func (a *Async) Idle() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running == 0 && len(a.pending) == 0
}

// Settle waits for the running jobs, builds a frame (frame is the
// tester's Frame, with a view that drains) and goes on until nothing is
// left: a job's result may start the next job. No job may block for it.
func (a *Async) Settle(frame func()) {
	for i := 0; i < 1000; i++ {
		a.mu.Lock()
		for a.running > 0 {
			a.cond.Wait()
		}
		a.mu.Unlock()
		frame() // drains: the frame shows the results, and may start jobs
		if a.Idle() {
			return
		}
	}
	panic("envtest: jobs keep starting jobs")
}

// ClickScrolled scrolls the content until text is inside the window,
// then clicks it: a plain Tester.Click on a text below the fold misses,
// and where the fold is depends on the fonts of the system (the settings
// tabs scroll at the minimum window size, and sooner on Windows).
func ClickScrolled(t testing.TB, tt *ui.Tester, text string) {
	t.Helper()
	ScrollIntoView(t, tt, text)
	if err := tt.Click(text); err != nil {
		t.Fatalf("click %q: %v\ntexts: %q", text, err, tt.Texts())
	}
}

// ScrollIntoView scrolls the content until text is inside the window.
func ScrollIntoView(t testing.TB, tt *ui.Tester, text string) {
	t.Helper()
	for i := 0; i < 8; i++ {
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("find %q: %q", text, tt.Texts())
		}
		if r.Y+r.H <= float32(tt.Image().Bounds().Dy())-20 && r.Y >= 0 {
			return
		}
		tt.Scroll(r.X, 400, 0, 300)
		tt.Frame()
	}
}
