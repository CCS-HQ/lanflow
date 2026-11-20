package har

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
)

func TestBuild(t *testing.T) {
	d := model.CaptureFlowDetail{
		Flow: model.CaptureFlow{Start: time.Now(), Method: "POST", Scheme: "https", Host: "a.test", Port: 443, Path: "/p?token=abcdefghijk&x=1",
			Status: 200, ContentType: "application/json", Proto: "h2", Flags: []string{model.FlowDecrypted}, Process: "curl"},
		ReqHeader:  []model.KV{{Key: "authorization", Value: "Bearer secretsecretsecret"}, {Key: "content-type", Value: "application/json"}},
		RespHeader: []model.KV{{Key: "content-type", Value: "application/json"}},
		ReqBody:    []byte(`{"password":"pppppppppp","q":1}`), ReqBodySize: 31,
		RespBody: []byte(`{"ok":true}`), RespBodySize: 11,
	}
	pass := model.CaptureFlowDetail{Flow: model.CaptureFlow{Host: "b.test", Flags: []string{model.FlowPassthrough}}}
	for _, bodies := range []bool{false, true} {
		b, err := Build([]model.CaptureFlowDetail{d, pass}, bodies, "test")
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		var doc struct {
			Log struct {
				Entries []map[string]any
			}
		}
		if err := json.Unmarshal(b, &doc); err != nil || len(doc.Log.Entries) != 1 {
			t.Fatalf("%v %d", err, len(doc.Log.Entries))
		}
		for _, secret := range []string{"secretsecretsecret", "abcdefghijk", "pppppppppp"} {
			if strings.Contains(s, secret) {
				t.Fatalf("bodies=%v: %s leaked", bodies, secret)
			}
		}
		if strings.Contains(s, `\"ok\": true`) != bodies || !strings.Contains(s, `"url": "https://a.test/p?token=`) || !strings.Contains(s, `"_lanflow"`) {
			t.Fatalf("bodies=%v:\n%s", bodies, s)
		}
	}
}

// Request bodies that are not text, or whose Content-Encoding cannot be
// undone here, are exported as base64 of the bytes: as a string they would
// lose every byte that is not UTF-8, or come out empty without a word.
func TestRequestBodyEncodings(t *testing.T) {
	binary := []byte{0x0a, 0x03, 0xff, 0xfe, 0x80, 0x00, 0x12}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(`{"password":"gzipped-secret-value","q":1}`))
	zw.Close()
	cases := []struct {
		name            string
		hdr             []model.KV
		body            []byte
		size            int64
		text, enc, note string
	}{
		{"binary", []model.KV{{Key: "content-type", Value: "application/grpc"}}, binary, 7, base64.StdEncoding.EncodeToString(binary), "base64", ""},
		{"brotli is not decoded", []model.KV{{Key: "content-type", Value: "application/json"}, {Key: "content-encoding", Value: "br"}},
			[]byte{0x1b, 0x03, 0x00}, 3, base64.StdEncoding.EncodeToString([]byte{0x1b, 0x03, 0x00}), "base64", "content-encoding br not decoded"},
		{"gzip is decoded and masked", []model.KV{{Key: "content-type", Value: "application/json"}, {Key: "content-encoding", Value: "gzip"}},
			gz.Bytes(), int64(gz.Len()), "", "", ""},
		{"truncated binary", []model.KV{{Key: "content-type", Value: "application/octet-stream"}}, binary, 4096, base64.StdEncoding.EncodeToString(binary), "base64", "truncated"},
		{"text", []model.KV{{Key: "content-type", Value: "text/plain"}}, []byte("héllo"), 6, "héllo", "", ""},
	}
	for _, c := range cases {
		d := model.CaptureFlowDetail{Flow: model.CaptureFlow{Method: "POST", Host: "a.test", Port: 443, Path: "/", Flags: []string{model.FlowDecrypted}},
			ReqHeader: c.hdr, ReqBody: c.body, ReqBodySize: c.size}
		b, err := Build([]model.CaptureFlowDetail{d}, true, "test")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Log struct {
				Entries []struct {
					Request struct {
						PostData struct{ Text, Encoding, Comment string }
					}
				}
			}
		}
		if err := json.Unmarshal(b, &doc); err != nil || len(doc.Log.Entries) != 1 {
			t.Fatalf("%s: %v\n%s", c.name, err, b)
		}
		pd := doc.Log.Entries[0].Request.PostData
		if c.name == "gzip is decoded and masked" {
			if strings.Contains(pd.Text, "gzipped-secret-value") || !strings.Contains(pd.Text, `"q": 1`) || pd.Encoding != "" {
				t.Errorf("%s: %+v", c.name, pd)
			}
			continue
		}
		if pd.Text != c.text || pd.Encoding != c.enc || pd.Comment != c.note {
			t.Errorf("%s: %+v", c.name, pd)
		}
	}
}

// The writer produces the document entry by entry; with no entry at all
// it is still a HAR log.
func TestWriter(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		var buf bytes.Buffer
		w := NewWriter(&buf, `v"1`)
		for i := 0; i < n; i++ {
			if err := w.Add(model.CaptureFlowDetail{Flow: model.CaptureFlow{Method: "GET", Host: "a.test", Port: 443, Path: "/", Flags: []string{model.FlowDecrypted}}}, false); err != nil {
				t.Fatal(err)
			}
			// not a request: left out
			w.Add(model.CaptureFlowDetail{Flow: model.CaptureFlow{Host: "b.test", Flags: []string{model.FlowPassthrough}}}, false)
		}
		if err := w.Close(); err != nil || w.Entries() != n {
			t.Fatalf("%v, %d entries", err, w.Entries())
		}
		var doc struct {
			Log struct {
				Version string
				Creator struct{ Name, Version string }
				Entries []map[string]any
			}
		}
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatalf("%d entries: %v\n%s", n, err, buf.Bytes())
		}
		if doc.Log.Version != "1.2" || doc.Log.Creator.Name != "Lanflow" || doc.Log.Creator.Version != `v"1` || len(doc.Log.Entries) != n || doc.Log.Entries == nil {
			t.Fatalf("%d entries: %+v", n, doc.Log)
		}
	}
	// a failing destination is reported, once and for good
	w := NewWriter(failWriter{}, "v")
	if err := w.Add(model.CaptureFlowDetail{Flow: model.CaptureFlow{Method: "GET", Flags: []string{model.FlowDecrypted}}}, false); err == nil {
		t.Fatal("write error lost")
	}
	if err := w.Close(); err == nil {
		t.Fatal("write error lost at Close")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
