package capture

import (
	"bytes"
	"compress/gzip"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/klauspost/compress/zstd"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func br(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func zs(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.White})); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func hdr(kv ...string) []model.KV {
	var h []model.KV
	for i := 0; i+1 < len(kv); i += 2 {
		h = append(h, model.KV{Key: kv[i], Value: kv[i+1]})
	}
	return h
}

// TestMakePreview: what a body tab shows of each kind of body.
func TestMakePreview(t *testing.T) {
	json := []byte(`{"user":"a","password":"hunter2hunter2"}`)
	for _, c := range []struct {
		name   string
		body   []byte
		h      []model.KV
		reveal bool
		check  func(*bodyPreview) bool
	}{
		{"json masked", json, hdr("Content-Type", "application/json"), false, func(pv *bodyPreview) bool {
			return pv.kind == "json" && !strings.Contains(pv.text, "hunter2hunter2") && strings.Contains(pv.text, "user") && !pv.unmasked
		}},
		{"json revealed and indented", json, hdr("Content-Type", "application/json"), true, func(pv *bodyPreview) bool {
			return strings.Contains(pv.text, "hunter2hunter2") && strings.Contains(pv.text, "\n  \"user\"")
		}},
		{"gzip", gz(t, json), hdr("Content-Type", "application/json", "Content-Encoding", "gzip"), true, func(pv *bodyPreview) bool {
			return !pv.failed && pv.size == len(json) && strings.Contains(pv.text, "hunter2hunter2")
		}},
		{"brotli", br(t, json), hdr("Content-Type", "application/json", "Content-Encoding", "br"), false, func(pv *bodyPreview) bool {
			return !pv.failed && pv.size == len(json) && !strings.Contains(pv.text, "hunter2hunter2") && strings.Contains(pv.text, "user")
		}},
		{"zstd", zs(t, json), hdr("Content-Type", "application/json", "Content-Encoding", "zstd"), true, func(pv *bodyPreview) bool {
			return !pv.failed && pv.size == len(json) && strings.Contains(pv.text, "hunter2hunter2")
		}},
		{"unknown encoding", []byte("xx"), hdr("Content-Encoding", "lz4"), false, func(pv *bodyPreview) bool {
			return pv.failed && pv.enc == "lz4" && pv.text == ""
		}},
		{"form on lines", []byte("a=1&password=secretsecret"), hdr("Content-Type", "application/x-www-form-urlencoded"), false, func(pv *bodyPreview) bool {
			return pv.kind == "form" && strings.Contains(pv.text, "a=1\n") && !strings.Contains(pv.text, "secretsecret")
		}},
		{"text is not masked", []byte("hello token=abc"), hdr("Content-Type", "text/plain"), false, func(pv *bodyPreview) bool {
			return pv.kind == "text" && pv.unmasked && pv.text == "hello token=abc"
		}},
		{"text clipped", bytes.Repeat([]byte("x"), previewLimit+10), hdr("Content-Type", "text/plain"), true, func(pv *bodyPreview) bool {
			return pv.clipped && len(pv.text) == previewLimit && !pv.unmasked
		}},
		{"binary", []byte{0, 1, 2, 0xff, 0xfe}, hdr("Content-Type", "application/octet-stream"), false, func(pv *bodyPreview) bool {
			return pv.kind == "binary" && strings.HasPrefix(pv.hex, "0000  00 01 02 ff fe") && pv.text == ""
		}},
		{"image", pngOf(t, 40, 20), hdr("Content-Type", "image/png"), false, func(pv *bodyPreview) bool {
			return pv.kind == "image" && pv.img != nil && pv.img.Bounds().Dx() == 40 && !pv.tooLarge
		}},
		{"image scaled down", pngOf(t, 3000, 1000), hdr("Content-Type", "image/png"), false, func(pv *bodyPreview) bool {
			return pv.img != nil && pv.img.Bounds().Dx() == previewEdge
		}},
		{"image too large", pngOf(t, 5000, 4000), hdr("Content-Type", "image/png"), false, func(pv *bodyPreview) bool {
			return pv.kind == "image" && pv.img == nil && pv.tooLarge
		}},
		{"not an image after all", []byte("nope"), hdr("Content-Type", "image/png"), false, func(pv *bodyPreview) bool {
			return pv.kind != "image" || (pv.img == nil && !pv.tooLarge)
		}},
	} {
		if pv := makePreview(c.body, c.h, c.reveal); !c.check(pv) {
			t.Errorf("%s: %+v", c.name, *pv)
		}
	}
}

