package app

import (
	"bufio"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"lanflow/internal/capture/ca"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// testAgent makes the test binary the capture agent, with its keys in a
// directory (never the Keychain), as TestCaptureLifecycle does.
func testAgent(t *testing.T) (keys string) {
	t.Helper()
	keys = t.TempDir()
	old := CaptureAgentCommand
	CaptureAgentCommand = func() (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "LANFLOW_TEST_CAPTURE_KEYS="+keys)
		return cmd, nil
	}
	t.Cleanup(func() { CaptureAgentCommand = old })
	return keys
}

func capState(s *Service) (root, leaf *x509.Certificate, trusted bool) {
	s.capst.mu.Lock()
	defer s.capst.mu.Unlock()
	return s.capst.caCert, s.capst.trustLeaf, s.capst.trusted
}

// The trust-check leaf must outlive the seven days of a served leaf: the
// window checks the root's trust with it for as long as the root is
// installed. Every start of the agent brings a new one, and a leaf that is
// missing or expired (one stored by an earlier version) is replaced before
// the root is reported as untrusted.
func TestCaptureTrustLeafLifetime(t *testing.T) {
	testAgent(t)
	ft := &fakeTrust{installed: map[string]bool{}}
	defer ft.hooks()()
	h := newHarness(t, nil)
	enableCapture(t, h)
	s := h.s
	if err := s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCaptureSettings(model.CaptureSettings{Domains: []string{"api.example.com"}, Acknowledged: true, AutoOffMin: -1}); err != nil {
		t.Fatal(err)
	}
	s.capLoad()
	if err := installWithIssuer(s); err != nil {
		t.Fatal(err)
	}
	root, leaf, trusted := capState(s)
	if !trusted || leaf == nil {
		t.Fatalf("after install: trusted=%v leaf=%v", trusted, leaf != nil)
	}
	if !leaf.NotAfter.Equal(root.NotAfter) {
		t.Fatalf("trust leaf ends %v, the root %v: after the leaf the root would read as untrusted", leaf.NotAfter, root.NotAfter)
	}
	leafFile := filepath.Join(h.dir, "capture", captureLeafFile)
	if b, _ := os.ReadFile(leafFile); string(b) != string(leaf.Raw) {
		t.Fatal("leaf file is not the leaf in use")
	}

	// the leaf of an earlier version is gone or expired: turning on gets a
	// new one from the agent instead of asking for the root to be installed
	s.capst.mu.Lock()
	s.capst.trustLeaf, s.capst.trusted = nil, false
	s.capst.mu.Unlock()
	os.Remove(leafFile)
	installs := ft.installs
	if err := s.SetCapture(true); err != nil {
		t.Fatalf("turning on with a stale trust leaf: %v", err)
	}
	_, leaf2, trusted := capState(s)
	if !trusted || leaf2 == nil || !leaf2.NotAfter.Equal(root.NotAfter) || ft.installs != installs {
		t.Fatalf("after turning on: trusted=%v leaf=%v installs %d→%d", trusted, leaf2 != nil, installs, ft.installs)
	}
	if b, _ := os.ReadFile(leafFile); string(b) != string(leaf2.Raw) {
		t.Fatal("leaf file not rewritten")
	}
	if st := s.CaptureStatus(); st.CA != model.CATrusted || !st.Enabled {
		t.Fatalf("%+v", st)
	}

	// every agent start refreshes it
	if err := s.SetCapture(false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCapture(true); err != nil {
		t.Fatal(err)
	}
	_, leaf3, trusted := capState(s)
	if !trusted || leaf3 == nil || leaf3.SerialNumber.Cmp(leaf2.SerialNumber) == 0 {
		t.Fatalf("leaf not refreshed by the agent's start: trusted=%v", trusted)
	}
	// a leaf of another root is not taken
	other, _, _ := ca.Generate(&ca.MemKeys{}, "other", time.Now())
	der, _ := other.TrustLeaf()
	s.capSetLeaf(base64.StdEncoding.EncodeToString(der))
	if _, l, _ := capState(s); l != leaf3 {
		t.Fatal("a leaf signed by another root replaced the stored one")
	}
	s.capRecheckTrust()
	if _, _, trusted := capState(s); !trusted {
		t.Fatal("hourly check turned the root untrusted")
	}
}

// A root that is replaced is taken out of the trust store on every path
// that replaces it, also when the agent does it at a start and when the
// new root cannot be installed.
func TestCaptureReplacedRootIsRemoved(t *testing.T) {
	for _, path := range []string{"agent start", "install fails"} {
		t.Run(path, func(t *testing.T) {
			keys := testAgent(t)
			ft := &fakeTrust{installed: map[string]bool{}}
			defer ft.hooks()()
			h := newHarness(t, nil)
			enableCapture(t, h)
			s := h.s
			if err := s.StartCore(); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveCaptureSettings(model.CaptureSettings{Domains: []string{"api.example.com"}, Acknowledged: true, AutoOffMin: -1}); err != nil {
				t.Fatal(err)
			}
			s.capLoad()
			if err := installWithIssuer(s); err != nil {
				t.Fatal(err)
			}
			old, _, _ := capState(s)
			oldFP := ca.Fingerprint(old.Raw)
			if !ft.installed[oldFP] {
				t.Fatal("root not installed")
			}
			// the key is lost: the agent makes a new root
			ents, _ := os.ReadDir(keys)
			for _, e := range ents {
				os.Remove(filepath.Join(keys, e.Name()))
			}
			removes := ft.removes
			var err error
			if path == "agent start" {
				err = s.SetCapture(true)
			} else {
				oldInstall := TrustHooks.Install
				TrustHooks.Install = func([]byte) error { return errors.New("denied by policy") }
				err = s.InstallCACert()
				TrustHooks.Install = oldInstall
			}
			if err == nil {
				t.Fatal("no error although the root was replaced / not installed")
			}
			now, _, _ := capState(s)
			if now == nil || ca.Fingerprint(now.Raw) == oldFP {
				t.Fatalf("root not replaced (%v)", err)
			}
			ft.mu.Lock()
			still, removed := ft.installed[oldFP], ft.removes-removes
			ft.mu.Unlock()
			if still || removed != 1 {
				t.Fatalf("the replaced root is still trusted (removes %d): %v", removed, err)
			}
			if ents, _ := os.ReadDir(keys); len(ents) != 1 {
				t.Fatalf("%d keys kept", len(ents))
			}
			if st := s.CaptureStatus(); st.Enabled || st.CA != model.CAUntrusted {
				t.Fatalf("%+v", st)
			}
		})
	}
}

func TestCaptureRemoveBackoff(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	fail := errors.New("authorization cancelled")
	fails, next := 0, time.Time{}
	for i, want := range []time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
		fails, next = captureRemoveBackoff(fails, fail, now)
		if fails != i+1 || next.Sub(now) != want {
			t.Fatalf("failure %d: fails=%d wait=%v, want %v", i+1, fails, next.Sub(now), want)
		}
	}
	if fails, next = captureRemoveBackoff(fails, nil, now); fails != 0 || !next.IsZero() {
		t.Fatalf("after success: %d %v", fails, next)
	}
}

