package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"lanflow/internal/controller"
	"lanflow/internal/model"
)

// These tests are plain unit tests: no kernel, no listener, no process.

func spillFiles(t *testing.T, dir string) []string {
	t.Helper()
	fs, _ := filepath.Glob(filepath.Join(dir, "session-*.bin"))
	return fs
}

// A connection keeps its record after the ring pushed it out or the list
// was cleared. What it writes from then on must not be counted: nothing
// would ever take it off the count again.
func TestStoreIgnoresRecordsThatLeftTheRing(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, 500)
	for _, c := range []struct {
		name  string
		leave func(s *store)
	}{
		{"pushed out by newer records", func(s *store) {
			for i := 0; i < 3; i++ {
				s.add(model.CaptureFlow{})
			}
		}},
		{"cleared", func(s *store) { s.clear() }},
	} {
		s := newStore(3, 1<<10, 1<<20, "", 0)
		r := s.add(model.CaptureFlow{Host: "stream.test"})
		s.appendBody(r, true, body)
		if s.memUsed != 500 {
			t.Fatalf("%s: counted %d before leaving", c.name, s.memUsed)
		}
		c.leave(s)
		if s.memUsed != 0 {
			t.Fatalf("%s: %d bytes still counted for a record that left", c.name, s.memUsed)
		}
		seq := s.seq
		for i := 0; i < 10; i++ {
			s.appendBody(r, true, body)
			s.appendBody(r, false, body)
		}
		s.update(r, func(d *model.CaptureFlowDetail) { d.Flow.Done = true })
		if s.memUsed != 0 {
			t.Errorf("%s: memory count drifted to %d", c.name, s.memUsed)
		}
		if len(r.d.ReqBody)+len(r.d.RespBody) != 0 {
			t.Errorf("%s: bodies kept for a record nothing can read", c.name)
		}
		if s.seq != seq {
			t.Errorf("%s: a record that left still signals changes", c.name)
		}
		if _, ok := s.detail(r.d.Flow.ID); ok {
			t.Errorf("%s: record still readable", c.name)
		}
	}
}

// The spill is not append-only: space of records that left the ring comes
// back, so a long session keeps bodies instead of dropping every one after
// the first spillLimit bytes.
func TestStoreSpillSpaceIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	const limit = 64 << 10
	s := newStore(8, 4<<10, 4<<10, dir, limit) // every finished body is spilled almost at once
	var written int64
	var last *record
	for i := 0; i < 400; i++ { // 400 × 3 KiB: many times the limit
		r := s.add(model.CaptureFlow{})
		s.appendBody(r, true, bytes.Repeat([]byte{byte(i)}, 3<<10))
		s.update(r, func(d *model.CaptureFlowDetail) { d.Flow.Done = true })
		written += 3 << 10
		last = r
		if s.spillUsed > limit {
			t.Fatalf("after %d records the spill holds %d bytes, limit %d", i+1, s.spillUsed, limit)
		}
	}
	if written < 10*limit {
		t.Fatal("the test did not write enough to wrap")
	}
	var onDisk int64
	for _, f := range spillFiles(t, dir) {
		st, _ := os.Stat(f)
		onDisk += st.Size()
	}
	if onDisk > limit {
		t.Fatalf("spill files hold %d bytes, limit %d", onDisk, limit)
	}
	// the newest records still have their bodies, wherever they are
	kept := 0
	fs, _, _, _ := s.since(0, 0)
	for _, f := range fs {
		d, ok := s.detail(f.ID)
		if ok && len(d.RespBody) == 3<<10 && d.RespBody[0] == byte(f.ID-1) {
			kept++
		}
	}
	if kept != len(fs) {
		t.Fatalf("%d of %d records in the ring have their body: bodies were dropped although space came back", kept, len(fs))
	}
	if last.dropped {
		t.Fatal("the newest record's body was dropped")
	}
	s.clear()
	if s.spillUsed != 0 || len(spillFiles(t, dir)) != 0 {
		t.Fatalf("after clear: %d bytes, files %v", s.spillUsed, spillFiles(t, dir))
	}
	// and the store spills again afterwards
	r := s.add(model.CaptureFlow{})
	s.appendBody(r, true, bytes.Repeat([]byte{'z'}, 4<<10))
	s.appendBody(r, false, bytes.Repeat([]byte{'y'}, 4<<10))
	s.update(r, func(d *model.CaptureFlowDetail) { d.Flow.Done = true })
	if d, _ := s.detail(r.d.Flow.ID); !r.spilled || len(d.RespBody) != 4<<10 || d.RespBody[0] != 'z' || d.ReqBody[0] != 'y' {
		t.Fatalf("after clear: spilled=%v resp=%d", r.spilled, len(d.RespBody))
	}
	s.close()
	if len(spillFiles(t, dir)) != 0 {
		t.Fatal("spill files left behind")
	}
}

