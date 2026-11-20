package applog

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"更新订阅 https://sub.example.com/api/v1/client/subscribe?token=abc123 失败": "更新订阅 https://sub.example.com/*** 失败",
		"GET https://example.com/ ok":                       "GET https://example.com/ ok",
		"url=https://user:pw@host.example:8443/x":           "url=https://***@host.example:8443/***",
		"token=abc&x=1 secret: s3cr3t password=\"hunter2\"": "token=***&x=1 secret: *** password=\"***\"",
		"版本 v1.19.33 在 127.0.0.1:7890":                      "版本 v1.19.33 在 127.0.0.1:7890",
		"monkey=banana":                                     "monkey=banana",
		"url: enc:v1:q83vEjRWeJA+/w== 已加载":                  "url: enc:v1:*** 已加载",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestLoggerFileAndRing(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	l.OnWrite(func() { calls++ })
	l.Infof("启动内核 %s", "v1")
	l.Errorf("订阅 https://a.example/sub?token=x 失败\n第二行")
	if calls != 2 {
		t.Fatal("onWrite", calls)
	}
	es := l.Entries()
	if len(es) != 2 || es[1].Level != Error || strings.Contains(es[1].Payload, "token") || strings.Contains(es[1].Payload, "\n") {
		t.Fatalf("entries %+v", es)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 2 || !strings.Contains(lines[0], "INFO") || !strings.Contains(lines[1], "ERROR") {
		t.Fatalf("file:\n%s", data)
	}
	l.Clear()
	if len(l.Entries()) != 0 {
		t.Fatal("clear")
	}

	// standard log package
	std := log.New(l.Writer(), "", log.LstdFlags)
	std.Printf("lanflow: tray: failed to load")
	if es := l.Entries(); len(es) != 1 || es[0].Payload != "tray: failed to load" || es[0].Level != Warn {
		t.Fatalf("std entry %+v", es)
	}
	l.Close()

	var nilL *Logger
	nilL.Infof("x")
	if nilL.Entries() != nil || nilL.Path() != "" {
		t.Fatal("nil logger")
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	defer l.Close()
	big := strings.Repeat("x", 1<<20)
	for i := 0; i < 6; i++ {
		l.Infof("%s", big)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".1")); err != nil {
		t.Fatal("not rotated")
	}
	st, _ := os.Stat(filepath.Join(dir, FileName))
	if st.Size() > maxSize {
		t.Fatal("too big", st.Size())
	}
	if len(l.Entries()) != 6 {
		t.Fatal(len(l.Entries()))
	}
}

// TestRedactShapes: the shapes credentials take in logs, errors and
// configuration dumps, and ordinary lines that must stay readable.
func TestRedactShapes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// JSON: the key is quoted.
		{"json", `{"password":"hunter2","uuid":"0f3c-11aa","port":443}`, `{"password":"***","uuid":"***","port":443}`},
		{"json spaced", `{ "access_token" : "abc.def" , "name": "n" }`, `{ "access_token" : "***" , "name": "n" }`},
		{"json escaped", `body="{\"token\":\"abc123\",\"ok\":true}"`, `body="{\"token\":\"***\",\"ok\":true}"`},
		{"json number", `{"sid":12345}`, `{"sid":***}`},
		// A quoted value is masked whole, not up to the first space.
		{"quoted words", `password: "two words here"`, `password: "***"`},
		{"single quoted", `secret='a b c' next`, `secret='***' next`},
		// YAML of a node.
		{"yaml", "- {name: hk, type: trojan, server: a.example, password: p4ss, sni: b.example}", "- {name: hk, type: trojan, server: a.example, password: ***, sni: b.example}"},
		{"private-key", "private-key: aGVsbG8gd29ybGQ= pre-shared-key: eHl6", "private-key: *** pre-shared-key: ***"},
		{"api key", "X-Api-Key: 12345 api_key=abc", "X-Api-Key: *** api_key=***"},
		// Share links: everything in them is a credential or the node.
		{"trojan", "解析 trojan://p4ssw0rd@host.example:443?sni=x#香港 失败", "解析 trojan://*** 失败"},
		{"ss", "ss://YWVzLTI1Ni1nY206cGFzcw@1.2.3.4:8388#node", "ss://***"},
		{"vmess", "vmess://eyJ2IjoiMiIsImlkIjoiMTIzIn0=", "vmess://***"},
		{"vless", "vless://0f3c11aa-0000-4000-8000-000000000000@h.example:443?security=reality", "vless://***"},
		{"hysteria2", "hysteria2://letmein@h.example:8443/?insecure=1", "hysteria2://***"},
		{"hy2", "hy2://letmein@h.example:8443", "hy2://***"},
		{"tuic", "tuic://uuid:pw@h.example:443?alpn=h3", "tuic://***"},
		{"quoted share link", `url="trojan://pw@h:443" ok`, `url="trojan://***" ok`},
		// Other URLs keep the host.
		{"socks5", "dial socks5://user:pw@127.0.0.1:1080 failed", "dial socks5://***@127.0.0.1:1080 failed"},
		{"slash in password", "https://user:pa/ss@host.example/x?y=1", "https://***@host.example/***"},
		{"dns", "nameserver tls://8.8.8.8:853 与 https://dns.example/dns-query", "nameserver tls://8.8.8.8:853 与 https://dns.example/***"},
		{"ipv6", "GET http://[::1]:9090/version", "GET http://[::1]:9090/***"},
		// Headers.
		{"bearer header", "Authorization: Bearer abc.def.ghi", "Authorization: Bearer ***"},
		{"basic header", "authorization: Basic dXNlcjpwYXNz", "authorization: Basic ***"},
		{"proxy auth", `"Proxy-Authorization":"Basic dXNlcjpwYXNz"`, `"Proxy-Authorization":"Basic ***"`},
		{"bare token", "Authorization=abcdef012345", "Authorization=***"},
		{"bearer alone", "curl -H 'bearer abcdef0123456789'", "curl -H 'bearer ***'"},
		{"basic alone", "sent Basic dXNlcjpwYXNz to proxy", "sent Basic *** to proxy"},
		{"cookie", "cookie: session=abcdef; theme=dark", "cookie: ***"},
		{"set-cookie", "Set-Cookie: sid=abc; Path=/\n下一行", "Set-Cookie: ***\n下一行"},
		// Ordinary lines.
		{"key as a word", "press any key to continue; the key is missing", "press any key to continue; the key is missing"},
		{"hotkey", "hotkey: Cmd+Shift+L registered, monkey=banana", "hotkey: Cmd+Shift+L registered, monkey=banana"},
		{"basic as a word", "Basic settings loaded, basic authentication failed", "Basic settings loaded, basic authentication failed"},
		{"bearer as a word", "the bearer token expired", "the bearer token expired"},
		{"addresses", "mixed-port 127.0.0.1:7890, controller 127.0.0.1:9090, fd00::1", "mixed-port 127.0.0.1:7890, controller 127.0.0.1:9090, fd00::1"},
		{"paths", "open /Users/me/Library/Lanflow/config.yaml: no such file; C:\\Users\\me\\x", "open /Users/me/Library/Lanflow/config.yaml: no such file; C:\\Users\\me\\x"},
		{"host kept", "GET https://example.com/ ok, 连接 api.example.com:443 超时", "GET https://example.com/ ok, 连接 api.example.com:443 超时"},
		{"tokens count", "tokens=5 passes: 3 keys: 2 bypass: off", "tokens=5 passes: 3 keys: 2 bypass: off"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("%s: Redact(%q)\n got %q\nwant %q", c.name, c.in, got, c.want)
		}
		// Redacting twice changes nothing more.
		if once := Redact(c.in); Redact(once) != once {
			t.Errorf("%s: not idempotent: %q → %q", c.name, once, Redact(once))
		}
	}
}