// A failed automatic removal is not tried again every hour.
func TestCaptureAutoRemoveBacksOff(t *testing.T) {
	testAgent(t)
	ft := &fakeTrust{installed: map[string]bool{}}
	defer ft.hooks()()
	h := newHarness(t, nil)
	s := h.s
	s.capLoad()
	if err := installWithIssuer(s); err != nil {
		t.Fatal(err)
	}
	tries := 0
	TrustHooks.Remove = func([]byte) error { tries++; return errors.New("authorization cancelled") }
	s.capst.mu.Lock()
	s.capst.set.LastDisabled = time.Now().Add(-8 * 24 * time.Hour)
	s.capst.mu.Unlock()
	for i := 0; i < 5; i++ { // five hourly ticks
		s.captureAutoRemove()
	}
	if tries != 1 {
		t.Fatalf("asked the system %d times", tries)
	}
	if st := s.CaptureStatus(); st.CA == model.CANone {
		t.Fatal("root dropped although its removal failed")
	}
	// once the wait is over it is tried again, and a success clears it
	TrustHooks.Remove = func([]byte) error { tries++; return nil }
	s.capst.mu.Lock()
	s.capst.removeNext = time.Now().Add(-time.Minute)
	s.capst.mu.Unlock()
	s.captureAutoRemove()
	s.capst.mu.Lock()
	fails := s.capst.removeFails
	s.capst.mu.Unlock()
	if tries != 2 || fails != 0 || s.CaptureStatus().CA != model.CANone {
		t.Fatalf("tries=%d fails=%d %+v", tries, fails, s.CaptureStatus().CA)
	}
}

