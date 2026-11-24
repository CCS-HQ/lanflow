//go:build windows

package netinfo

import (
	"net/netip"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWin32StructLayouts(t *testing.T) {
	if got := unsafe.Sizeof(mibIPForwardRow2{}); got != 104 {
		t.Fatalf("sizeof(MIB_IPFORWARD_ROW2) = %d, want 104", got)
	}
	if got := unsafe.Offsetof(mibIPForwardTable2{}.Table); got != 8 {
		t.Fatalf("offsetof(MIB_IPFORWARD_TABLE2.Table) = %d, want 8", got)
	}
	routes4, err := listRoutes(windows.AF_INET)
	if err != nil {
		t.Fatalf("listRoutes(AF_INET): %v", err)
	}
	if len(routes4) == 0 {
		t.Fatal("listRoutes(AF_INET) returned 0 routes")
	}
	if _, err := listRoutes(windows.AF_INET6); err != nil {
		t.Fatalf("listRoutes(AF_INET6): %v", err)
	}
}

func TestDetectWindowsTUN(t *testing.T) {
	phys := adapter{name: "WLAN", index: 12, metric: 35}
	vm := adapter{name: "vEthernet (Default Switch)", index: 18, metric: 5000, tunnel: true}
	wintun := adapter{name: "Clash", index: 24, ipv4: "198.18.0.1", metric: 5, tunnel: true, vpn: true}

	v4Default := netip.MustParsePrefix("0.0.0.0/0")

	t.Run("split defaults via TUN", func(t *testing.T) {
		got, ok := detectWindowsTUN([]adapter{phys, vm, wintun}, []winRoute{
			{ifIndex: 12, dst: v4Default, metric: 0},
			{ifIndex: 24, dst: v4SplitLow, metric: 0},
			{ifIndex: 24, dst: v4SplitHigh, metric: 0},
		})
		if !ok || got != "Clash" {
			t.Fatalf("got (%q, %v), want (Clash, true)", got, ok)
		}
	})

	t.Run("full default captured by low-metric VPN adapter", func(t *testing.T) {
		got, ok := detectWindowsTUN([]adapter{phys, wintun}, []winRoute{
			{ifIndex: 12, dst: v4Default, metric: 0},
			{ifIndex: 24, dst: v4Default, metric: 0},
		})
		if !ok || got != "Clash" {
			t.Fatalf("got (%q, %v), want (Clash, true)", got, ok)
		}
	})

	t.Run("VM switch does not count as competing TUN", func(t *testing.T) {
		got, ok := detectWindowsTUN([]adapter{phys, vm}, []winRoute{
			{ifIndex: 12, dst: v4Default, metric: 0},
			{ifIndex: 18, dst: v4Default, metric: 0},
		})
		if ok || got != "" {
			t.Fatalf("got (%q, %v), want (\"\", false)", got, ok)
		}
	})
}

func TestAnalyzeWindowsV6Routes(t *testing.T) {
	phys := adapter{name: "以太网", index: 12, ipv6Index: 12}
	tun := adapter{name: "Lanflow", index: 24, ipv6Index: 24, tunnel: true, vpn: true}
	v6Default := netip.MustParsePrefix("::/0")

	r := analyzeWindowsV6Routes([]adapter{phys, tun}, []winRoute{
		{ifIndex: 12, dst: v6Default, nextHop: netip.MustParseAddr("fe80::1")},
		{ifIndex: 24, dst: netip.MustParsePrefix("::/1"), nextHop: netip.IPv6Unspecified()},
		{ifIndex: 24, dst: netip.MustParsePrefix("8000::/1"), nextHop: netip.IPv6Unspecified()},
	})
	if r.Interface != "以太网" || !r.ViaTUN {
		t.Fatalf("got %+v, want Interface=以太网 ViaTUN=true", r)
	}

	if r := parseWindowsV6Routes(" 12 281 ::/0 fe80::1\n 24 0 ::/0 在链路上\n"); r.Interface != "if#12" || !r.ViaTUN {
		t.Fatalf("localized route print parse = %+v", r)
	}
}
