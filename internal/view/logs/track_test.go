package logs

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// TestTracker: entries keep their numbers as the buffer drops old ones,
// and a cleared or replaced log never reuses a number.
func TestTracker(t *testing.T) {
	all := entries(30)
	var k tracker
	for _, c := range []struct {
		name string
		log  []model.LogEntry
		want uint64
	}{
		{"first", all[0:10], 0},
		{"same", all[0:10], 0},
		{"appended", all[0:12], 0},
		{"one dropped, one added", all[1:13], 1},
		{"three dropped", all[4:16], 4},
		{"only drops", all[6:16], 6},
		{"all replaced", all[20:25], 16}, // after the 10 held (6…15)
		{"cleared", nil, 21},
		{"still empty", nil, 21},
		{"new after clear", all[0:3], 21},
		{"grows", all[0:5], 21},
	} {
		if got := k.follow(c.log); got != c.want {
			t.Fatalf("%s: base %d, want %d", c.name, got, c.want)
		}
	}
}

// fullAPI is a log that holds at most cap entries, as the service's does.
func push(api *logAPI, cap int, l model.LogEntry) {
	api.logs = append(api.logs, l)
	if len(api.logs) > cap {
		api.logs = api.logs[len(api.logs)-cap:]
	}
}

// TestFullBufferKeepsPlaceAndHint: with the buffer full, new entries drop
// the oldest ones; a reader scrolled up stays on the same lines, and the
// button still tells of new lines.
func TestFullBufferKeepsPlaceAndHint(t *testing.T) {
	const size = 400
	api := &logAPI{Fake: fake.NewInstant(), logs: entries(size)}
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 600)
	tt.Scroll(500, 300, 0, -2000)
	tt.Frame()
	if p.list.AtEnd() {
		t.Fatal("did not scroll up")
	}
	first, _ := p.list.Visible()
	top := p.vis.idx[first]
	reading := api.logs[top]
	if tt.HasText("回到底部（有新日志）") {
		t.Fatal("hint before any new line")
	}
	for i := 0; i < 5; i++ {
		push(api, size, model.LogEntry{Time: time.Now(), Level: "info", Payload: "later"})
		tt.Frame()
	}
	if len(api.logs) != size {
		t.Fatalf("buffer holds %d", len(api.logs))
	}
	if !tt.HasText("回到底部（有新日志）") {
		t.Fatalf("no new-line hint with a full buffer: %q", tt.Texts()[:6])
	}
	first, _ = p.list.Visible()
	if got := api.logs[p.vis.idx[first]]; !sameEntry(got, reading) {
		t.Fatalf("the list slid: reading %q, now at %q", reading.Payload, got.Payload)
	}
	if err := tt.Click("回到底部（有新日志）"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.list.AtEnd() || tt.HasText("回到底部") {
		t.Fatal("回到底部 should scroll to the end and hide")
	}
}

// countLogs counts the reads of the log.
type countLogs struct {
	*logAPI
	reads int
}

func (c *countLogs) Logs() []model.LogEntry { c.reads++; return c.logAPI.Logs() }

// TestPausedReadsNothing: paused, the log is not copied every build, and
// the filter's answer is kept while nothing changes.
func TestPausedReadsNothing(t *testing.T) {
	api := &countLogs{logAPI: &logAPI{Fake: fake.NewInstant(), logs: entries(50)}}
	e, _ := testEnv(api.logAPI)
	e.API = api
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 600)
	idx := p.vis.idx
	tt.Frame()
	if &p.vis.idx[0] != &idx[0] {
		t.Fatal("the filter ran again for an unchanged log")
	}
	p.paused = true
	tt.Frame()
	reads := api.reads
	tt.Frame()
	tt.Move(400, 300)
	tt.Frame()
	if api.reads != reads {
		t.Fatalf("paused, the log was read %d more times", api.reads-reads)
	}
	p.paused = false
	api.logAPI.logs = append(api.logAPI.logs, model.LogEntry{Time: time.Now(), Level: "error", Payload: "RESUMED"})
	tt.Frame()
	if !tt.HasText("RESUMED") {
		t.Fatal("resumed, the new line shows")
	}
}