// The export fetches and writes one record at a time, leaves out what is
// not a decrypted request, counts what could not be fetched and never
// holds a body the caller did not ask for.
func TestExportHARStreams(t *testing.T) {
	dec := []string{model.FlowDecrypted}
	flows := []model.CaptureFlow{
		{ID: 1, Method: "GET", Host: "a.test", Port: 443, Path: "/one", Flags: dec},
		{ID: 2, Host: "b.test", Flags: []string{model.FlowPassthrough}}, // a connection, not a request
		{ID: 3, Method: "POST", Host: "a.test", Port: 443, Path: "/gone", Flags: dec},
		{ID: 4, Method: "POST", Host: "a.test", Port: 443, Path: "/two", Flags: dec},
	}
	for _, bodies := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "out.har")
		var asked []int64
		inFlight, maxInFlight := 0, 0
		skipped, err := exportHAR(path, flows, bodies, "test", func(id int64) (model.CaptureFlowDetail, error) {
			asked = append(asked, id)
			if id == 3 {
				return model.CaptureFlowDetail{}, errCaptureNotFound
			}
			inFlight++
			maxInFlight = max(maxInFlight, inFlight)
			defer func() { inFlight-- }()
			f := flows[id-1]
			return model.CaptureFlowDetail{Flow: f, RespBody: []byte("response-body-" + f.Path), RespBodySize: 20,
				RespHeader: []model.KV{{Key: "content-type", Value: "text/plain"}}}, nil
		})
		if err != nil || skipped != 1 {
			t.Fatalf("bodies=%v: skipped=%d err=%v", bodies, skipped, err)
		}
		if fmt.Sprint(asked) != "[1 3 4]" || maxInFlight != 1 {
			t.Fatalf("bodies=%v: fetched %v, %d at a time", bodies, asked, maxInFlight)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Log struct {
				Entries []struct {
					Request  struct{ URL string }
					Response struct{ Content struct{ Text string } }
				}
			}
		}
		if err := json.Unmarshal(b, &doc); err != nil || len(doc.Log.Entries) != 2 {
			t.Fatalf("bodies=%v: %v\n%s", bodies, err, b)
		}
		if doc.Log.Entries[0].Request.URL != "https://a.test/one" || doc.Log.Entries[1].Request.URL != "https://a.test/two" {
			t.Fatalf("bodies=%v: %+v", bodies, doc.Log.Entries)
		}
		if got := doc.Log.Entries[1].Response.Content.Text; (got == "response-body-/two") != bodies || strings.Contains(string(b), "response-body") != bodies {
			t.Fatalf("bodies=%v: text %q", bodies, got)
		}
		if st, _ := os.Stat(path); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
			t.Fatalf("mode %v", st.Mode())
		}
		if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
			t.Fatalf("temp files left: %v", ents)
		}
	}
	// a destination that cannot be written is an error
	blocker := filepath.Join(t.TempDir(), "a-file")
	os.WriteFile(blocker, nil, 0o600)
	if _, err := exportHAR(filepath.Join(blocker, "out.har"), flows[:1], false, "test",
		func(int64) (model.CaptureFlowDetail, error) { return model.CaptureFlowDetail{Flow: flows[0]}, nil }); err == nil {
		t.Fatal("no error for a path that cannot be written")
	}
}

