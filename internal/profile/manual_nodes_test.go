package profile

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

func sampleManualNodes() []model.ManualNode {
	return []model.ManualNode{
		{
			Name: "socks-1", Type: model.ProxyTypeSOCKS5, Server: "1.2.3.4", Port: 1080,
			Username: "u", Password: "p", TLS: true, SkipCertVerify: true, UDP: true,
		},
		{
			Name: "http-1", Type: model.ProxyTypeHTTP, Server: "proxy.example.com", Port: 443,
			Username: "u", Password: "p", TLS: true, SNI: "proxy.example.com",
		},
		{
			Name: "ss-1", Type: model.ProxyTypeSS, Server: "ss.example.com", Port: 8388,
			Cipher: "aes-128-gcm", Password: "secret", UDP: true,
			Plugin: "obfs", PluginMode: "tls", PluginHost: "bing.com",
		},
		{
			Name: "vmess-1", Type: model.ProxyTypeVMess, Server: "vmess.example.com", Port: 443,
			UUID: "11111111-1111-1111-1111-111111111111", AlterID: 0, Cipher: "auto",
			TLS: true, SNI: "vmess.example.com", ClientFingerprint: "chrome",
			Network: "ws", WSPath: "/ws", WSHost: "vmess.example.com", UDP: true,
		},
		{
			Name: "vless-1", Type: model.ProxyTypeVLESS, Server: "vless.example.com", Port: 443,
			UUID: "22222222-2222-2222-2222-222222222222", Flow: "xtls-rprx-vision",
			PacketEncoding: "xudp", TLS: true, SNI: "cdn.example.com", ClientFingerprint: "chrome",
			RealityPublicKey: "pubkey123", RealityShortID: "abcd", UDP: true,
		},
		{
			Name: "trojan-1", Type: model.ProxyTypeTrojan, Server: "tr.example.com", Port: 443,
			Password: "trpw", SNI: "tr.example.com", ClientFingerprint: "firefox",
			ALPN: []string{"h2", "http/1.1"}, UDP: true,
		},
		{
			Name: "trojan-go-1", Type: model.ProxyTypeTrojanGo, Server: "tg.example.com", Port: 443,
			Password: "tgpw", SNI: "tg.example.com", Network: "ws", WSPath: "/tg", WSHost: "tg.example.com",
			SSOptsEnabled: true, SSOptsMethod: "aes-128-gcm", SSOptsPassword: "sspw", UDP: true,
		},
		{
			Name: "hy1-1", Type: model.ProxyTypeHysteria, Server: "hy1.example.com", Port: 443,
			AuthStr: "hy1auth", Protocol: "udp", Up: "50 Mbps", Down: "200 Mbps",
			Obfs: "xplus", SNI: "hy1.example.com", ALPN: []string{"h3"},
		},
		{
			Name: "hy2-1", Type: model.ProxyTypeHysteria2, Server: "hy2.example.com", Port: 443,
			Ports: "443-8443", Password: "hy2pw", Up: "50 Mbps", Down: "200 Mbps",
			Obfs: "salamander", ObfsPassword: "obfspw", SNI: "hy2.example.com", ALPN: []string{"h3"},
		},
		{
			Name: "tuic-1", Type: model.ProxyTypeTUIC, Server: "tuic.example.com", Port: 8443,
			UUID: "33333333-3333-3333-3333-333333333333", Password: "tuicpw",
			CongestionController: "bbr", UDPRelayMode: "native", SNI: "tuic.example.com",
			ALPN: []string{"h3"}, ReduceRTT: true, DisableSNI: true,
		},
		{
			Name: "anytls-1", Type: model.ProxyTypeAnyTLS, Server: "any.example.com", Port: 443,
			Password: "anypw", SNI: "any.example.com", ClientFingerprint: "chrome",
			ALPN: []string{"h2", "http/1.1"}, UDP: true,
			IdleSessionCheckInterval: 30, IdleSessionTimeout: 30, MinIdleSession: 1,
		},
	}
}

func TestBuildManualProxyNodeAllProtocols(t *testing.T) {
	for _, n := range sampleManualNodes() {
		y, err := BuildManualProxyNode(n)
		if err != nil {
			t.Fatalf("BuildManualProxyNode(%s) failed: %v", n.Type, err)
		}
		var got map[string]any
		data, err := yaml.Marshal(y)
		if err != nil {
			t.Fatalf("marshal %s: %v", n.Name, err)
		}
		if err := yaml.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", n.Name, err)
		}
		if got["name"] != n.Name {
			t.Fatalf("%s: name = %v, want %s", n.Name, got["name"], n.Name)
		}
		wantType := n.Type
		if n.Type == model.ProxyTypeTrojanGo {
			wantType = model.ProxyTypeTrojan
		}
		if got["type"] != wantType {
			t.Fatalf("%s: type = %v, want %s", n.Name, got["type"], wantType)
		}
		if n.Type == model.ProxyTypeTrojanGo {
			ssOpts, ok := got["ss-opts"].(map[string]any)
			if !ok || ssOpts["enabled"] != true || ssOpts["method"] != "aes-128-gcm" || ssOpts["password"] != "sspw" {
				t.Fatalf("trojan-go ss-opts = %#v", got["ss-opts"])
			}
		}
		if n.Type == model.ProxyTypeVLESS {
			ro, ok := got["reality-opts"].(map[string]any)
			if !ok || ro["public-key"] != "pubkey123" || ro["short-id"] != "abcd" {
				t.Fatalf("vless reality-opts = %#v", got["reality-opts"])
			}
		}
	}
}

