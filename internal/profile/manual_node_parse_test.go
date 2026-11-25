package profile

import (
	"encoding/base64"
	"errors"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

func TestParseManualNodeClipboardURIs(t *testing.T) {
	sip002User := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret-pass"))
	legacySS := base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:legacy-pw@ss2.example.com:8389"))
	vmessPayload := base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"VMess HK","add":"vm.example.com","port":"443","id":"11111111-1111-1111-1111-111111111111","aid":2,"scy":"aes-128-gcm","net":"ws","host":"ws.example.com","path":"/ws","tls":"tls","sni":"sni.example.com","fp":"chrome","alpn":"h2,http/1.1"}`))
	vmessIntPort := base64.StdEncoding.EncodeToString([]byte(`{"ps":"VMess Int","add":"vm2.example.com","port":8443,"id":"11111111-1111-1111-1111-111111111111","aid":"0","net":"grpc","path":"GunService","tls":true}`))
	socksB64 := base64.RawStdEncoding.EncodeToString([]byte("suser:spass@socks2.example.com:1081"))

	tests := []struct {
		name string
		raw  string
		want model.ManualNode
	}{
		{
			name: "ss-sip002-with-plugin",
			raw:  "ss://" + sip002User + "@ss.example.com:8388?plugin=obfs-local%3Bobfs%3Dtls%3Bobfs-host%3Dbing.com#SS%20Node",
			want: model.ManualNode{
				Name: "SS Node", Type: model.ProxyTypeSS, Server: "ss.example.com", Port: 8388,
				Cipher: "aes-256-gcm", Password: "secret-pass", UDP: true,
				Plugin: "obfs", PluginMode: "tls", PluginHost: "bing.com",
			},
		},
		{
			name: "ss-legacy-base64",
			raw:  "ss://" + legacySS + "#LegacySS",
			want: model.ManualNode{
				Name: "LegacySS", Type: model.ProxyTypeSS, Server: "ss2.example.com", Port: 8389,
				Cipher: "chacha20-ietf-poly1305", Password: "legacy-pw", UDP: true,
			},
		},
		{
			name: "vmess-json-string-port",
			raw:  "vmess://" + vmessPayload,
			want: model.ManualNode{
				Name: "VMess HK", Type: model.ProxyTypeVMess, Server: "vm.example.com", Port: 443,
				UUID: "11111111-1111-1111-1111-111111111111", AlterID: 2, Cipher: "aes-128-gcm",
				UDP: true, TLS: true, SNI: "sni.example.com", ClientFingerprint: "chrome",
				ALPN: []string{"h2", "http/1.1"}, Network: "ws", WSPath: "/ws", WSHost: "ws.example.com",
			},
		},
		{
			name: "vmess-json-int-port-grpc",
			raw:  "vmess://" + vmessIntPort,
			want: model.ManualNode{
				Name: "VMess Int", Type: model.ProxyTypeVMess, Server: "vm2.example.com", Port: 8443,
				UUID: "11111111-1111-1111-1111-111111111111", AlterID: 0, Cipher: "auto",
				UDP: true, TLS: true, Network: "grpc", GRPCServiceName: "GunService",
			},
		},
		{
			name: "vmess-url",
			raw:  "vmess://11111111-1111-1111-1111-111111111111@vm3.example.com:443?encryption=none&security=tls&type=ws&path=%2Fvm&host=h.example.com&sni=s.example.com&fp=firefox#VMessURL",
			want: model.ManualNode{
				Name: "VMessURL", Type: model.ProxyTypeVMess, Server: "vm3.example.com", Port: 443,
				UUID: "11111111-1111-1111-1111-111111111111", Cipher: "none",
				UDP: true, TLS: true, SNI: "s.example.com", ClientFingerprint: "firefox",
				Network: "ws", WSPath: "/vm", WSHost: "h.example.com",
			},
		},
		{
			name: "vless-reality",
			raw:  "vless://22222222-2222-2222-2222-222222222222@vless.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=cdn.example.com&fp=chrome&pbk=pubkey123&sid=abcd&type=tcp&packetEncoding=xudp#VLESS%20Reality",
			want: model.ManualNode{
				Name: "VLESS Reality", Type: model.ProxyTypeVLESS, Server: "vless.example.com", Port: 443,
				UUID: "22222222-2222-2222-2222-222222222222", Flow: "xtls-rprx-vision", PacketEncoding: "xudp",
				UDP: true, TLS: true, SNI: "cdn.example.com", ClientFingerprint: "chrome",
				RealityPublicKey: "pubkey123", RealityShortID: "abcd", Network: "tcp",
			},
		},
		{
			name: "trojan",
			raw:  "trojan://tr%3Apw@tr.example.com:443?sni=tr.example.com&fp=firefox&alpn=h2,http/1.1&allowInsecure=1#Trojan",
			want: model.ManualNode{
				Name: "Trojan", Type: model.ProxyTypeTrojan, Server: "tr.example.com", Port: 443,
				Password: "tr:pw", UDP: true, TLS: true, SNI: "tr.example.com", SkipCertVerify: true,
				ClientFingerprint: "firefox", ALPN: []string{"h2", "http/1.1"}, Network: "tcp",
			},
		},
		{
			name: "trojan-go",
			raw:  "trojan-go://tgpw@tg.example.com:443?sni=tg.example.com&type=ws&path=%2Ftg&host=tg.example.com&encryption=ss%3Baes-128-gcm%3Bsspw#TrojanGo",
			want: model.ManualNode{
				Name: "TrojanGo", Type: model.ProxyTypeTrojanGo, Server: "tg.example.com", Port: 443,
				Password: "tgpw", UDP: true, TLS: true, SNI: "tg.example.com",
				Network: "ws", WSPath: "/tg", WSHost: "tg.example.com",
				SSOptsEnabled: true, SSOptsMethod: "aes-128-gcm", SSOptsPassword: "sspw",
			},
		},
		{
			name: "hysteria-v1",
			raw:  "hysteria://hy1.example.com:443?protocol=udp&auth=hy1auth&peer=hy1.example.com&upmbps=50&downmbps=200&obfsParam=xplus&alpn=h3#Hy1",
			want: model.ManualNode{
				Name: "Hy1", Type: model.ProxyTypeHysteria, Server: "hy1.example.com", Port: 443,
				AuthStr: "hy1auth", Protocol: "udp", Up: "50 Mbps", Down: "200 Mbps",
				Obfs: "xplus", SNI: "hy1.example.com", ALPN: []string{"h3"},
			},
		},
		{
			name: "hysteria2",
			raw:  "hy2://hy2pw@hy2.example.com:443/?mport=443-8443&obfs=salamander&obfs-password=obfspw&sni=hy2.example.com&insecure=1&upmbps=100&downmbps=500&alpn=h3#Hy2",
			want: model.ManualNode{
				Name: "Hy2", Type: model.ProxyTypeHysteria2, Server: "hy2.example.com", Port: 443,
				Password: "hy2pw", Ports: "443-8443", Up: "100 Mbps", Down: "500 Mbps",
				Obfs: "salamander", ObfsPassword: "obfspw", SNI: "hy2.example.com",
				SkipCertVerify: true, ALPN: []string{"h3"},
			},
		},
		{
			name: "tuic-v5",
			raw:  "tuic://33333333-3333-3333-3333-333333333333:tuicpw@tuic.example.com:8443?congestion_control=bbr&udp_relay_mode=native&sni=tuic.example.com&alpn=h3&reduce_rtt=1&disable_sni=1#TUIC5",
			want: model.ManualNode{
				Name: "TUIC5", Type: model.ProxyTypeTUIC, Server: "tuic.example.com", Port: 8443,
				UUID: "33333333-3333-3333-3333-333333333333", Password: "tuicpw",
				CongestionController: "bbr", UDPRelayMode: "native", SNI: "tuic.example.com",
				ALPN: []string{"h3"}, ReduceRTT: true, DisableSNI: true,
			},
		},
		{
			name: "tuic-v4",
			raw:  "tuic://v4token@tuic4.example.com:8443?sni=tuic4.example.com#TUIC4",
			want: model.ManualNode{
				Name: "TUIC4", Type: model.ProxyTypeTUIC, Server: "tuic4.example.com", Port: 8443,
				Token: "v4token", SNI: "tuic4.example.com",
			},
		},
		{
			name: "anytls",
			raw:  "anytls://anypw@any.example.com:443?sni=any.example.com&fp=chrome&alpn=h2,http/1.1&insecure=1&idle_session_check_interval=30&idle_session_timeout=30&min_idle_session=1#AnyTLS",
			want: model.ManualNode{
				Name: "AnyTLS", Type: model.ProxyTypeAnyTLS, Server: "any.example.com", Port: 443,
				Password: "anypw", UDP: true, TLS: true, SNI: "any.example.com",
				SkipCertVerify: true, ClientFingerprint: "chrome", ALPN: []string{"h2", "http/1.1"},
				IdleSessionCheckInterval: 30, IdleSessionTimeout: 30, MinIdleSession: 1,
			},
		},
		{
			name: "socks5",
			raw:  "socks5://u:p@1.2.3.4:1080?tls=1&insecure=1#Socks1",
			want: model.ManualNode{
				Name: "Socks1", Type: model.ProxyTypeSOCKS5, Server: "1.2.3.4", Port: 1080,
				Username: "u", Password: "p", UDP: true, TLS: true, SkipCertVerify: true,
			},
		},
		{
			name: "socks-base64",
			raw:  "socks://" + socksB64 + "#SocksB64",
			want: model.ManualNode{
				Name: "SocksB64", Type: model.ProxyTypeSOCKS5, Server: "socks2.example.com", Port: 1081,
				Username: "suser", Password: "spass", UDP: true,
			},
		},
		{
			name: "https",
			raw:  "https://hu:hp@proxy.example.com?sni=proxy.example.com#HTTPS1",
			want: model.ManualNode{
				Name: "HTTPS1", Type: model.ProxyTypeHTTP, Server: "proxy.example.com", Port: 443,
				Username: "hu", Password: "hp", TLS: true, SNI: "proxy.example.com",
			},
		},
		{
			name: "http-no-auth",
			raw:  "http://plain.example.com:8080",
			want: model.ManualNode{
				Name: "plain.example.com", Type: model.ProxyTypeHTTP, Server: "plain.example.com", Port: 8080,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseManualNodeClipboard(tc.raw)
			if err != nil {
				t.Fatalf("ParseManualNodeClipboard(%q) error = %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got:\n%+v\nwant:\n%+v", got, tc.want)
			}
			if _, err := BuildManualProxyNode(got); err != nil {
				t.Fatalf("BuildManualProxyNode failed for parsed node: %v", err)
			}
		})
	}
}

func TestParseManualNodeClipboardYAMLRoundTrip(t *testing.T) {
	for _, sample := range sampleManualNodes() {
		y, err := BuildManualProxyNode(sample)
		if err != nil {
			t.Fatalf("BuildManualProxyNode(%s): %v", sample.Name, err)
		}
		raw, err := yaml.Marshal(y)
		if err != nil {
			t.Fatalf("yaml.Marshal(%s): %v", sample.Name, err)
		}
		got, err := ParseManualNodeClipboard(string(raw))
		if err != nil {
			t.Fatalf("ParseManualNodeClipboard(%s YAML):\n%s\nerr = %v", sample.Name, raw, err)
		}
		if got.Name != sample.Name || got.Type != sample.Type || got.Server != sample.Server || got.Port != sample.Port {
			t.Fatalf("%s mismatch: got %+v, want %+v", sample.Name, got, sample)
		}
		if _, err := BuildManualProxyNode(got); err != nil {
			t.Fatalf("BuildManualProxyNode after YAML round-trip(%s): %v", sample.Name, err)
		}
	}
}

func TestParseManualNodeClipboardVariantsAndErrors(t *testing.T) {
	t.Run("yaml-list-and-proxies-wrapper-and-flow", func(t *testing.T) {
		inputs := []string{
			"- name: list-ss\n  type: ss\n  server: 1.2.3.4\n  port: 8388\n  cipher: aes-128-gcm\n  password: pw",
			"proxies:\n  - {name: flow-ss, type: ss, server: 1.2.3.4, port: 8388, cipher: aes-128-gcm, password: pw}",
		}
		for _, in := range inputs {
			got, err := ParseManualNodeClipboard(in)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", in, err)
			}
			if got.Type != model.ProxyTypeSS || got.Server != "1.2.3.4" || got.Port != 8388 {
				t.Fatalf("unexpected node %+v", got)
			}
		}
	})

	t.Run("base64-wrapped-uri-and-multiline", func(t *testing.T) {
		uri := "trojan://pw@tr.example.com:443?sni=tr.example.com#Wrapped"
		b64 := base64.StdEncoding.EncodeToString([]byte(uri))
		got, err := ParseManualNodeClipboard(b64)
		if err != nil {
			t.Fatalf("base64 wrapped URI error = %v", err)
		}
		if got.Name != "Wrapped" || got.Type != model.ProxyTypeTrojan {
			t.Fatalf("got %+v", got)
		}

		multi := "\n  hello note\n" + uri + "\n"
		got2, err := ParseManualNodeClipboard(multi)
		if err != nil {
			t.Fatalf("multiline URI error = %v", err)
		}
		if got2.Name != "Wrapped" {
			t.Fatalf("got2 %+v", got2)
		}
	})

	t.Run("errors", func(t *testing.T) {
		if _, err := ParseManualNodeClipboard("   \n\t "); !errors.Is(err, ErrClipboardEmpty) {
			t.Fatalf("empty clipboard err = %v, want ErrClipboardEmpty", err)
		}
		if _, err := ParseManualNodeClipboard("just some random text"); !errors.Is(err, ErrUnsupportedNodeFormat) {
			t.Fatalf("plain text err = %v, want ErrUnsupportedNodeFormat", err)
		}
		if _, err := ParseManualNodeClipboard("vless://uuid@example.com:99999#badport"); err == nil {
			t.Fatal("expected error for invalid port")
		}
	})
}