// Memory held by unfinished records cannot be spilled; the scan for
// something to spill is then not repeated on every update.
func TestStoreSpillScanBacksOff(t *testing.T) {
	s := newStore(100, 1<<20, 8<<10, t.TempDir(), 0)
	t.Cleanup(s.close) // before the directory goes: Windows keeps an open file
	r := s.add(model.CaptureFlow{})
	s.appendBody(r, true, bytes.Repeat([]byte{'a'}, 9<<10)) // over the limit, unfinished
	if s.spillFloor <= s.memUsed {
		t.Fatalf("no back-off: floor %d, used %d", s.spillFloor, s.memUsed)
	}
	floor := s.spillFloor
	s.appendBody(r, true, []byte("b"))
	if s.spillFloor != floor {
		t.Fatal("scanned again for one more byte")
	}
	s.update(r, func(d *model.CaptureFlowDetail) { d.Flow.Done = true })
	if !r.spilled || s.memUsed != 0 {
		t.Fatalf("finished record not spilled: spilled=%v used=%d", r.spilled, s.memUsed)
	}
}

func TestLossyPipe(t *testing.T) {
	// in order, then EOF after Close
	p := newLossyPipe(16)
	p.Write([]byte("hello "))
	p.Write([]byte("world"))
	p.Close()
	if b, err := io.ReadAll(p); err != nil || string(b) != "hello world" {
		t.Fatalf("%q %v", b, err)
	}
	if n, err := p.Write([]byte("late")); n != 4 || err != nil {
		t.Fatalf("write after close: %d %v", n, err)
	}

	// a reader that falls behind: the pipe breaks, the writer never blocks
	// or fails, the reader stops with an error and gets nothing stale
	p = newLossyPipe(8)
	p.Write([]byte("12345678"))
	if n, err := p.Write([]byte("9")); n != 1 || err != nil {
		t.Fatalf("overflowing write: %d %v", n, err)
	}
	if n, err := p.Read(make([]byte, 4)); n != 0 || !errors.Is(err, errPipeBroken) {
		t.Fatalf("read of a broken pipe: %d %v", n, err)
	}
	if n, err := p.Write([]byte("more")); n != 4 || err != nil {
		t.Fatalf("write to a broken pipe: %d %v", n, err)
	}

	// Abort wakes a reader that is waiting
	p = newLossyPipe(8)
	got := make(chan error, 1)
	go func() { _, err := p.Read(make([]byte, 1)); got <- err }()
	time.Sleep(20 * time.Millisecond)
	p.Abort()
	select {
	case err := <-got:
		if !errors.Is(err, errPipeBroken) {
			t.Fatalf("aborted read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Abort did not wake the reader")
	}

	// teeReader copies what it forwards
	p = newLossyPipe(64)
	tr := teeReader{r: strings.NewReader("forwarded"), w: p}
	b, _ := io.ReadAll(tr)
	p.Close()
	c, _ := io.ReadAll(p)
	if string(b) != "forwarded" || string(c) != "forwarded" {
		t.Fatalf("%q %q", b, c)
	}
}

// h1Run feeds both directions to a parser and returns the store.
func h1Run(t *testing.T, req, resp io.Reader) *store {
	t.Helper()
	s := newStore(100, 1<<20, 0, "", 0)
	p := newH1Parser(&connRec{st: s, base: model.CaptureFlow{Host: "h.test", Port: 80}}, "http", "h.test")
	done := make(chan struct{})
	go func() { p.requests(req); close(done) }()
	p.responses(resp)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the request side is still waiting after the response side ended")
	}
	p.drain("") // as relay does once both sides are done
	return s
}