// blockRotation makes path+".1" something a file cannot be renamed onto
// and that cannot be removed: a directory that is not empty.
func blockRotation(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path+".1", "held"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestRotateBlocked: when the full file cannot be moved aside it is
// emptied; the log stays bounded.
func TestRotateBlocked(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	defer l.Close()
	blockRotation(t, l.Path())
	big := strings.Repeat("x", 1<<20)
	for i := 0; i < 14; i++ {
		l.Infof("%s", big)
	}
	st, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > maxSize {
		t.Fatalf("log grew to %d bytes with rotation blocked", st.Size())
	}
	if st.Size() == 0 {
		t.Fatal("nothing was written after the file was emptied")
	}
}

func TestRotateHelper(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.log")
	if err := Rotate(p); err != nil {
		t.Fatalf("no file: %v", err)
	}
	os.WriteFile(p, []byte("one"), 0o600)
	if err := Rotate(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p + ".1"); string(b) != "one" {
		t.Fatalf(".1 = %q", b)
	}
	os.WriteFile(p, []byte("two"), 0o600)
	if err := Rotate(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p + ".1"); string(b) != "two" {
		t.Fatalf(".1 = %q after the second rotation", b)
	}
	// Blocked: the file is emptied in place.
	os.Remove(p + ".1")
	blockRotation(t, p)
	os.WriteFile(p, []byte("three"), 0o600)
	if err := Rotate(p); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Size() != 0 {
		t.Fatalf("file not emptied: %v %v", st, err)
	}
}