// A response larger than the cap is cut without reading the rest: its
// size is the declared one, or what was received, and an endless stream
// does not run until the timeout.
func TestReplayTruncatedBody(t *testing.T) {
	chunk := strings.Repeat("x", 64<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total := int64(replayBodyCap + 1<<20)
		if r.URL.Path == "/sized" {
			w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
		}
		var n int64
		for r.URL.Path == "/endless" || n < total {
			m, err := io.WriteString(w, chunk)
			n += int64(m)
			if err != nil {
				break
			}
		}
	}))
	defer srv.Close()
	h := newHarness(t, nil)
	for _, c := range []struct {
		path string
		size func(n int64) bool
	}{
		{"/sized", func(n int64) bool { return n == replayBodyCap+1<<20 }},
		{"/chunked", func(n int64) bool { return n > replayBodyCap && n <= replayBodyCap+1<<20 }},
		{"/endless", func(n int64) bool { return n > replayBodyCap }},
	} {
		start := time.Now()
		res, err := h.s.ReplayRequest(model.ReplayRequest{URL: srv.URL + c.path, Direct: true, TimeoutMs: 20000})
		if err != nil || res.Error != "" {
			t.Fatalf("%s: %v %s", c.path, err, res.Error)
		}
		if !res.Truncated || len(res.Body) != replayBodyCap || !c.size(res.BodySize) {
			t.Errorf("%s: truncated=%v body=%d size=%d", c.path, res.Truncated, len(res.Body), res.BodySize)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s: took %v", c.path, d)
		}
	}
	// under the cap nothing changes
	small := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tiny") }))
	defer small.Close()
	res, _ := h.s.ReplayRequest(model.ReplayRequest{URL: small.URL, Direct: true})
	if res.Truncated || res.BodySize != 4 || string(res.Body) != "tiny" {
		t.Fatalf("%+v", res)
	}
}

func TestReplayDeadline(t *testing.T) {
	for ms, want := range map[int]time.Duration{
		0: replayTimeout, -5: replayTimeout, 2000: 2 * time.Second, 120000: 120 * time.Second,
		120001: replayMaxTimeout, 86400000: replayMaxTimeout, 1 << 40: replayMaxTimeout,
	} {
		if got := replayDeadline(ms); got != want {
			t.Errorf("replayDeadline(%d) = %v, want %v", ms, got, want)
		}
	}
}

// upListener is a stand-in for the core's return listener: it answers the
// CONNECT as told and then connects to the target or hangs up.
func upListener(t *testing.T, pass string, answer func(target string) (status string, dial string)) *captureRun {
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
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				req.Header.Set("Authorization", req.Header.Get("Proxy-Authorization"))
				user, got, _ := req.BasicAuth()
				if req.Method != http.MethodConnect || user != "lanflow" || got != pass {
					io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
					return
				}
				status, dial := answer(req.Host)
				fmt.Fprintf(c, "HTTP/1.1 %s\r\n\r\n", status)
				if dial == "" {
					return
				}
				up, err := net.Dial("tcp", dial)
				if err != nil {
					return
				}
				defer up.Close()
				go io.Copy(up, br)
				io.Copy(c, up)
			}()
		}
	}()
	return &captureRun{upPort: ln.Addr().(*net.TCPAddr).Port, upPass: pass}
}

func TestProbeUpstream(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})) // a certificate no system trusts
	defer secure.Close()
	addr := func(s *httptest.Server) string {
		return strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://")
	}

	closedPort := func() int {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}()
	var seen string // the target of the CONNECT that got through
	cases := []struct {
		name   string
		run    *captureRun
		tls    bool
		reason string
		stage  string
	}{
		{"plain HTTP reaches the target", upListener(t, "pw", func(target string) (string, string) {
			seen = target
			return "200 Connection established", addr(plain)
		}), false, "", ""},
		{"return listener is not there", &captureRun{upPort: closedPort, upPass: "pw"}, true, model.CaptureReasonUpConnect, "上游连接"},
		{"return listener refuses the credentials", func() *captureRun {
			r := upListener(t, "pw", func(string) (string, string) { return "200 Connection established", addr(plain) })
			r.upPass = "wrong"
			return r
		}(), true, model.CaptureReasonUpConnect, "上游连接"},
		{"core cannot route", upListener(t, "pw", func(string) (string, string) { return "502 Bad Gateway", "" }), true, model.CaptureReasonUpConnect, "上游连接"},
		{"target unreachable: closed right after the CONNECT", upListener(t, "pw", func(string) (string, string) { return "200 Connection established", "" }), true, model.CaptureReasonUpConnect, "上游连接"},
		{"plain target unreachable", upListener(t, "pw", func(string) (string, string) { return "200 Connection established", "" }), false, model.CaptureReasonUpConnect, "上游连接"},
		{"server certificate not trusted: the server's problem, not the root's", upListener(t, "pw", func(string) (string, string) { return "200 Connection established", addr(secure) }), true, model.CaptureReasonUpTLS, "上游 TLS"},
	}
	for _, c := range cases {
		err := probeUpstream(c.run, "probe.test", 8443, c.tls)
		switch {
		case c.reason == "" && err != nil:
			t.Errorf("%s: %+v", c.name, err)
		case c.reason == "" && seen != "probe.test:8443":
			t.Errorf("%s: CONNECT to %q", c.name, seen)
		case c.reason != "" && (err == nil || err.reason != c.reason || err.stage != i18n.T(c.stage) || err.msg == "" || err.advice == ""):
			t.Errorf("%s: %+v", c.name, err)
		}
	}
}