// A request with an Upgrade header waits for the response to learn the
// protocol. When the connection ends before any response (a WebSocket
// server that drops the handshake), it must stop waiting at once: it holds
// one of the decrypting slots meanwhile.
func TestH1UpgradeWithoutAnswerDoesNotWait(t *testing.T) {
	start := time.Now()
	s := h1Run(t, strings.NewReader("GET /ws HTTP/1.1\r\nHost: h.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"),
		strings.NewReader(""))
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("waited %v", d)
	}
	fs, _, _, _ := s.since(0, 0)
	if len(fs) != 1 || !fs[0].Done || fs[0].Error == "" || fs[0].Has(model.FlowWebSocket) {
		t.Fatalf("%+v", fs)
	}
}

func TestH1EdgeCases(t *testing.T) {
	upgrade := "GET /ws HTTP/1.1\r\nHost: h.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"
	cases := []struct {
		name      string
		req, resp string
		want      []string // "METHOD path status error?" per flow
	}{
		{
			"upgrade refused: the connection stays HTTP",
			upgrade + "GET /next HTTP/1.1\r\nHost: h.test\r\n\r\n",
			"HTTP/1.1 400 Bad Request\r\nContent-Length: 2\r\n\r\nno" + "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok",
			[]string{"GET /ws 400", "GET /next 200"},
		},
		{
			"upgrade accepted: frames, not requests",
			upgrade + string(wsFrame(1, []byte("hi"), true)),
			"HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n" + string(wsFrame(1, []byte("yo"), false)),
			[]string{"GET /ws 101"},
		},
		{
			"Connection: close among other tokens ends the response side",
			"GET /a HTTP/1.1\r\nHost: h.test\r\n\r\nGET /b HTTP/1.1\r\nHost: h.test\r\n\r\n",
			"HTTP/1.1 200 OK\r\nConnection: close, TE\r\nContent-Length: 1\r\n\r\nx" + "HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\ny",
			[]string{"GET /a 200", "GET /b 0 连接在响应前关闭"},
		},
		{
			"keep-alive is not close",
			"GET /a HTTP/1.1\r\nHost: h.test\r\n\r\nGET /b HTTP/1.1\r\nHost: h.test\r\n\r\n",
			"HTTP/1.1 200 OK\r\nConnection: keep-alive\r\nContent-Length: 1\r\n\r\nx" + "HTTP/1.1 204 No Content\r\n\r\n",
			[]string{"GET /a 200", "GET /b 204"},
		},
		{
			"100-continue, then the body and the answer",
			"PUT /up HTTP/1.1\r\nHost: h.test\r\nExpect: 100-continue\r\nContent-Length: 4\r\n\r\nbody",
			"HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 201 Created\r\nContent-Length: 0\r\n\r\n",
			[]string{"PUT /up 201"},
		},
		{
			// the client then sends no body and closes: the record keeps
			// the answer, not a body error
			"100-continue refused",
			"PUT /up HTTP/1.1\r\nHost: h.test\r\nExpect: 100-continue\r\nContent-Length: 4\r\n\r\n",
			"HTTP/1.1 417 Expectation Failed\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
			[]string{"PUT /up 417"},
		},
	}
	for _, c := range cases {
		s := h1Run(t, strings.NewReader(c.req), strings.NewReader(c.resp))
		fs, _, _, _ := s.since(0, 0)
		var got []string
		for _, f := range fs {
			l := f.Method + " " + f.Path + " " + itoa(f.Status)
			if f.Error != "" {
				l += " " + f.Error
			}
			if !f.Done {
				l += " (open)"
			}
			got = append(got, l)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if strings.HasPrefix(c.name, "upgrade accepted") {
			d, _ := s.detail(fs[0].ID)
			if !d.Flow.Has(model.FlowWebSocket) || len(d.WSFrames) != 2 {
				t.Errorf("%s: frames %+v", c.name, d.WSFrames)
			}
		}
	}
}

func TestHasToken(t *testing.T) {
	for v, want := range map[string]bool{"close": true, "Close": true, "close, TE": true, "TE ,  CLOSE": true,
		"keep-alive": false, "": false, "closed": false, "not-close": false} {
		if hasToken(v, "close") != want {
			t.Errorf("hasToken(%q) = %v", v, !want)
		}
	}
}

// h2Side builds the bytes one side of an HTTP/2 connection sends.
type h2Side struct {
	buf bytes.Buffer
	fr  *http2.Framer
	hb  bytes.Buffer
	enc *hpack.Encoder
}

func newH2Side(client bool) *h2Side {
	s := &h2Side{}
	if client {
		s.buf.WriteString(http2.ClientPreface)
	}
	s.fr = http2.NewFramer(&s.buf, nil)
	s.enc = hpack.NewEncoder(&s.hb)
	s.fr.WriteSettings()
	return s
}

func (s *h2Side) block(kv ...string) []byte {
	s.hb.Reset()
	for i := 0; i+1 < len(kv); i += 2 {
		s.enc.WriteField(hpack.HeaderField{Name: kv[i], Value: kv[i+1]})
	}
	return append([]byte(nil), s.hb.Bytes()...)
}

func (s *h2Side) headers(id uint32, end bool, kv ...string) {
	s.fr.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: s.block(kv...), EndHeaders: true, EndStream: end})
}

