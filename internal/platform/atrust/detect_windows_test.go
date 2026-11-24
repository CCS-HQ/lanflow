//go:build windows

package atrust

import (
	"net/netip"
	"slices"
	"testing"
	"unsafe"
)

func TestATrustWindowsStructsAndBuild(t *testing.T) {
	if got := unsafe.Sizeof(mibIPForwardRow2{}); got != 104 {
		t.Fatalf("sizeof(mibIPForwardRow2) = %d, want 104", got)
	}
	if got := unsafe.Sizeof(mibTCPRowOwnerPID{}); got != 24 {
		t.Fatalf("sizeof(mibTCPRowOwnerPID) = %d, want 24", got)
	}

	adapters := []winAdapter{
		{index: 12, name: "WLAN", vpn: false},
		{index: 20, name: "Sangfor aTrust VNIC", desc: "Sangfor EasyConnect VNIC", vpn: true, dnsSuffix: "corp.example", dns: []string{"10.10.0.53"}},
		{index: 25, name: "Lanflow", desc: "Wintun Userspace Tunnel", vpn: true, dns: []string{"198.18.0.2"}},
	}
	routes := []winRoute{
		{ifIndex: 12, prefix: netip.MustParsePrefix("0.0.0.0/0")},
		{ifIndex: 20, prefix: netip.MustParsePrefix("10.0.0.0/8")},
		{ifIndex: 20, prefix: netip.MustParsePrefix("172.16.1.0/24")},
		{ifIndex: 25, prefix: netip.MustParsePrefix("198.18.0.0/16")},
	}
	nrpt := []nrptRule{
		{domains: []string{"git.corp.example"}, servers: []string{"10.10.0.54"}},
	}
	own := func(iface string) bool { return iface == "Lanflow" }
	info := buildWindowsInfo([]string{"aTrustTray.exe"}, adapters, routes, nrpt, own)

	if !slices.Equal(info.Ifaces, []string{"Sangfor aTrust VNIC"}) {
		t.Fatalf("Ifaces = %v", info.Ifaces)
	}
	if !slices.Equal(info.Domains, []string{"corp.example", "git.corp.example"}) {
		t.Fatalf("Domains = %v", info.Domains)
	}
	if !slices.Equal(info.DNS, []string{"10.10.0.53", "10.10.0.54"}) {
		t.Fatalf("DNS = %v", info.DNS)
	}
	// 10.10.0.53/54 are inside 10.0.0.0/8 after collapse().
	if !slices.Equal(info.CIDRs, []string{"10.0.0.0/8", "172.16.1.0/24"}) {
		t.Fatalf("CIDRs = %v", info.CIDRs)
	}
}

func TestParseNRPTEntry(t *testing.T) {
	r, ok := parseNRPTEntry([]string{".intranet.corp.", ".local"}, "10.1.1.53; ::1")
	if !ok {
		t.Fatal("expected ok")
	}
	if !slices.Equal(r.domains, []string{"intranet.corp"}) {
		t.Fatalf("domains = %v", r.domains)
	}
	if !slices.Equal(r.servers, []string{"10.1.1.53"}) {
		t.Fatalf("servers = %v", r.servers)
	}
}
