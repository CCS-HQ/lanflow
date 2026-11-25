package agent

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/capture/ca"
	"lanflow/internal/model"
)

// clientHelloBytes captures the ClientHello a Go client sends.
func clientHelloBytes(t testing.TB, cfg *tls.Config) []byte {
	c1, c2 := net.Pipe()
	go func() {
		tls.Client(c1, cfg).Handshake()
		c1.Close()
	}()
	var buf bytes.Buffer
	c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 64<<10)
	n, _ := c2.Read(b)
	buf.Write(b[:n])
	// read the rest of the first flight if split
	for {
		br := bufio.NewReaderSize(bytes.NewReader(buf.Bytes()), maxHello+5)
		if _, err := peekHello(br); err == nil || buf.Len() > maxHello {
			break
		}
		n, err := c2.Read(b)
		if err != nil {
			break
		}
		buf.Write(b[:n])
	}
	c2.Close()
	return buf.Bytes()
}

func TestPeekHello(t *testing.T) {
	raw := clientHelloBytes(t, &tls.Config{ServerName: "Example.com", NextProtos: []string{"h2", "http/1.1"}})
	br := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(raw), strings.NewReader("tail")), maxHello+5)
	ch, err := peekHello(br)
	if err != nil {
		t.Fatal(err)
	}
	if ch.sni != "Example.com" || strings.Join(ch.alpn, ",") != "h2,http/1.1" || ch.ech || len(ch.random) != 32 {
		t.Fatalf("%+v", ch)
	}
	if ch.rawLen != len(raw) || br.Buffered() < len(raw) {
		t.Fatalf("rawLen %d of %d", ch.rawLen, len(raw))
	}
	// nothing consumed
	got, _ := io.ReadAll(br)
	if !bytes.Equal(got[:len(raw)], raw) {
		t.Fatal("peek consumed bytes")
	}
	// no SNI
	raw = clientHelloBytes(t, &tls.Config{InsecureSkipVerify: true})
	ch, err = peekHello(bufio.NewReaderSize(bytes.NewReader(raw), maxHello+5))
	if err != nil || ch.sni != "" {
		t.Fatalf("no-SNI hello: %v %+v", err, ch)
	}
	// truncated and garbage
	for _, b := range [][]byte{raw[:3], raw[:20], []byte("GET / HTTP/1.1\r\n"), {0x16, 3, 1, 0, 0}} {
		if _, err := peekHello(bufio.NewReaderSize(bytes.NewReader(b), maxHello+5)); err == nil {
			t.Fatalf("accepted %q", b)
		}
	}
}

func TestPeekHelloSplitRecords(t *testing.T) {
	raw := clientHelloBytes(t, &tls.Config{ServerName: "split.test"})
	body := raw[5:]
	// re-frame the handshake message as two records
	var out []byte
	for _, part := range [][]byte{body[:40], body[40:]} {
		out = append(out, 0x16, 3, 1, byte(len(part)>>8), byte(len(part)))
		out = append(out, part...)
	}
	ch, err := peekHello(bufio.NewReaderSize(bytes.NewReader(out), maxHello+5))
	if err != nil || ch.sni != "split.test" || ch.rawLen != len(out) {
		t.Fatalf("%v %+v", err, ch)
	}
}

func FuzzPeekHello(f *testing.F) {
	f.Add(clientHelloBytes(f, &tls.Config{ServerName: "a.test", NextProtos: []string{"h2"}}))
	f.Add([]byte{0x16, 3, 1, 0, 4, 1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		peekHello(bufio.NewReaderSize(bytes.NewReader(b), maxHello+5))
	})
}