func (s *h2Side) data(id uint32, end bool, b string) { s.fr.WriteData(id, end, []byte(b)) }

func h2Flows(s *store) []string {
	fs, _, _, _ := s.since(0, 0)
	var out []string
	for _, f := range fs {
		d, _ := s.detail(f.ID)
		l := f.Method + " " + f.Host + f.Path + " " + itoa(f.Status) + " req=" + string(d.ReqBody) + " resp=" + string(d.RespBody)
		if f.Error != "" {
			l += " err=" + f.Error
		}
		if !f.Done {
			l += " (open)"
		}
		out = append(out, l)
	}
	return out
}

func TestH2Parser(t *testing.T) {
	get := func(c *h2Side, id uint32, path string) {
		c.headers(id, true, ":method", "GET", ":scheme", "https", ":authority", "h2.test", ":path", path, "x-token", "abc")
	}
	build := func() (*h2Side, *h2Side) {
		c, sv := newH2Side(true), newH2Side(false)
		get(c, 1, "/one")
		c.headers(3, false, ":method", "POST", ":scheme", "https", ":authority", "h2.test", ":path", "/two", "content-type", "text/plain")
		c.data(3, true, "ping")
		get(c, 5, "/reset")
		get(c, 7, "/open")

		sv.headers(1, false, ":status", "200", "content-type", "text/plain")
		sv.data(1, false, "he")
		sv.data(1, true, "llo")
		sv.headers(3, false, ":status", "103")
		sv.headers(3, false, ":status", "201")
		sv.data(3, false, "pong")
		sv.headers(3, true, "x-trailer", "t")
		sv.fr.WriteRSTStream(5, http2.ErrCodeRefusedStream)
		return c, sv
	}
	want := []string{
		"GET h2.test/one 200 req= resp=hello",
		"POST h2.test/two 201 req=ping resp=pong",
		"GET h2.test/reset 0 req= resp= err=RST_STREAM REFUSED_STREAM",
		"GET h2.test/open 0 req= resp= err=连接已关闭",
	}
	// The two directions are parsed from separate copies and may run in
	// any order: a fast server's answer can be parsed before the request
	// it belongs to. The records must come out the same.
	for _, order := range []string{"requests first", "responses first", "together"} {
		c, sv := build()
		s := newStore(100, 1<<20, 0, "", 0)
		p := newH2Parser(&connRec{st: s, base: model.CaptureFlow{Host: "h2.test", Port: 443}})
		switch order {
		case "requests first":
			p.run(&c.buf, true)
			p.run(&sv.buf, false)
		case "responses first":
			p.run(&sv.buf, false)
			p.run(&c.buf, true)
		default:
			done := make(chan struct{})
			go func() { p.run(&c.buf, true); close(done) }()
			p.run(&sv.buf, false)
			<-done
			p.abort("") // as relay does once both sides are done
		}
		got := h2Flows(s)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", order, got, want)
		}
		if order != "together" && (len(p.streams) != 0 || len(p.parked) != 0 || p.parkedN != 0) {
			t.Errorf("%s: left %d streams, %d parked (%d bytes)", order, len(p.streams), len(p.parked), p.parkedN)
		}
		d, _ := s.detail(2)
		if headerValue(d.RespHeader, "x-trailer") != "t" || headerValue(d.ReqHeader, "content-type") != "text/plain" {
			t.Errorf("%s: headers %+v / %+v", order, d.ReqHeader, d.RespHeader)
		}
	}
}

