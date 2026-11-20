package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"lanflow/internal/model"
)

// h2Parser reads both directions of an HTTP/2 connection from copies of
// the bytes: one frame reader and HPACK decoder per direction, so the
// dynamic tables follow exactly what the peers sent (docs/capture.md
// §5.3). A parse error stops the record, never the forwarding.
type h2Parser struct {
	c       *connRec
	mu      sync.Mutex
	streams map[uint32]*h2stream
	// maxReq is the highest stream the request side has registered. The
	// two directions are parsed from separate copies and are not in step:
	// what the server says about a stream above it is parked until the
	// request arrives (parked, bounded by h2ParkMax bytes).
	maxReq  uint32
	parked  map[uint32][]h2event
	parkedN int
	srvEOF  bool // the server's side has ended
}

type h2stream struct {
	mu       sync.Mutex // orders the server's events on the stream
	rec      *record
	gotResp  bool
	reqDone  bool
	respDone bool
}

// h2event is something the server sent on a stream.
type h2event struct {
	headers bool
	status  int
	hdr     []model.KV
	data    []byte
	end     bool
	rst     string // RST_STREAM: how the record ends
}

func (e h2event) size() int {
	n := 64 + len(e.data)
	for _, kv := range e.hdr {
		n += len(kv.Key) + len(kv.Value)
	}
	return n
}

func newH2Parser(c *connRec) *h2Parser {
	return &h2Parser{c: c, streams: map[uint32]*h2stream{}, parked: map[uint32][]h2event{}}
}

const (
	h2MaxTable = 1 << 20
	h2ParkMax  = 256 << 10
)

func (p *h2Parser) run(r io.Reader, client bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	if client {
		pre := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(br, pre); err != nil || !bytes.Equal(pre, []byte(http2.ClientPreface)) {
			return
		}
	}
	fr := http2.NewFramer(nil, br)
	fr.SetMaxReadFrameSize(1<<24 - 1)
	dec := hpack.NewDecoder(h2MaxTable, nil)
	dec.SetAllowedMaxDynamicTableSize(h2MaxTable)
	fr.ReadMetaHeaders = dec
	fr.MaxHeaderListSize = 1 << 20
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// The server side ending closes what is open. The request
				// side may be behind: what the server said about streams
				// it has not reached stays parked for it, and its own end
				// closes the rest.
				p.mu.Lock()
				first := !client && !p.srvEOF
				p.srvEOF = p.srvEOF || !client
				over := client && p.srvEOF
				p.mu.Unlock()
				switch {
				case over:
					p.abort("")
				case first:
					p.closeOpen()
				}
				return
			}
			p.abort(fmt.Sprintf("HTTP/2 解析中止：%v", err))
			return
		}
		switch f := f.(type) {
		case *http2.MetaHeadersFrame:
			p.headers(f, client)
		case *http2.DataFrame:
			p.data(f, client)
		case *http2.RSTStreamFrame:
			// from either side: ordered with the server's other events
			p.reset(f.StreamID, f.ErrCode)
		case *http2.PushPromiseFrame:
			// keep the decoder's table in step; pushed streams are not
			// recorded (browsers have turned push off). A promise that
			// continues in CONTINUATION frames never gets here: the
			// framer refuses that sequence and the parse stops with its
			// error, so the table cannot fall out of step silently.
			if f.HeadersEnded() {
				dec.DecodeFull(f.HeaderBlockFragment())
			}
		}
	}
}

func (p *h2Parser) stream(id uint32) *h2stream {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streams[id]
}

func (p *h2Parser) headers(f *http2.MetaHeadersFrame, client bool) {
	id := f.StreamID
	var hdr []model.KV
	pseudo := map[string]string{}
	for _, h := range f.Fields {
		if h.IsPseudo() {
			pseudo[h.Name] = h.Value
			continue
		}
		hdr = append(hdr, model.KV{Key: h.Name, Value: h.Value})
	}
	if client {
		s := p.stream(id)
		if s == nil {
			rec := p.c.newFlow(pseudo[":method"], pseudo[":scheme"], pseudo[":authority"], pseudo[":path"], hdr)
			p.register(id, &h2stream{rec: rec})
		} else {
			// request trailers
			p.c.st.update(s.rec, func(d *model.CaptureFlowDetail) { d.ReqHeader = append(d.ReqHeader, hdr...) })
		}
		if f.StreamEnded() {
			p.endReq(id)
		}
		return
	}
	status, _ := strconv.Atoi(pseudo[":status"])
	p.server(id, h2event{headers: true, status: status, hdr: hdr, end: f.StreamEnded()})
}