func TestMatcher(t *testing.T) {
	m := newMatcher([]string{"api.example.com", "*.example.org", "+.example.net", "ocsp.*", "10.0.0.0/8", "fc00::/7", " Upper.Test. "})
	cases := map[string]bool{
		"api.example.com": true, "x.api.example.com": false, "example.com": false,
		"a.example.org": true, "a.b.example.org": true, "example.org": false,
		"example.net": true, "deep.x.example.net": true, "badexample.net": false,
		"ocsp.digicert.com": true, "xocsp.a": false,
		"10.1.2.3": true, "11.0.0.1": false, "fd00::1": true, "[fd00::1]": true,
		"upper.test": true, "UPPER.TEST.": true,
	}
	for h, want := range cases {
		if got := m.match(h) != ""; got != want {
			t.Errorf("%s: %v", h, got)
		}
	}
	if m.match("a.example.org") != "*.example.org" {
		t.Error("pattern as written")
	}
	if !singleLabel("nas") || singleLabel("a.b") || singleLabel("10.0.0.1") {
		t.Error("singleLabel")
	}
}

func TestStoreSpill(t *testing.T) {
	dir := t.TempDir()
	s := newStore(10, 1<<10, 4<<10, dir, 1<<20)
	t.Cleanup(s.close)
	var ids []int64
	for i := 0; i < 8; i++ {
		r := s.add(model.CaptureFlow{Host: "h"})
		s.appendBody(r, false, bytes.Repeat([]byte{'q'}, 300))
		s.appendBody(r, true, bytes.Repeat([]byte{byte('a' + i)}, 2000)) // truncated at 1 KiB
		s.update(r, func(d *model.CaptureFlowDetail) { d.Flow.Done = true })
		ids = append(ids, r.d.Flow.ID)
	}
	s.waitSpill()
	if memUsed, _, _ := s.stats(); memUsed > 4<<10 {
		t.Fatalf("memory %d over the limit", memUsed)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "session-*.bin"))
	if len(files) != 1 {
		t.Fatalf("spill files %v", files)
	}
	if st, _ := os.Stat(files[0]); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no Unix modes
		t.Fatalf("spill mode %v", st.Mode())
	}
	if raw, _ := os.ReadFile(files[0]); bytes.Contains(raw, bytes.Repeat([]byte{'q'}, 50)) {
		t.Fatal("spill file holds plaintext")
	}
	for i, id := range ids {
		d, ok := s.detail(id)
		if !ok || len(d.ReqBody) != 300 || len(d.RespBody) != 1<<10 || d.RespBody[0] != byte('a'+i) || d.RespBodySize != 2000 || !d.Flow.Has(model.FlowTruncated) {
			t.Fatalf("flow %d: ok=%v req=%d resp=%d size=%d", i, ok, len(d.ReqBody), len(d.RespBody), d.RespBodySize)
		}
	}
	// ring limit
	for i := 0; i < 5; i++ {
		s.add(model.CaptureFlow{})
	}
	if s.count() != 10 {
		t.Fatalf("count %d", s.count())
	}
	_, _, minID, _ := s.since(0, 0)
	if minID != ids[3] {
		t.Fatalf("minID %d", minID)
	}
	s.close()
	if files, _ := filepath.Glob(filepath.Join(dir, "session-*.bin")); len(files) != 0 {
		t.Fatal("spill file left behind")
	}
}

func TestH1Parser(t *testing.T) {
	s := newStore(100, 1<<20, 0, "", 0)
	cr := &connRec{st: s, base: model.CaptureFlow{Host: "h.test", Port: 80}}
	p := newH1Parser(cr, "http", "h.test")
	req := "POST /a HTTP/1.1\r\nHost: h.test\r\nContent-Length: 3\r\n\r\nabc" +
		"GET /b HTTP/1.1\r\nHost: h.test\r\n\r\n" +
		"HEAD /c HTTP/1.1\r\nHost: h.test\r\n\r\n"
	resp := "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 201 Created\r\nTransfer-Encoding: chunked\r\n\r\n2\r\nok\r\n0\r\nX-T: 1\r\n\r\n" +
		"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nContent-Type: text/plain\r\n\r\nhi" +
		"HTTP/1.1 200 OK\r\nContent-Length: 99\r\n\r\n"
	done := make(chan struct{})
	go func() { p.requests(strings.NewReader(req)); close(done) }()
	p.responses(strings.NewReader(resp))
	<-done
	fs, _, _, _ := s.since(0, 0)
	if len(fs) != 3 {
		t.Fatalf("%d flows", len(fs))
	}
	want := []struct {
		m, path string
		st      int
		req     int64
		resp    int64
	}{{"POST", "/a", 201, 3, 2}, {"GET", "/b", 200, 0, 2}, {"HEAD", "/c", 200, 0, 0}}
	for i, w := range want {
		f := fs[i]
		if f.Method != w.m || f.Path != w.path || f.Status != w.st || f.ReqSize != w.req || f.RespSize != w.resp || !f.Done {
			t.Fatalf("flow %d: %+v", i, f)
		}
	}
	d, _ := s.detail(fs[0].ID)
	if string(d.RespBody) != "ok" || headerValue(d.ReqHeader, "host") != "h.test" {
		t.Fatalf("%+v", d)
	}
}