// What the server says about streams the request side never opens is not
// kept without bound, and is dropped with the connection.
func TestH2ParkedEventsAreBounded(t *testing.T) {
	s := newStore(100, 1<<20, 0, "", 0)
	p := newH2Parser(&connRec{st: s})
	chunk := bytes.Repeat([]byte{'x'}, 16<<10)
	for id := uint32(1); id < 401; id += 2 {
		p.server(id, h2event{headers: true, status: 200})
		p.server(id, h2event{data: chunk})
		if p.parkedN > h2ParkMax {
			t.Fatalf("%d bytes parked, limit %d", p.parkedN, h2ParkMax)
		}
	}
	if p.parkedN < h2ParkMax/2 {
		t.Fatalf("only %d bytes parked", p.parkedN)
	}
	p.server(2, h2event{data: chunk}) // the server's own stream (push)
	if _, ok := p.parked[2]; ok {
		t.Fatal("a pushed stream was parked")
	}
	// the request arrives: its answer is replayed and no longer parked
	before := p.parkedN
	rec := s.add(model.CaptureFlow{Method: "GET"})
	p.register(1, &h2stream{rec: rec})
	if d, _ := s.detail(rec.d.Flow.ID); d.Flow.Status != 200 || len(d.RespBody) != len(chunk) {
		t.Fatalf("replayed: status %d, %d bytes", d.Flow.Status, len(d.RespBody))
	}
	if p.parkedN >= before || p.parked[1] != nil {
		t.Fatalf("still parked after the replay: %d of %d", p.parkedN, before)
	}
	// later events of a registered stream are applied at once, and events
	// of streams below the highest request are for streams already over
	p.server(1, h2event{end: true})
	p.server(1, h2event{data: chunk})
	if d, _ := s.detail(rec.d.Flow.ID); !d.Flow.Done || len(d.RespBody) != len(chunk) || p.parked[1] != nil {
		t.Fatalf("after the end: done=%v, %d bytes", d.Flow.Done, len(d.RespBody))
	}
	p.abort("")
	if len(p.parked) != 0 || p.parkedN != 0 {
		t.Fatalf("the end of the connection left %d parked streams", len(p.parked))
	}
}

// A PUSH_PROMISE changes the server's HPACK table like any other header
// block: left out, every later header of the connection would be decoded
// with the wrong table. One that continues in CONTINUATION frames is
// refused by the framer: the records stop with that error instead of
// going on with a table out of step.
func TestH2PushPromiseKeepsTheTable(t *testing.T) {
	for _, split := range []bool{false, true} {
		c, sv := newH2Side(true), newH2Side(false)
		c.headers(1, true, ":method", "GET", ":scheme", "https", ":authority", "h2.test", ":path", "/")
		// the promise puts "x-pushed" into the dynamic table…
		blk := sv.block(":method", "GET", ":scheme", "https", ":authority", "h2.test", ":path", "/style.css", "x-pushed", "first-value-in-the-table")
		if split {
			half := len(blk) / 2
			sv.fr.WritePushPromise(http2.PushPromiseParam{StreamID: 1, PromiseID: 2, BlockFragment: blk[:half], EndHeaders: false})
			sv.fr.WriteContinuation(1, true, blk[half:])
		} else {
			sv.fr.WritePushPromise(http2.PushPromiseParam{StreamID: 1, PromiseID: 2, BlockFragment: blk, EndHeaders: true})
		}
		// …and the response refers to that entry by index
		sv.headers(1, true, ":status", "200", "x-pushed", "first-value-in-the-table", "x-after", "ok")

		s := newStore(100, 1<<20, 0, "", 0)
		p := newH2Parser(&connRec{st: s})
		p.run(&c.buf, true)
		p.run(&sv.buf, false)
		fs, _, _, _ := s.since(0, 0)
		if len(fs) != 1 || !fs[0].Done {
			t.Fatalf("split=%v: %+v", split, fs)
		}
		d, _ := s.detail(fs[0].ID)
		if split {
			if fs[0].Status != 0 || !strings.Contains(fs[0].Error, "HTTP/2 解析中止") || len(d.RespHeader) != 0 {
				t.Fatalf("split promise: went on parsing: %+v %+v", fs[0], d.RespHeader)
			}
			continue
		}
		if fs[0].Status != 200 || fs[0].Error != "" ||
			headerValue(d.RespHeader, "x-pushed") != "first-value-in-the-table" || headerValue(d.RespHeader, "x-after") != "ok" {
			t.Fatalf("headers decoded with a table out of step: %+v %+v", fs[0], d.RespHeader)
		}
	}
}

