package tunnel

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFDConfig(t *testing.T) {
	src := `mixed-port: 7890
ipv6: true
interface-name: en0
tun:
  enable: false
  stack: mixed
  auto-route: true
  auto-detect-interface: true
  inet6-address: [fdfe:dcba:9876::1/126]
  device: utun1024
  dns-hijack: [any:53, tcp://any:53]
  route-exclude-address: [192.168.0.0/16, 10.1.2.3/8, bad]
proxies: []
`
	out, iface, err := fdConfig([]byte(src), 3)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	tun := got["tun"].(map[string]any)
	want := map[string]any{
		"enable": true, "file-descriptor": 3, "stack": "gvisor", "mtu": 9000,
		"auto-route": false, "auto-detect-interface": false, "strict-route": false,
		"inet4-address": []any{"198.18.0.1/30"}, "inet6-address": []any{"fdfe:dcba:9876::1/126"},
		"dns-hijack": []any{"any:53", "tcp://any:53"},
	}
	if !reflect.DeepEqual(tun, want) {
		t.Errorf("tun = %v\nwant %v", tun, want)
	}
	if _, ok := got["interface-name"]; ok {
		t.Error("interface-name kept")
	}
	if dns, _ := got["dns"].(map[string]any); dns["enable"] != true || dns["enhanced-mode"] != "fake-ip" || dns["ipv6"] != true {
		t.Errorf("dns = %v", got["dns"])
	}
	if got["mixed-port"] != 7890 {
		t.Errorf("mixed-port = %v", got["mixed-port"])
	}
	wantIface := FDInterface{MTU: 9000, Address4: "198.18.0.1/30", Address6: "fdfe:dcba:9876::1/126",
		DNS: []string{"198.18.0.2"}, Exclude: []string{"192.168.0.0/16", "10.0.0.0/8"}}
	if !reflect.DeepEqual(iface, wantIface) {
		t.Errorf("iface = %+v\nwant %+v", iface, wantIface)
	}
}

func TestFDConfigNoTUNNoIPv6(t *testing.T) {
	out, iface, err := fdConfig([]byte("ipv6: false\ntun:\n  inet4-address: 172.19.0.1/30\n  mtu: 1500\n"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if iface.Address6 != "" || iface.Address4 != "172.19.0.1/30" || iface.MTU != 1500 || iface.DNS[0] != "172.19.0.2" {
		t.Errorf("iface = %+v", iface)
	}
	var got struct {
		TUN struct {
			FD     int  `yaml:"file-descriptor"`
			Enable bool `yaml:"enable"`
		} `yaml:"tun"`
	}
	if err := yaml.Unmarshal(out, &got); err != nil || got.TUN.FD != 5 || !got.TUN.Enable {
		t.Errorf("tun = %+v, %v", got.TUN, err)
	}

	if _, _, err := fdConfig([]byte("a: 1\n"), 3); err != nil {
		t.Errorf("config without tun: %v", err)
	}
	// A DNS of the config's own is kept.
	out, _, err = fdConfig([]byte("dns:\n  enable: true\n  nameserver: [1.1.1.1]\n"), 3)
	var own struct {
		DNS map[string]any `yaml:"dns"`
	}
	if err != nil || yaml.Unmarshal(out, &own) != nil || len(own.DNS) != 2 {
		t.Errorf("own dns = %v, %v", own.DNS, err)
	}
	for _, bad := range []string{"- 1\n", "tun:\n  inet4-address: 10.0.0.3/32\n", "tun:\n  inet4-address: nope\n"} {
		if _, _, err := fdConfig([]byte(bad), 3); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
