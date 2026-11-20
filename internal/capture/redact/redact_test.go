package redact

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"

	"lanflow/internal/model"
)

func TestHeadersAndPath(t *testing.T) {
	h := Headers([]model.KV{{Key: "Authorization", Value: "Bearer abcdefghijklmnop"}, {Key: "X-Session-Id", Value: "123456789"}, {Key: "Accept", Value: "*/*"}})
	if !strings.HasPrefix(h[0].Value, "Bear…(") || !strings.Contains(h[1].Value, "(9 B)") || h[2].Value != "*/*" {
		t.Fatal(h)
	}
	p := Path("/a?access_token=secretsecret&x=1")
	if strings.Contains(p, "secretsecret") || !strings.Contains(p, "x=1") {
		t.Fatal(p)
	}
}

func TestBodyAndStructure(t *testing.T) {
	b := Body([]byte(`{"user":"u","password":"hunter22hunter22","nested":{"refresh_token":"zzzzzzzzzz"}}`), "application/json")
	if bytes.Contains(b, []byte("hunter22")) || bytes.Contains(b, []byte("zzzzzzzzzz")) || !bytes.Contains(b, []byte(`"u"`)) {
		t.Fatal(string(b))
	}
	f := Body([]byte("a=1&client_secret=ssssssssss"), "application/x-www-form-urlencoded")
	if bytes.Contains(f, []byte("ssssssssss")) {
		t.Fatal(string(f))
	}
	st, _ := json.Marshal(Structure([]byte(`{"name":"bob","n":[1,2]}`), "application/json"))
	if strings.Contains(string(st), "bob") || !strings.Contains(string(st), `"name":"string(3)"`) {
		t.Fatal(string(st))
	}
}

func TestDecode(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte("hello"))
	w.Close()
	if b, ok := Decode(buf.Bytes(), "gzip"); !ok || string(b) != "hello" {
		t.Fatal(b, ok)
	}
	if _, ok := Decode([]byte("x"), "br"); ok {
		t.Fatal("br decoded")
	}
}

func TestSensitiveNames(t *testing.T) {
	for name, want := range map[string]bool{
		"password": true, "pass": true, "user_pass": true, "userPass": true, "PASS": true, "pwd": true, "newPwd": true, "db-pwd": true,
		"cookie": true, "Set-Cookie2": true, "credential": true, "AWSCredentials": true, "jwt": true, "id_jwt": true,
		"sid": true, "connect.sid": true, "userSid": true, "SID": true, "bearer": true, "x-bearer": true,
		"passphrase": true, "access_token": true, "apiKey": true, "X-Auth": true,
		// words that only contain the short names
		"passenger": false, "bypass": false, "compass": false, "inside": false, "side": false, "consider": false,
		"residue": false, "name": false, "id": false, "page": false, "password_hint_shown": true,
	} {
		if SensitiveKey(name) != want {
			t.Errorf("SensitiveKey(%q) = %v", name, !want)
		}
	}
	if !SensitiveHeader("Cookie") || !SensitiveHeader("X-Upstream-Sid") || SensitiveHeader("Accept-Language") {
		t.Error("SensitiveHeader")
	}
}

// A sensitive key hides its value whatever the type, and everything inside
// an object or array found there; the rest of the document is passed on
// as it was written: key order and number digits included.
func TestBodyMasksEveryValueType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		gone []string // must not appear
		kept []string // must appear
	}{
		{"number", `{"password":12345678901234,"n":7}`, []string{"12345678901234"}, []string{`"n": 7`, `(14 B)`}},
		{"array of strings", `{"tokens":["first-secret-value","second-secret-value"],"list":["shown"]}`,
			[]string{"first-secret-value", "second-secret-value"}, []string{`"shown"`}},
		{"object under a sensitive key", `{"auth":{"user":"visible-user-name","pass":"hunter22hunter22","code":424242}}`,
			[]string{"visible-user-name", "hunter22hunter22", "424242"}, []string{`"user"`, `"code"`}},
		{"deep", `{"a":[{"b":{"session":[{"x":"deep-secret-value"}]}}],"ok":"fine"}`, []string{"deep-secret-value"}, []string{`"fine"`}},
		{"short names", `{"pass":"short-name-secret","pwd":"other-name-secret","passenger":"alice"}`,
			[]string{"short-name-secret", "other-name-secret"}, []string{`"alice"`}},
		{"booleans and null stay", `{"authenticated":true,"token":null,"secret":false}`, nil, []string{`"authenticated": true`, `"token": null`, `"secret": false`}},
		{"big integers keep their digits", `{"id":9007199254740993,"price":1.10,"exp":1e3}`, nil,
			[]string{`"id": 9007199254740993`, `"price": 1.10`, `"exp": 1e3`}},
		{"empty containers", `{"a":{},"b":[],"auth":{}}`, nil, []string{`"a": {}`, `"b": []`, `"auth": {}`}},
		{"top-level array", `[{"token":"array-secret-value"},1]`, []string{"array-secret-value"}, []string{"1"}},
	}
	for _, c := range cases {
		out := string(Body([]byte(c.in), "application/json"))
		if !json.Valid([]byte(out)) {
			t.Errorf("%s: not JSON:\n%s", c.name, out)
		}
		for _, g := range c.gone {
			if strings.Contains(out, g) {
				t.Errorf("%s: %q not masked:\n%s", c.name, g, out)
			}
		}
		for _, k := range c.kept {
			if !strings.Contains(out, k) {
				t.Errorf("%s: %q missing:\n%s", c.name, k, out)
			}
		}
	}

	// keys keep the order they were sent in
	out := string(Body([]byte(`{"zeta":1,"alpha":2,"mid":{"y":1,"b":2}}`), "application/json"))
	var pos []int
	for _, k := range []string{`"zeta"`, `"alpha"`, `"mid"`, `"y"`, `"b"`} {
		pos = append(pos, strings.Index(out, k))
	}
	for i := 1; i < len(pos); i++ {
		if pos[i-1] < 0 || pos[i] < pos[i-1] {
			t.Fatalf("keys reordered:\n%s", out)
		}
	}
	if want := "{\n  \"zeta\": 1,\n  \"alpha\": 2,\n  \"mid\": {\n    \"y\": 1,\n    \"b\": 2\n  }\n}"; out != want {
		t.Fatalf("layout:\n%s", out)
	}
	// what is not JSON after all is returned as it is
	for _, in := range []string{`{"a":1} trailing`, `{"a":`, ``} {
		if got := string(Body([]byte(in), "application/json")); got != in {
			t.Errorf("Body(%q) = %q", in, got)
		}
	}
}