func TestSourceCheckMatch(t *testing.T) {
	t0 := time.Now()
	conn := func(id string, age time.Duration, src, typ, proc string) controller.Connection {
		return controller.Connection{ID: id, Start: t0.Add(-age), Chains: []string{"LANFLOW-MITM-x", "规则"},
			Metadata: controller.Metadata{Type: typ, SourceIP: src, DestinationPort: "443", Host: "API.test", Process: proc, ProcessPath: "/bin/" + proc}}
	}
	other := controller.Connection{ID: "o", Start: t0, Chains: []string{"DIRECT"},
		Metadata: controller.Metadata{SourceIP: "127.0.0.1", DestinationPort: "443", Host: "api.test"}}
	byIP := controller.Connection{ID: "ip", Start: t0, Chains: []string{"LANFLOW-MITM-x"},
		Metadata: controller.Metadata{SourceIP: "127.0.0.1", DestinationPort: "443", DestinationIP: "203.0.113.7", SniffHost: "Sniffed.test"}}

	cases := []struct {
		name        string
		conns       []controller.Connection
		host        string
		port        int
		found       bool
		local       bool
		process     string
		attribution string
		rhost       string
	}{
		{"one local connection", []controller.Connection{other, conn("a", time.Second, "127.0.0.1", "HTTP", "curl")}, "api.test", 443, true, true, "curl", "certain", "api.test"},
		{"not through the outbound", []controller.Connection{other}, "api.test", 443, false, false, "", "", ""},
		{"another port", []controller.Connection{conn("a", 0, "127.0.0.1", "HTTP", "curl")}, "api.test", 8443, false, false, "", "", ""},
		{"TUN counts as this machine", []controller.Connection{conn("a", 0, "198.18.0.1", "Tun", "curl")}, "api.test", 443, true, true, "curl", "certain", "api.test"},
		{"from the LAN", []controller.Connection{conn("a", 0, "192.168.1.9", "HTTP", "")}, "api.test", 443, true, false, "", "certain", "api.test"},
		{"two programs to the same host: the oldest, not certain",
			[]controller.Connection{conn("new", time.Second, "127.0.0.1", "HTTP", "wget"), conn("old", time.Minute, "127.0.0.1", "HTTP", "curl")},
			"api.test", 443, true, true, "curl", "ambiguous", "api.test"},
		{"one candidate from the LAN makes all of them unconfirmed",
			[]controller.Connection{conn("a", time.Minute, "127.0.0.1", "HTTP", "curl"), conn("b", time.Second, "10.0.0.8", "HTTP", "curl")},
			"api.test", 443, true, false, "curl", "certain", "api.test"},
		{"a CONNECT by address takes the name the core sniffed", []controller.Connection{byIP}, "203.0.113.7", 443, true, true, "", "certain", "sniffed.test"},
		{"started after the agent was handed the connection", []controller.Connection{conn("late", -5*time.Second, "127.0.0.1", "HTTP", "curl")}, "api.test", 443, false, false, "", "", ""},
	}
	for _, c := range cases {
		s := &sourceCheck{claimed: map[string]time.Time{}}
		r, ok := s.match(controller.Connections{Connections: c.conns}, "LANFLOW-MITM-x", c.host, c.port, t0)
		if ok != c.found || r.found != c.found || r.local != c.local || r.process != c.process || r.attribution != c.attribution || r.host != c.rhost {
			t.Errorf("%s: ok=%v %+v", c.name, ok, r)
		}
	}

	// each connection is claimed once: two agent connections to the same
	// host get the two core connections in order, a third gets none
	s := &sourceCheck{claimed: map[string]time.Time{}}
	cs := controller.Connections{Connections: []controller.Connection{
		conn("second", time.Second, "127.0.0.1", "HTTP", "curl"), conn("first", time.Minute, "127.0.0.1", "HTTP", "curl")}}
	for i, want := range []bool{true, true, false} {
		r, ok := s.match(cs, "LANFLOW-MITM-x", "api.test", 443, t0)
		if ok != want || (ok && (r.process != "curl" || r.attribution != "certain")) {
			t.Fatalf("claim %d: ok=%v %+v", i, ok, r)
		}
	}
	if _, a := s.claimed["first"]; !a || len(s.claimed) != 2 {
		t.Fatalf("claimed %v", s.claimed)
	}
	// claims are forgotten after two minutes
	s.claimed["first"] = time.Now().Add(-3 * time.Minute)
	s.match(controller.Connections{}, "LANFLOW-MITM-x", "api.test", 443, t0)
	if _, a := s.claimed["first"]; a {
		t.Fatal("old claim kept")
	}
}