func TestValidateManualNodeErrors(t *testing.T) {
	bad := []model.ManualNode{
		{Type: model.ProxyTypeSOCKS5, Server: "1.1.1.1", Port: 1080},
		{Name: "a", Type: "unknown", Server: "1.1.1.1", Port: 1080},
		{Name: "a", Type: model.ProxyTypeSOCKS5, Port: 1080},
		{Name: "a", Type: model.ProxyTypeSOCKS5, Server: "1.1.1.1", Port: 0},
		{Name: "a", Type: model.ProxyTypeSS, Server: "1.1.1.1", Port: 8388, Password: "p"},
		{Name: "a", Type: model.ProxyTypeVMess, Server: "1.1.1.1", Port: 443},
		{Name: "a", Type: model.ProxyTypeVLESS, Server: "1.1.1.1", Port: 443},
		{Name: "a", Type: model.ProxyTypeTrojan, Server: "1.1.1.1", Port: 443},
		{Name: "a", Type: model.ProxyTypeTrojanGo, Server: "1.1.1.1", Port: 443, Password: "p", SSOptsEnabled: true},
		{Name: "a", Type: model.ProxyTypeHysteria, Server: "1.1.1.1", Port: 443, Up: "10 Mbps"},
		{Name: "a", Type: model.ProxyTypeHysteria2, Server: "1.1.1.1", Port: 443},
		{Name: "a", Type: model.ProxyTypeTUIC, Server: "1.1.1.1", Port: 443},
		{Name: "a", Type: model.ProxyTypeAnyTLS, Server: "1.1.1.1", Port: 443},
	}
	for i, n := range bad {
		if _, err := ValidateManualNode(n); err == nil {
			t.Fatalf("case %d (%+v): expected validation error", i, n)
		}
	}
	// TUIC v4 with Token only should succeed.
	if _, err := ValidateManualNode(model.ManualNode{
		Name: "tuic-v4", Type: model.ProxyTypeTUIC, Server: "1.1.1.1", Port: 443, Token: "tok",
	}); err != nil {
		t.Fatalf("tuic v4 failed: %v", err)
	}
}

func TestManualNodesStoreRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := sampleManualNodes()
	saved, err := st.SetManualNodes(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != len(in) {
		t.Fatalf("saved len = %d, want %d", len(saved), len(in))
	}
	for i, n := range saved {
		if n.ID == "" {
			t.Fatalf("node %d got empty ID", i)
		}
	}
	st2, err := Open(st.Dir())
	if err != nil {
		t.Fatal(err)
	}
	got := st2.ManualNodes()
	if len(got) != len(saved) {
		t.Fatalf("reopened len = %d, want %d", len(got), len(saved))
	}
	if got[0].Name != "socks-1" || got[len(got)-1].Type != model.ProxyTypeAnyTLS {
		t.Fatalf("unexpected reopened nodes: %+v", got)
	}
}

func TestComposeManualNodesStandaloneAndWithSubscription(t *testing.T) {
	nodes := sampleManualNodes()
	// 1. Standalone (no subscription)
	out, err := Compose(Input{
		ManualNodes: nodes[:2],
		Managed:     Managed{MixedPort: 7890, Mode: model.ModeRule},
	})
	if err != nil {
		t.Fatalf("standalone Compose failed: %v", err)
	}
	if !slices.Contains(out.Groups, ManualGroupName) {
		t.Fatalf("expected %q in Groups, got %v", ManualGroupName, out.Groups)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out.YAML, &doc); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}
	proxies, _ := doc["proxies"].([]any)
	if len(proxies) != 2 {
		t.Fatalf("proxies len = %d, want 2", len(proxies))
	}
	rules, _ := doc["rules"].([]any)
	if len(rules) == 0 || !strings.Contains(rules[len(rules)-1].(string), ManualGroupName) {
		t.Fatalf("unexpected standalone rules: %v", rules)
	}

	// 2. Combined with subscription
	sub := []byte(`
proxies:
  - { name: sub-node, type: ss, server: 9.9.9.9, port: 8388, cipher: aes-128-gcm, password: p }
proxy-groups:
  - { name: Proxy, type: select, proxies: [sub-node] }
rules:
  - MATCH,Proxy
`)
	out2, err := Compose(Input{
		Subscription: sub,
		ManualNodes:  nodes[:2],
		Managed:      Managed{MixedPort: 7890, Mode: model.ModeRule},
	})
	if err != nil {
		t.Fatalf("combined Compose failed: %v", err)
	}
	if !slices.Equal(out2.Groups, []string{"Proxy", ManualGroupName}) {
		t.Fatalf("combined Groups = %v", out2.Groups)
	}
	var doc2 map[string]any
	if err := yaml.Unmarshal(out2.YAML, &doc2); err != nil {
		t.Fatal(err)
	}
	gs2 := doc2["proxy-groups"].([]any)
	firstGroup := gs2[0].(map[string]any)
	firstProxies := firstGroup["proxies"].([]any)
	if len(firstProxies) != 2 || firstProxies[1] != ManualGroupName {
		t.Fatalf("first select group proxies = %v, want ManualGroupName appended", firstProxies)
	}
}
