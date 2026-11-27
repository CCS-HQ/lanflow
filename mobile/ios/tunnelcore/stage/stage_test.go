package stage

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestApplyOverrides(t *testing.T) {
	src := `
mixed-port: 7890
external-controller: 127.0.0.1:9090
external-ui: ui
interface-name: en0
find-process-mode: always
tun:
  enable: false
  device: utun9
  auto-route: true
  stack: system
  route-exclude-address: [10.0.0.0/8]
dns:
  enable: true
  fake-ip-range: 28.0.0.1/8
rules:
  - MATCH,DIRECT
`
	out, net, err := Apply([]byte(src), Params{FD: 7, Controller: "/g/c.sock"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	tun := doc["tun"].(map[string]any)
	for k, want := range map[string]any{
		"enable": true, "stack": "mixed", "file-descriptor": 7, "auto-route": false,
		"auto-detect-interface": false, "mtu": MTU,
	} {
		if tun[k] != want {
			t.Errorf("tun.%s = %v, want %v", k, tun[k], want)
		}
	}
	if _, ok := tun["device"]; ok {
		t.Error("tun.device kept")
	}
	if tun["route-exclude-address"] == nil {
		t.Error("unrelated tun keys dropped")
	}
	if doc["external-controller"] != "" || doc["external-controller-unix"] != "/g/c.sock" {
		t.Errorf("controller = %v / %v", doc["external-controller"], doc["external-controller-unix"])
	}
	for _, k := range []string{"external-ui", "interface-name"} {
		if _, ok := doc[k]; ok {
			t.Errorf("%s kept", k)
		}
	}
	if doc["find-process-mode"] != "off" || doc["geodata-loader"] != "memconservative" {
		t.Error("memory/process overrides missing")
	}
	if doc["mixed-port"] != 7890 || doc["rules"] == nil {
		t.Error("the rest of the config changed")
	}
	if net.Inet4 != "28.0.0.1" || net.DNS != "28.0.0.2" || net.Inet6 != "" || net.MTU != MTU {
		t.Errorf("network = %+v", net)
	}
}

func TestApplyDefaultsAndIPv6(t *testing.T) {
	_, net, err := Apply([]byte("ipv6: true\n"), Params{})
	if err != nil {
		t.Fatal(err)
	}
	if net.Inet4 != "198.18.0.1" || net.DNS != "198.18.0.2" || net.Inet6 != "fdfe:dcba:9876::1" {
		t.Errorf("network = %+v", net)
	}
	_, net, err = Apply([]byte("ipv6: true\ntun:\n  inet6-address: [fd00::5/126]\n"), Params{})
	if err != nil || net.Inet6 != "fd00::5" {
		t.Errorf("inet6 = %q, %v", net.Inet6, err)
	}
}

func TestApplyRejects(t *testing.T) {
	for _, src := range []string{"", "a: [", "dns:\n  fake-ip-range: fd00::/64\n"} {
		if _, _, err := Apply([]byte(src), Params{}); err == nil {
			t.Errorf("%q: no error", src)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%q: empty error", src)
		}
	}
}