// register makes the request's stream known and replays what the server
// already said about it.
func (p *h2Parser) register(id uint32, s *h2stream) {
	s.mu.Lock() // the server side waits until the replay is done
	defer s.mu.Unlock()
	p.mu.Lock()
	p.streams[id] = s
	if id > p.maxReq {
		p.maxReq = id
	}
	evs := p.parked[id]
	delete(p.parked, id)
	for _, e := range evs {
		p.parkedN -= e.size()
	}
	p.mu.Unlock()
	for _, e := range evs {
		p.apply(id, s, e)
	}
}

// server handles an event of the server's side: on a known stream at
// once, on one the request side has not reached yet later (register).
func (p *h2Parser) server(id uint32, e h2event) {
	p.mu.Lock()
	s := p.streams[id]
	if s == nil {
		// a stream below maxReq is over (reset, or already answered);
		// even numbers are the server's own (push)
		if id > p.maxReq && id%2 == 1 && p.parkedN+e.size() <= h2ParkMax {
			e.data = append([]byte(nil), e.data...) // the framer reuses its buffer
			p.parked[id] = append(p.parked[id], e)
			p.parkedN += e.size()
		}
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	p.apply(id, s, e)
}

// apply records a server event; the caller holds s.mu.
func (p *h2Parser) apply(id uint32, s *h2stream, e h2event) {
	if e.rst != "" {
		p.mu.Lock()
		delete(p.streams, id)
		p.mu.Unlock()
		p.c.finish(s.rec, e.rst)
		return
	}
	if s.respDone {
		return // kept for the rest of the request only
	}
	if e.headers {
		switch {
		case e.status >= 100 && e.status < 200:
			// interim
		case !s.gotResp && e.status > 0:
			s.gotResp = true
			p.c.respHeaders(s.rec, e.status, e.hdr)
		default:
			// response trailers
			p.c.st.update(s.rec, func(d *model.CaptureFlowDetail) { d.RespHeader = append(d.RespHeader, e.hdr...) })
		}
	}
	if len(e.data) > 0 {
		p.c.st.appendBody(s.rec, true, e.data)
	}
	if e.end {
		p.endResp(id)
	}
}

func (p *h2Parser) data(f *http2.DataFrame, client bool) {
	if !client {
		if len(f.Data()) > 0 || f.StreamEnded() {
			p.server(f.StreamID, h2event{data: f.Data(), end: f.StreamEnded()})
		}
		return
	}
	s := p.stream(f.StreamID)
	if s == nil {
		return
	}
	if d := f.Data(); len(d) > 0 {
		p.c.st.appendBody(s.rec, false, d)
	}
	if f.StreamEnded() {
		p.endReq(f.StreamID)
	}
}

func (p *h2Parser) endReq(id uint32) {
	p.mu.Lock()
	s := p.streams[id]
	if s != nil {
		s.reqDone = true
		if s.respDone {
			delete(p.streams, id)
		}
	}
	p.mu.Unlock()
}

func (p *h2Parser) endResp(id uint32) {
	p.mu.Lock()
	s := p.streams[id]
	if s != nil {
		s.respDone = true
		// The record is finished with the response, but the stream is
		// kept while the request is not over: its body may still be
		// arriving (or, the request side being behind, be parsed later).
		if s.reqDone {
			delete(p.streams, id)
		}
	}
	p.mu.Unlock()
	if s != nil {
		p.c.finish(s.rec, "")
	}
}

func (p *h2Parser) reset(id uint32, code http2.ErrCode) {
	p.server(id, h2event{rst: "RST_STREAM " + code.String()})
}

// closeOpen finishes the streams open now (the server's side has ended).
func (p *h2Parser) closeOpen() {
	p.mu.Lock()
	ss := p.streams
	p.streams = map[uint32]*h2stream{}
	p.mu.Unlock()
	for _, s := range ss {
		p.c.finish(s.rec, "连接已关闭")
	}
}

// abort finishes every open stream (parse failure or end of connection).
func (p *h2Parser) abort(msg string) {
	p.mu.Lock()
	ss := p.streams
	p.streams = map[uint32]*h2stream{}
	p.parked, p.parkedN = map[uint32][]h2event{}, 0
	p.mu.Unlock()
	for _, s := range ss {
		if msg == "" || msg == "HTTP/2 解析中止：EOF" {
			p.c.finish(s.rec, "连接已关闭")
		} else {
			p.c.finish(s.rec, msg)
		}
	}
}