// The check names the first thing in the way and lists every stage.
func TestCheckCaptureTarget(t *testing.T) {
	ft := &fakeTrust{installed: map[string]bool{}}
	defer ft.hooks()()
	h := newHarness(t, nil)
	s := h.s
	if err := s.StartCore(); err != nil {
		t.Fatal(err)
	}
	s.capLoad()
	stage := func(chk model.CaptureCheck, name string) (model.CaptureCheckStep, bool) {
		for _, st := range chk.Steps {
			if st.Stage == i18n.T(name) {
				return st, true
			}
		}
		return model.CaptureCheckStep{}, false
	}
	// addresses, so that nothing is asked of a DNS server
	chk := s.CheckCaptureTarget("  ")
	if chk.Reason != model.CaptureReasonNotMatched || len(chk.Steps) != 1 || chk.Steps[0].OK {
		t.Fatalf("empty target: %+v", chk)
	}
	chk = s.CheckCaptureTarget("https://203.0.113.7:8443/path")
	if chk.Host != "203.0.113.7" || chk.Port != 8443 || chk.Scheme != "https" || chk.Reason != model.CaptureReasonDisabled {
		t.Fatalf("off: %+v", chk)
	}
	if st, ok := stage(chk, "上游连接"); !ok || !st.Skip {
		t.Fatalf("upstream stage while off: %+v", chk.Steps)
	}

	// on (as far as the check can tell), root trusted
	s.capst.mu.Lock()
	s.capst.enabled, s.capst.trusted = true, true
	s.capst.set.Ports = "443/8000-8100"
	s.capst.flows = []model.CaptureFlow{{ID: 1, Host: "203.0.113.7", Port: 8443, Error: "不在选择范围内", Flags: []string{model.FlowPassthrough}}}
	s.capst.mu.Unlock()
	defer func() {
		s.capst.mu.Lock()
		s.capst.enabled = false
		s.capst.mu.Unlock()
	}()
	chk = s.CheckCaptureTarget("203.0.113.7:8443")
	if chk.Reason != model.CaptureReasonNotMatched {
		t.Fatalf("unselected: %+v", chk)
	}
	for name, ok := range map[string]bool{"运行状态": true, "匹配": false, "端口": false, "跳过列表": true, "DNS": true, "请求记录": false} {
		st, found := stage(chk, name)
		if !found || st.OK != ok {
			t.Errorf("stage %s: found=%v %+v", name, found, st)
		}
	}
	if len(chk.Addrs) != 1 || chk.Addrs[0] != "203.0.113.7" {
		t.Errorf("addrs %v", chk.Addrs)
	}
	if st, _ := stage(chk, "端口"); !strings.Contains(st.Detail, "8443") {
		t.Errorf("port stage %+v", st)
	}
	chk = s.CheckCaptureTarget("203.0.113.7:8050")
	if st, _ := stage(chk, "端口"); !st.OK {
		t.Errorf("port in a range refused: %+v", st)
	}
	// the root's trust is named when nothing else was in the way first
	s.capst.mu.Lock()
	s.capst.trusted = false
	s.capst.mu.Unlock()
	if chk = s.CheckCaptureTarget("203.0.113.7:443"); chk.Reason != model.CaptureReasonCA {
		t.Fatalf("untrusted root: %+v", chk)
	}
}