// A core that does not answer costs a connection the budget, not four
// request timeouts.
func TestSourceCheckFindIsBounded(t *testing.T) {
	for _, c := range []struct {
		name string
		dial func(ctx context.Context) (net.Conn, error)
	}{
		{"refused", func(context.Context) (net.Conn, error) { return nil, errors.New("refused") }},
		{"no answer", func(ctx context.Context) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }},
	} {
		s := newSourceCheckDial(c.dial)
		start := time.Now()
		r := s.find("LANFLOW-MITM-x", "api.test", 443)
		if d := time.Since(start); r.found || d > srcBudget+srcFetchTimeout+500*time.Millisecond {
			t.Errorf("%s: found=%v after %v", c.name, r.found, d)
		}
	}
}

func TestLoopGuardShortFlights(t *testing.T) {
	g := newLoopGuard(time.Second, 3)
	long := bytes.Repeat([]byte("GET /long-request-line "), 8) // over the prefix
	short := []byte("GET / HTTP/1.1\r\n\r\n")                  // under it
	for _, flight := range [][]byte{long, short} {
		g.markOwn(flight)
		for i, want := range []bool{false, true} { // plaintext: the second time
			br := bufioReader(flight)
			if g.isOwn(br, false) != want {
				t.Fatalf("flight of %d bytes, arrival %d: want %v", len(flight), i+1, want)
			}
		}
	}
	if g.short != 1 {
		t.Fatalf("short entries %d", g.short)
	}
	if g.isOwn(bufioReader(bytes.Repeat([]byte("POST /something-else "), 8)), false) {
		t.Fatal("a stranger's flight taken for a loop")
	}
}

func bufioReader(b []byte) *bufio.Reader {
	br := bufio.NewReaderSize(bytes.NewReader(b), 4096)
	br.Peek(len(b))
	return br
}

// pipeConn reports whether it was closed.
type pipeConn struct {
	net.Conn
	closed chan struct{}
}

func newPipeConn() *pipeConn {
	a, _ := net.Pipe()
	return &pipeConn{Conn: a, closed: make(chan struct{})}
}

func (p *pipeConn) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return p.Conn.Close()
}

func (p *pipeConn) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

