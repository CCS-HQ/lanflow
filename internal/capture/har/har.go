// Package har writes captured requests as HAR 1.2 (docs/capture.md §7.4,
// M3): credentials masked as on screen; bodies only when asked for, and
// then with JSON / form credentials masked; Lanflow's own fields (process,
// selection, flags) under "_lanflow".
package har

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"lanflow/internal/capture/redact"
	"lanflow/internal/model"
)

type nv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type content struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type postData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	// Encoding "base64" (as in content; an extension HAR readers such as
	// the browsers' accept): the decoded body is not UTF-8 text.
	Encoding string `json:"encoding,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type request struct {
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	HTTPVersion string    `json:"httpVersion"`
	Cookies     []nv      `json:"cookies"`
	Headers     []nv      `json:"headers"`
	QueryString []nv      `json:"queryString"`
	PostData    *postData `json:"postData,omitempty"`
	HeadersSize int       `json:"headersSize"`
	BodySize    int64     `json:"bodySize"`
}

type response struct {
	Status      int     `json:"status"`
	StatusText  string  `json:"statusText"`
	HTTPVersion string  `json:"httpVersion"`
	Cookies     []nv    `json:"cookies"`
	Headers     []nv    `json:"headers"`
	Content     content `json:"content"`
	RedirectURL string  `json:"redirectURL"`
	HeadersSize int     `json:"headersSize"`
	BodySize    int64   `json:"bodySize"`
}

type timings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

type lanflowExt struct {
	Process     string   `json:"process,omitempty"`
	ProcessPath string   `json:"processPath,omitempty"`
	Attribution string   `json:"attribution,omitempty"`
	Selector    string   `json:"selector,omitempty"`
	Flags       []string `json:"flags,omitempty"`
	TLS         any      `json:"tls,omitempty"`
}

type entry struct {
	StartedDateTime string     `json:"startedDateTime"`
	Time            float64    `json:"time"`
	Request         request    `json:"request"`
	Response        response   `json:"response"`
	Cache           struct{}   `json:"cache"`
	Timings         timings    `json:"timings"`
	Lanflow         lanflowExt `json:"_lanflow"`
}

type creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Build returns the HAR document of the decrypted requests among ds.
func Build(ds []model.CaptureFlowDetail, withBodies bool, version string) ([]byte, error) {
	var buf bytes.Buffer
	w := NewWriter(&buf, version)
	for _, d := range ds {
		if err := w.Add(d, withBodies); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Writer writes a HAR document entry by entry, so an export holds one
// record's bodies at a time and never the whole document.
type Writer struct {
	w   io.Writer
	n   int
	err error
}

// NewWriter starts a HAR document on w.
func NewWriter(w io.Writer, version string) *Writer {
	hw := &Writer{w: w}
	c, _ := json.Marshal(creator{Name: "Lanflow", Version: version})
	hw.write([]byte("{\n  \"log\": {\n    \"version\": \"1.2\",\n    \"creator\": "))
	hw.write(c)
	hw.write([]byte(",\n    \"entries\": ["))
	return hw
}

func (w *Writer) write(b []byte) {
	if w.err == nil {
		_, w.err = w.w.Write(b)
	}
}

// Add writes d as an entry; records that are not decrypted requests are
// left out. Entries reports how many were written.
func (w *Writer) Add(d model.CaptureFlowDetail, withBodies bool) error {
	e, ok := build(d, withBodies)
	if !ok || w.err != nil {
		return w.err
	}
	b, err := json.MarshalIndent(e, "      ", "  ")
	if err != nil {
		return err
	}
	if w.n > 0 {
		w.write([]byte(","))
	}
	w.write([]byte("\n      "))
	w.write(b)
	w.n++
	return w.err
}

// Entries is the number of entries written so far.
func (w *Writer) Entries() int { return w.n }

// Close ends the document.
func (w *Writer) Close() error {
	if w.n > 0 {
		w.write([]byte("\n    "))
	}
	w.write([]byte("]\n  }\n}\n"))
	return w.err
}

// build makes the entry of a decrypted request.
func build(d model.CaptureFlowDetail, withBodies bool) (entry, bool) {
	f := d.Flow
	if f.Method == "" || !f.Has(model.FlowDecrypted) {
		return entry{}, false
	}
	httpVer := "HTTP/1.1"
	if f.Proto == "h2" {
		httpVer = "HTTP/2"
	}
	scheme := f.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := f.Host
	if !(scheme == "https" && f.Port == 443 || scheme == "http" && f.Port == 80) && f.Port > 0 {
		host = host + ":" + itoa(f.Port)
	}
	path := redact.Path(f.Path)
	e := entry{
		StartedDateTime: f.Start.UTC().Format(time.RFC3339Nano),
		Time:            float64(f.DurationMs),
		Request: request{Method: f.Method, URL: scheme + "://" + host + path, HTTPVersion: httpVer,
			Cookies: []nv{}, Headers: headers(d.ReqHeader), QueryString: query(path), HeadersSize: -1, BodySize: d.ReqBodySize},
		Response: response{Status: f.Status, HTTPVersion: httpVer, Cookies: []nv{}, Headers: headers(d.RespHeader),
			HeadersSize: -1, BodySize: d.RespBodySize, RedirectURL: ""},
		Timings: timings{Send: 0, Wait: float64(d.Timing.TTFBMs), Receive: float64(max(0, d.Timing.EndMs-d.Timing.TTFBMs))},
		Lanflow: lanflowExt{Process: f.Process, ProcessPath: f.ProcessPath, Attribution: f.Attribution, Selector: f.Selector, Flags: f.Flags, TLS: d.TLS},
	}
	e.Response.Content = content{Size: d.RespBodySize, MimeType: f.ContentType}
	if !withBodies {
		e.Response.Content.Comment = "bodies not exported"
		return e, true
	}
	if len(d.ReqBody) > 0 || d.ReqBodySize > 0 {
		ct := hv(d.ReqHeader, "content-type")
		pd := &postData{MimeType: ct}
		pd.Text, pd.Encoding, pd.Comment = bodyText(d.ReqBody, hvJoin(d.ReqHeader, "content-encoding"), ct)
		if d.ReqBodySize > int64(len(d.ReqBody)) {
			pd.Comment = joinComment("truncated", pd.Comment)
		}
		e.Request.PostData = pd
	}
	if len(d.RespBody) > 0 {
		c := &e.Response.Content
		c.Text, c.Encoding, c.Comment = bodyText(d.RespBody, hvJoin(d.RespHeader, "content-encoding"), f.ContentType)
		if d.RespBodySize > int64(len(d.RespBody)) {
			c.Comment = joinComment("truncated", c.Comment)
		}
	}
	return e, true
}

// bodyText is a body as HAR text: decoded and masked when it is text; as
// base64 of the decoded bytes when it is binary (as a string it would lose
// every byte that is not UTF-8); omitted when Content-Encoding cannot be
// decoded so unredacted credentials are never exported.
func bodyText(raw []byte, encoding, contentType string) (text, enc, comment string) {
	body, ok := redact.Decode(raw, encoding)
	switch {
	case !ok:
		return "", "", "content-encoding " + strings.TrimSpace(encoding) + " not decoded; body omitted"
	case !utf8.Valid(body):
		return base64.StdEncoding.EncodeToString(body), "base64", ""
	}
	return string(redact.Body(body, contentType)), "", ""
}

func joinComment(a, b string) string {
	if b == "" {
		return a
	}
	return a + "; " + b
}

func headers(h []model.KV) []nv {
	out := []nv{}
	for _, kv := range redact.Headers(h) {
		out = append(out, nv{kv.Key, kv.Value})
	}
	return out
}

func query(path string) []nv {
	out := []nv{}
	_, q, ok := strings.Cut(path, "?")
	if !ok {
		return out
	}
	for _, kv := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(kv, "=")
		out = append(out, nv{k, v})
	}
	return out
}

func hv(h []model.KV, name string) string {
	for _, kv := range h {
		if strings.EqualFold(kv.Key, name) {
			return kv.Value
		}
	}
	return ""
}

func hvJoin(h []model.KV, name string) string {
	var vals []string
	for _, kv := range h {
		if strings.EqualFold(kv.Key, name) {
			if v := strings.TrimSpace(kv.Value); v != "" {
				vals = append(vals, v)
			}
		}
	}
	return strings.Join(vals, ", ")
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