func TestLoopGuard(t *testing.T) {
	g := newLoopGuard(time.Second, 3)
	hello := clientHelloBytes(t, &tls.Config{ServerName: "a"})
	g.markOwn(hello)
	br := bufio.NewReaderSize(bytes.NewReader(hello), maxHello+5)
	br.Peek(len(hello))
	if !g.isOwn(br, true) {
		t.Fatal("own ClientHello not recognised")
	}
	other := clientHelloBytes(t, &tls.Config{ServerName: "a"})
	br = bufio.NewReaderSize(bytes.NewReader(other), maxHello+5)
	br.Peek(len(other))
	if g.isOwn(br, true) {
		t.Fatal("another client's ClientHello taken for a loop")
	}
	plain := []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	g.markOwn(plain)
	for i, want := range []bool{false, true} {
		br = bufio.NewReader(bytes.NewReader(plain))
		br.Peek(len(plain))
		if g.isOwn(br, false) != want {
			t.Fatalf("plaintext match %d", i)
		}
	}
	for i := 0; i < 3; i++ {
		if g.tooFast("t:1") {
			t.Fatal("rate tripped early")
		}
	}
	if !g.tooFast("t:1") {
		t.Fatal("rate not tripped")
	}
}

func TestPinTable(t *testing.T) {
	p := newPinTable()
	if p.fail("k") || p.skipped("k") {
		t.Fatal("one alert skipped")
	}
	if !p.fail("k") || !p.skipped("k") {
		t.Fatal("two alerts did not skip")
	}
}

func wsFrame(op byte, payload []byte, mask bool) []byte {
	b := []byte{0x80 | op}
	n := len(payload)
	m := byte(0)
	if mask {
		m = 0x80
	}
	switch {
	case n < 126:
		b = append(b, m|byte(n))
	default:
		b = append(b, m|126, byte(n>>8), byte(n))
	}
	if mask {
		key := []byte{1, 2, 3, 4}
		b = append(b, key...)
		for i, c := range payload {
			b = append(b, c^key[i%4])
		}
		return b
	}
	return append(b, payload...)
}