// wait runs frames until ok or two seconds passed.
func wait(t *testing.T, tt *ui.Tester, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(what)
}

// TestPreviewCache: a preview is made once for its bodies and its key,
// the request's and the response's are their own, and new bodies drop
// the old previews.
func TestPreviewCache(t *testing.T) {
	var pc previewCache
	var redraws atomic.Int32
	get := func(key bodyKey, body string) *bodyPreview {
		return pc.get(key, []byte(body), hdr("Content-Type", "text/plain"), func() { redraws.Add(1) })
	}
	until := func(key bodyKey, body string) *bodyPreview {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if pv := get(key, body); pv != nil {
				return pv
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("preview never made")
		return nil
	}
	req := until(bodyKey{}, "request")
	if again := get(bodyKey{}, "request"); again != req {
		t.Fatal("made again")
	}
	resp := until(bodyKey{resp: true}, "response")
	if resp == req || resp.text != "response" || req.text != "request" {
		t.Fatalf("request %q and response %q share a preview", req.text, resp.text)
	}
	if rev := until(bodyKey{reveal: true}, "request"); rev == req {
		t.Fatal("masked and revealed share a preview")
	}
	pc.reset(nil)
	if pv := until(bodyKey{}, "another"); pv == req || pv.text != "another" {
		t.Fatalf("after new bodies: %q", pv.text)
	}
	if redraws.Load() == 0 {
		t.Fatal("no redraw asked when a preview landed")
	}
}

// TestBodyTabUsesFetchedPreview: the detail arrives with its previews,
// and a build of the body tab makes none again.
func TestBodyTabUsesFetchedPreview(t *testing.T) {
	api := fake.NewInstant()
	arg := ""
	e, _ := testEnv(api, &arg)
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 800)
	p.sel, p.tab = 3, 2
	waitDetail(t, p, tt)
	p.bodies.mu.Lock()
	pv := p.bodies.done[bodyKey{}]
	p.bodies.mu.Unlock()
	if pv == nil || !strings.Contains(pv.text, "grant_type=password") {
		t.Fatalf("no preview came with the detail: %+v", pv)
	}
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	p.bodies.mu.Lock()
	same, busy := p.bodies.done[bodyKey{}] == pv, len(p.bodies.busy)
	p.bodies.mu.Unlock()
	if !same || busy != 0 {
		t.Fatalf("the preview was made again (same %v, %d in the making)", same, busy)
	}
	// The other state of the switch is made on demand, then kept.
	p.reveal = true
	wait(t, tt, "revealed body never showed", func() bool {
		return strings.Contains(strings.Join(tt.Texts(), "\n"), "hunter2hunter2")
	})
}