// A change of the selection closes the decrypted connections it no longer
// covers, and only those.
func TestApplyClosesConnectionsNoLongerSelected(t *testing.T) {
	dom := Selector{User: "lanflow", Pass: "p", Kind: "domain"}
	base := Config{Enabled: true, Selectors: []Selector{dom}, Domains: []string{"keep.test", "drop.test", "skip.test"}}
	strs := func(s ...string) *[]string { return &s }
	yes, no := true, false
	cases := []struct {
		name   string
		update Update
		closed []string // of keep, drop, skip
	}{
		{"host removed from the selection", Update{Domains: strs("keep.test", "skip.test")}, []string{"drop"}},
		{"host added to the skip list", Update{Bypass: strs("skip.test")}, []string{"skip"}},
		{"recording paused", Update{NoRecord: &yes}, []string{"keep", "drop", "skip"}},
		{"turned off", Update{Enabled: &no}, []string{"keep", "drop", "skip"}},
		{"credentials replaced", Update{Selectors: &[]Selector{{User: "lanflow", Pass: "other", Kind: "domain"}}}, []string{"keep", "drop", "skip"}},
		{"nothing that concerns them", Update{Domains: strs("keep.test", "drop.test", "skip.test", "new.test"), NoRecord: &no}, nil},
	}
	for _, c := range cases {
		a := &Agent{active: map[*tracked]struct{}{}, pins: newPinTable()}
		a.cfg.Store(compile(base))
		conns := map[string][2]*pipeConn{}
		for _, h := range []string{"keep", "drop", "skip"} {
			req := connectReq{host: h + ".test", port: 80}
			cl, up := newPipeConn(), newPipeConn()
			conns[h] = [2]*pipeConn{cl, up}
			_, ok := a.track(a.stillSelected(dom, func(cfg *compiled) string {
				why, _ := a.decidePlain(cfg, dom, req)
				return why
			}), cl, up)
			if !ok {
				t.Fatalf("%s: %s not selected to begin with", c.name, h)
			}
		}
		a.Apply(c.update)
		var closed []string
		for _, h := range []string{"keep", "drop", "skip"} {
			cl, up := conns[h][0].isClosed(), conns[h][1].isClosed()
			if cl != up {
				t.Errorf("%s: %s: client closed=%v, upstream closed=%v", c.name, h, cl, up)
			}
			if cl {
				closed = append(closed, h)
			}
		}
		if strings.Join(closed, ",") != strings.Join(c.closed, ",") {
			t.Errorf("%s: closed %v, want %v", c.name, closed, c.closed)
		}
	}

	// a connection set up while the selection changed is refused
	a := &Agent{active: map[*tracked]struct{}{}, pins: newPinTable()}
	a.cfg.Store(compile(base))
	old := a.cfg.Load() // what the connection decided with
	a.Apply(Update{Domains: strs("keep.test")})
	req := connectReq{host: "drop.test", port: 80}
	if why, _ := a.decidePlain(old, dom, req); why != "" {
		t.Fatal(why)
	}
	done, ok := a.track(a.stillSelected(dom, func(cfg *compiled) string {
		why, _ := a.decidePlain(cfg, dom, req)
		return why
	}), newPipeConn())
	if ok {
		t.Fatal("a connection to a host removed meanwhile was accepted")
	}
	done()
	if len(a.active) != 0 {
		t.Fatal("still tracked")
	}
}

// A fault in a parser ends the records of its connection and nothing else.
func TestGuardParser(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	oldErr := os.Stderr
	os.Stderr = devnull // the report of the fault
	defer func() { os.Stderr = oldErr }()

	for _, failPanics := range []bool{false, true} {
		reqPipe, respPipe := newLossyPipe(64), newLossyPipe(64)
		failed := false
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer reqPipe.Abort()
			defer guardParser(func() {
				failed = true
				if failPanics {
					panic("fail on broken state")
				}
			}, reqPipe, respPipe)
			var p *h2Parser
			p.headers(nil, true) // nil dereference
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("parser goroutine did not end")
		}
		if !failed {
			t.Fatal("open records not finished")
		}
		for _, p := range []*lossyPipe{reqPipe, respPipe} {
			if _, err := p.Read(make([]byte, 1)); !errors.Is(err, errPipeBroken) {
				t.Fatalf("pipe not cut: %v", err)
			}
			if n, err := p.Write([]byte("still forwarded")); n != 15 || err != nil {
				t.Fatalf("the forwarding side saw the fault: %d %v", n, err)
			}
		}
	}

	// without a fault nothing is touched
	p := newLossyPipe(64)
	func() { defer guardParser(func() { t.Fatal("fail called") }, p) }()
	p.Write([]byte("x"))
	p.Close()
	if b, _ := io.ReadAll(p); string(b) != "x" {
		t.Fatal("pipe cut without a fault")
	}
}