func TestWebSocketFrames(t *testing.T) {
	s := newStore(100, 1<<20, 0, "", 0)
	cr := &connRec{st: s, base: model.CaptureFlow{Host: "ws.test", Port: 443}}
	p := newH1Parser(cr, "https", "ws.test")
	req := "GET /chat HTTP/1.1\r\nHost: ws.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"
	reqBytes := append([]byte(req), wsFrame(1, []byte("hello server"), true)...)
	reqBytes = append(reqBytes, wsFrame(8, nil, true)...)
	resp := append([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"), wsFrame(1, []byte(strings.Repeat("x", 300)), false)...)
	resp = append(resp, wsFrame(2, []byte{0, 1, 2}, false)...)
	resp = append(resp, wsFrame(8, nil, false)...)
	done := make(chan struct{})
	go func() { p.requests(bytes.NewReader(reqBytes)); close(done) }()
	p.responses(bytes.NewReader(resp))
	<-done
	fs, _, _, _ := s.since(0, 0)
	if len(fs) != 1 || fs[0].Status != 101 || !fs[0].Has(model.FlowWebSocket) {
		t.Fatalf("%+v", fs)
	}
	d, _ := s.detail(fs[0].ID)
	var texts []string
	for _, f := range d.WSFrames {
		texts = append(texts, f.Text)
	}
	if len(d.WSFrames) != 5 || !strings.Contains(strings.Join(texts, "|"), "hello server") || !strings.Contains(strings.Join(texts, "|"), strings.Repeat("x", 300)) {
		t.Fatalf("frames %+v", d.WSFrames)
	}
}

func TestStoreSearchBodies(t *testing.T) {
	dir := t.TempDir()
	s := newStore(20, 1<<20, 1024, dir, 1<<20) // small memLimit forces spill to AES-256-GCM segments
	defer s.close()

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	gw.Write([]byte(`{"msg":"hello Needle-Gzip world"}`))
	gw.Close()

	r1 := s.add(model.CaptureFlow{Host: "a.test", ContentType: "application/json"})
	s.update(r1, func(d *model.CaptureFlowDetail) {
		d.RespHeader = []model.KV{{Key: "Content-Encoding", Value: "gzip"}, {Key: "Content-Type", Value: "application/json"}}
		d.Flow.Done = true
	})
	s.appendBody(r1, true, gzBuf.Bytes())

	// Binary/image bodies must be skipped.
	r2 := s.add(model.CaptureFlow{Host: "img.test", ContentType: "image/png"})
	s.update(r2, func(d *model.CaptureFlowDetail) {
		d.RespHeader = []model.KV{{Key: "Content-Type", Value: "image/png"}}
		d.Flow.Done = true
	})
	s.appendBody(r2, true, append([]byte{0x89, 'P', 'N', 'G'}, []byte("needle-png-hidden")...))

	// Headers + request body.
	r3 := s.add(model.CaptureFlow{Host: "api.test", ContentType: "application/json"})
	s.update(r3, func(d *model.CaptureFlowDetail) {
		d.ReqHeader = []model.KV{{Key: "X-Trace-ID", Value: "Trace-Needle-42"}}
		d.Flow.Done = true
	})
	s.appendBody(r3, false, []byte(`{"payload":"needle-req"}`))

	// WebSocket text frame.
	r4 := s.add(model.CaptureFlow{Host: "ws.test"})
	s.update(r4, func(d *model.CaptureFlowDetail) {
		d.WSFrames = []model.CaptureWSFrame{{Opcode: 1, Text: "socket-needle-frame"}}
		d.Flow.Done = true
	})

	// Force spilling of r1..r3 with a large finished body.
	r5 := s.add(model.CaptureFlow{Host: "pad.test"})
	s.appendBody(r5, true, bytes.Repeat([]byte{'z'}, 2048))
	s.update(r5, func(d *model.CaptureFlowDetail) { d.Flow.Done = true })
	s.waitSpill()

	ctx := context.Background()
	if got := s.searchBodies(ctx, ""); got != nil {
		t.Fatalf("empty query = %v", got)
	}
	if got := s.searchBodies(ctx, "needle-gzip"); len(got) != 1 || got[0] != r1.d.Flow.ID {
		t.Fatalf("gzip spilled search = %v, want [%d]", got, r1.d.Flow.ID)
	}
	if got := s.searchBodies(ctx, "needle-png-hidden"); len(got) != 0 {
		t.Fatalf("image body matched unexpectedly: %v", got)
	}
	if got := s.searchBodies(ctx, "trace-needle"); len(got) != 1 || got[0] != r3.d.Flow.ID {
		t.Fatalf("header search = %v", got)
	}
	if got := s.searchBodies(ctx, "needle-req"); len(got) != 1 || got[0] != r3.d.Flow.ID {
		t.Fatalf("req body search = %v", got)
	}
	if got := s.searchBodies(ctx, "socket-needle"); len(got) != 1 || got[0] != r4.d.Flow.ID {
		t.Fatalf("ws search = %v", got)
	}

	// Control GET /search route.
	a := &Agent{st: s}
	ctl := &Control{a: a}
	req := httptest.NewRequest("GET", "/search?q=needle", nil)
	rec := httptest.NewRecorder()
	ctl.search(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	var out struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.IDs) != 3 {
		t.Fatalf("/search response %s err=%v", rec.Body.String(), err)
	}
}

// startConnectRelay runs a minimal HTTP CONNECT proxy forwarding to targetAddr.
func startConnectRelay(t *testing.T, targetAddr string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					l, err := br.ReadString('\n')
					if err != nil || l == "\r\n" {
						break
					}
				}
				dst, err := net.Dial("tcp", targetAddr)
				if err != nil {
					io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer dst.Close()
				io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() {
					io.Copy(dst, br)
					closeWrite(dst)
				}()
				io.Copy(c, dst)
				closeWrite(c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestTLSSessionResumptionBothSides(t *testing.T) {
	pki := newTestPKI(t)
	cert := pki.leaf(t, "resume.test")
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upLn.Close() })

	var upResumed atomic.Int32
	var upTotal atomic.Int32
	upCfg := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}
	go func() {
		for {
			raw, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(raw net.Conn) {
				defer raw.Close()
				tc := tls.Server(raw, upCfg)
				if err := tc.Handshake(); err != nil {
					return
				}
				defer tc.Close()
				upTotal.Add(1)
				if tc.ConnectionState().DidResume {
					upResumed.Add(1)
				}
				// Read request and send a tiny HTTP response so both TLS
				// clients (upstream agent and downstream test client) read
				// TLS 1.3 post-handshake NewSessionTicket messages.
				br := bufio.NewReader(tc)
				for {
					l, err := br.ReadString('\n')
					if err != nil || l == "\r\n" {
						break
					}
				}
				io.WriteString(tc, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}(raw)
		}
	}()

	upProxy := startConnectRelay(t, upLn.Addr().String())
	root, _, err := ca.Generate(&ca.MemKeys{}, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sel := Selector{User: "u", Pass: "p", Kind: "domain", Name: "域名"}
	a, err := New(Config{
		Selectors:   []Selector{sel},
		UpAddr:      upProxy,
		UpUser:      "up",
		UpPass:      "uppass",
		Domains:     []string{"resume.test"},
		Enabled:     true,
		NoLoopGuard: true,
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	a.SetRoots(pki.pool)
	go a.Serve()
	t.Cleanup(a.Close)

	pool := x509.NewCertPool()
	pool.AddCert(root.Cert)
	clientCache := tls.NewLRUClientSessionCache(16)
	auth := base64.StdEncoding.EncodeToString([]byte("u:p"))

	dialOnce := func(alpn []string) tls.ConnectionState {
		t.Helper()
		c, err := net.DialTimeout("tcp", a.Addr(), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "CONNECT resume.test:443 HTTP/1.1\r\nHost: resume.test:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth)
		br := bufio.NewReader(c)
		line, err := br.ReadString('\n')
		if err != nil || !strings.Contains(line, "200") {
			t.Fatalf("CONNECT: %q %v", line, err)
		}
		for l, _ := br.ReadString('\n'); l != "\r\n" && l != ""; l, _ = br.ReadString('\n') {
		}
		tc := tls.Client(&bufConn{Conn: c, r: br}, &tls.Config{
			ServerName:         "resume.test",
			RootCAs:            pool,
			NextProtos:         alpn,
			ClientSessionCache: clientCache,
		})
		if err := tc.Handshake(); err != nil {
			t.Fatalf("client handshake (%v): %v", alpn, err)
		}
		st := tc.ConnectionState()
		// Exchange an HTTP/1.1 message (sent even over h2 ALPN since this
		// test bypasses HTTP/2 framing) or read until close so TLS 1.3
		// NewSessionTicket is consumed into clientCache.
		tc.Write([]byte("GET / HTTP/1.1\r\nHost: resume.test\r\n\r\n"))
		io.ReadAll(tc)
		tc.Close()
		return st
	}

	st1 := dialOnce([]string{"h2", "http/1.1"})
	if st1.DidResume {
		t.Fatal("first handshake unexpectedly resumed")
	}
	st2 := dialOnce([]string{"h2", "http/1.1"})
	if !st2.DidResume {
		t.Fatal("downstream client TLS session did not resume on second connection")
	}
	if upResumed.Load() == 0 {
		t.Fatalf("upstream TLS session did not resume (total=%d resumed=%d)", upTotal.Load(), upResumed.Load())
	}
	// Switching ALPN on a subsequent connection still handshakes cleanly.
	st3 := dialOnce([]string{"http/1.1"})
	if st3.NegotiatedProtocol != "http/1.1" {
		t.Fatalf("ALPN switch got %q, want http/1.1", st3.NegotiatedProtocol)
	}
}

func TestHandleSkipsSourceCheckForObviousPassthrough(t *testing.T) {
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { upLn.Close() })
	go func() {
		for {
			c, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(io.Discard, c)
			}(c)
		}
	}()
	upProxy := startConnectRelay(t, upLn.Addr().String())
	root, _, err := ca.Generate(&ca.MemKeys{}, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sel := Selector{User: "u", Pass: "p", Kind: "domain", Name: "域名"}
	a, err := New(Config{
		Selectors:   []Selector{sel},
		UpAddr:      upProxy,
		UpUser:      "up",
		UpPass:      "uppass",
		Domains:     []string{"sel.test"},
		Bypass:      []string{"skip.test"},
		Enabled:     true,
		NoLoopGuard: true,
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	var ctrlCalls atomic.Int32
	a.SetController("", func(ctx context.Context) (net.Conn, error) {
		ctrlCalls.Add(1)
		return nil, fmt.Errorf("controller unreachable")
	})
	go a.Serve()
	t.Cleanup(a.Close)

	auth := base64.StdEncoding.EncodeToString([]byte("u:p"))
	send := func(host string, payload []byte) time.Duration {
		t.Helper()
		start := time.Now()
		c, err := net.DialTimeout("tcp", a.Addr(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\nProxy-Authorization: Basic %s\r\n\r\n", host, host, auth)
		br := bufio.NewReader(c)
		line, err := br.ReadString('\n')
		if err != nil || !strings.Contains(line, "200") {
			c.Close()
			t.Fatalf("CONNECT %s: %q %v", host, line, err)
		}
		for l, _ := br.ReadString('\n'); l != "\r\n" && l != ""; l, _ = br.ReadString('\n') {
		}
		c.Write(payload)
		c.Close()
		// Wait briefly for handle() to record the flow.
		deadline := time.Now().Add(2 * time.Second)
		before := a.Count()
		_ = before
		for time.Now().Before(deadline) {
			fs, _, _, _ := a.Flows(0, 0)
			if len(fs) > 0 && fs[len(fs)-1].Done {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		return time.Since(start)
	}

	selHello := clientHelloBytes(t, &tls.Config{ServerName: "sel.test"})
	otherHello := clientHelloBytes(t, &tls.Config{ServerName: "other.test"})
	skipHello := clientHelloBytes(t, &tls.Config{ServerName: "skip.test"})

	for _, tc := range []struct {
		name    string
		enabled bool
		host    string
		payload []byte
	}{
		{"non-HTTP", true, "sel.test", []byte("SSH-2.0-OpenSSH\r\n")},
		{"bad ClientHello", true, "sel.test", []byte{0x16, 3, 1, 0, 2, 1, 2}},
		{"unselected domain", true, "other.test", otherHello},
		{"bypass domain", true, "skip.test", skipHello},
		{"capture disabled", false, "sel.test", selHello},
	} {
		en := tc.enabled
		a.Apply(Update{Enabled: &en})
		a.Clear()
		ctrlCalls.Store(0)
		d := send(tc.host, tc.payload)
		if got := ctrlCalls.Load(); got != 0 {
			t.Errorf("%s: controller called %d times, want 0", tc.name, got)
		}
		if d > 150*time.Millisecond {
			t.Logf("%s: took %v", tc.name, d)
		}
	}

	// Selected host still performs sourceCheck.
	en := true
	a.Apply(Update{Enabled: &en})
	a.Clear()
	ctrlCalls.Store(0)
	send("sel.test", selHello)
	if got := ctrlCalls.Load(); got == 0 {
		t.Fatal("selected connection did not invoke sourceCheck")
	}
}
