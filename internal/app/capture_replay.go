package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"lanflow/internal/capture/redact"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 抓包 · 重新发送 (docs/capture.md §7.5): a recorded request, edited or
// not, sent again from the service process. It goes through the core's
// mixed port by default, so it takes the route (and, when its host is
// selected, the decryption) the original took; the request is sent as
// written: no User-Agent or Accept-Encoding is added and the response is
// not decompressed on the wire.

const (
	replayBodyCap    = 4 << 20
	replayTimeout    = 30 * time.Second
	replayMaxTimeout = 120 * time.Second
)

// hop-by-hop and framing headers the transport sets itself
var replayDrop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-connection": true, "proxy-authorization": true,
	"transfer-encoding": true, "content-length": true, "te": true, "trailer": true, "upgrade": true,
}

// ReplayRequest sends req and returns the response.
func (s *Service) ReplayRequest(req model.ReplayRequest) (res model.ReplayResult, err error) {
	defer func() { s.logOp("capture replay "+req.Method, err) }()
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return res, i18n.Error("请输入以 http:// 或 https:// 开头的完整网址。")
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	ctx, cancel := context.WithTimeout(s.rootCtx, replayDeadline(req.TimeoutMs))
	defer cancel()

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return res, err
	}
	hr.Header = http.Header{}
	for _, kv := range req.Header {
		name := strings.TrimSpace(kv.Key)
		lower := strings.ToLower(name)
		switch {
		case name == "" || strings.HasPrefix(name, ":") || replayDrop[lower]:
			continue
		case lower == "host":
			// sent as written, also when it differs from the URL's host:
			// the editor says so (抓包 · 重新发送)
			hr.Host = kv.Value
			continue
		}
		hr.Header.Add(name, kv.Value)
	}
	if _, ok := hr.Header["User-Agent"]; !ok {
		hr.Header["User-Agent"] = []string{""} // Go would add its own
	}

	tr := &http.Transport{
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
		TLSClientConfig:    &tls.Config{},
		Proxy:              nil,
	}
	defer tr.CloseIdleConnections()
	res.Via = "direct"
	if !req.Direct && s.running() {
		st := s.Status()
		if st.MixedPort > 0 {
			pu, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", st.MixedPort))
			tr.Proxy = http.ProxyURL(pu)
			res.Via = "core"
		}
	}
	cl := &http.Client{Transport: tr}
	if !req.FollowRedirects {
		cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}

	start := time.Now()
	var ttfb time.Duration
	hr = hr.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotFirstResponseByte: func() { ttfb = time.Since(start) },
	}))
	resp, err := cl.Do(hr)
	if err != nil {
		res.DurationMs = time.Since(start).Milliseconds()
		res.Error = err.Error()
		return res, nil
	}
	defer resp.Body.Close()
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, replayBodyCap+1))
	n := int64(len(raw))
	if n > replayBodyCap {
		// The rest is not read: a stream without end would run until the
		// timeout. The size is the declared one when there is one, else
		// what was received.
		raw, res.Truncated = raw[:replayBodyCap], true
		n = max(n, resp.ContentLength)
	}
	res.Status, res.StatusText, res.Proto = resp.StatusCode, http.StatusText(resp.StatusCode), resp.Proto
	res.BodySize, res.TTFBMs, res.DurationMs = n, ttfb.Milliseconds(), time.Since(start).Milliseconds()
	for k, vs := range resp.Header {
		for _, v := range vs {
			res.Header = append(res.Header, model.KV{Key: k, Value: v})
		}
	}
	sortKV(res.Header)
	res.Body = raw
	if !res.Truncated {
		if dec, ok := redact.Decode(raw, resp.Header.Get("Content-Encoding")); ok {
			res.Body = dec
		}
	}
	if rerr != nil {
		res.Error = rerr.Error()
	}
	return res, nil
}

// replayDeadline is the time a replayed request may take: the default
// when ms is not given, never more than replayMaxTimeout.
func replayDeadline(ms int) time.Duration {
	if ms <= 0 {
		return replayTimeout
	}
	return min(time.Duration(ms)*time.Millisecond, replayMaxTimeout)
}

func sortKV(kv []model.KV) {
	for i := 1; i < len(kv); i++ {
		for j := i; j > 0 && strings.ToLower(kv[j].Key) < strings.ToLower(kv[j-1].Key); j-- {
			kv[j], kv[j-1] = kv[j-1], kv[j]
		}
	}
}