// TestDoneSaves: 完成 saves the edited selection; an invalid value keeps
// the editor open, and an unchanged one closes it without saving.
func TestDoneSaves(t *testing.T) {
	api := fake.NewInstant()
	arg := ""
	e, toasts := testEnv(api, &arg)
	p := New(e)
	set := api.CaptureStatus().Settings
	set.Acknowledged = true
	if err := api.SaveCaptureSettings(set); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(p.View, 1300, 800)
	if err := tt.Click("选择…"); err != nil {
		t.Fatal(err)
	}
	if !p.editing || !tt.HasText("完成") {
		t.Fatalf("editor not open: %q", tt.Texts())
	}
	// Unchanged: closes, saves nothing.
	*toasts = nil
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	if p.editing || len(*toasts) != 0 {
		t.Fatalf("unchanged 完成: editing %v, toasts %q", p.editing, *toasts)
	}
	// Edited: 完成 saves.
	if err := tt.Click("选择…"); err != nil {
		t.Fatal(err)
	}
	p.domains += "\nedited.example.org"
	tt.Frame()
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	got := api.CaptureStatus().Settings.Domains
	if p.editing || len(got) == 0 || got[len(got)-1] != "edited.example.org" {
		t.Fatalf("完成 did not save: editing %v, domains %q", p.editing, got)
	}
	// Invalid: stays open with a message, nothing lost.
	if err := tt.Click("选择…"); err != nil {
		t.Fatal(err)
	}
	p.domains += "\nkept.example.org"
	p.autoOff = "soon"
	*toasts = nil
	tt.Frame()
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	if !p.editing || len(*toasts) != 1 || !strings.Contains(p.domains, "kept.example.org") {
		t.Fatalf("invalid 完成: editing %v, toasts %q", p.editing, *toasts)
	}
}

// TestExportsAskOffTheBuild: the save dialog is not opened while the view
// builds (the menu item's Chosen), but by what spawn starts.
func TestExportsAskOffTheBuild(t *testing.T) {
	api := fake.NewInstant()
	arg := ""
	e, toasts := testEnv(api, &arg)
	p := New(e)
	var jobs []func()
	p.async = func(fn func()) { jobs = append(jobs, fn) }
	asked := 0
	old := saveDialog
	saveDialog = func(o mygo.SaveDialogOptions) (string, error) { asked++; return "/tmp/out", nil }
	t.Cleanup(func() { saveDialog = old })
	p.exportHAR(false)
	p.exportCA()
	if asked != 0 || len(jobs) != 2 {
		t.Fatalf("asked %d times in the build, %d jobs", asked, len(jobs))
	}
	for _, j := range jobs {
		j()
	}
	if asked != 2 || len(*toasts) != 2 || !strings.Contains((*toasts)[0], "out.har") {
		t.Fatalf("asked %d, toasts %q", asked, *toasts)
	}
}

// countFlows counts the reads of the records.
type countFlows struct {
	*fake.Fake
	reads int
}

func (c *countFlows) CaptureFlows() []model.CaptureFlow { c.reads++; return c.Fake.CaptureFlows() }

// TestRowsKeptBetweenChanges: a build for anything but new records reads
// and sorts nothing again; a new search or order filters the rows held.
func TestRowsKeptBetweenChanges(t *testing.T) {
	f := fake.NewInstant()
	api := &countFlows{Fake: f}
	arg := ""
	e, _ := testEnv(f, &arg)
	e.API = api
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 800)
	tt.Frame()
	reads, rows := api.reads, p.shown.rows
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	tt.Frame()
	tt.Move(400, 400)
	tt.Frame()
	if api.reads != reads || &p.shown.rows[0] != &rows[0] {
		t.Fatalf("read %d more times; rows made again: %v", api.reads-reads, &p.shown.rows[0] != &rows[0])
	}
	p.query = "github"
	tt.Frame()
	if api.reads != reads || len(p.shown.rows) == 0 || len(p.shown.rows) >= len(rows) {
		t.Fatalf("search: %d more reads, %d of %d rows", api.reads-reads, len(p.shown.rows), len(rows))
	}
	p.query = ""
	p.sort = ui.SortOrder{Column: "host"}
	tt.Frame()
	if api.reads != reads || p.shown.rows[0].Host > p.shown.rows[1].Host {
		t.Fatalf("order: %d more reads", api.reads-reads)
	}
	if err := f.ClearCaptureFlows(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if api.reads == reads {
		t.Fatal("new data was not read")
	}
	for _, r := range p.shown.rows {
		if r.ID > 0 {
			t.Fatalf("a cleared record still shows: %+v", r)
		}
	}
}
