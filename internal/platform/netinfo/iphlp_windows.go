//go:build windows

package netinfo

import (
	"encoding/binary"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIPHlpAPI            = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIpForwardTable2 = modIPHlpAPI.NewProc("GetIpForwardTable2")
	procFreeMibTable       = modIPHlpAPI.NewProc("FreeMibTable")
)

// mibIPForwardRow2 matches Windows MIB_IPFORWARD_ROW2 (netioapi.h): 104 bytes.
type mibIPForwardRow2 struct {
	InterfaceLuid        uint64
	InterfaceIndex       uint32
	DestinationPrefix    [32]byte // SOCKADDR_INET (28) + UINT8 PrefixLength + 3 bytes pad
	NextHop              [28]byte // SOCKADDR_INET
	SitePrefixLength     uint8
	_                    [3]byte
	ValidLifetime        uint32
	PreferredLifetime    uint32
	Metric               uint32
	Protocol             int32
	Loopback             uint8
	AutoconfigureAddress uint8
	Publish              uint8
	Immortal             uint8
	Age                  uint32
	Origin               int32
}

type mibIPForwardTable2 struct {
	NumEntries uint32
	_          uint32
	Table      [1]mibIPForwardRow2
}

type winRoute struct {
	ifIndex uint32
	dst     netip.Prefix
	nextHop netip.Addr
	metric  uint32
}

func listRoutes(family uint16) ([]winRoute, error) {
	if err := procGetIpForwardTable2.Find(); err != nil {
		return nil, err
	}
	if err := procFreeMibTable.Find(); err != nil {
		return nil, err
	}
	var table *mibIPForwardTable2
	r, _, _ := procGetIpForwardTable2.Call(uintptr(family), uintptr(unsafe.Pointer(&table)))
	if r != 0 {
		return nil, windows.Errno(r)
	}
	if table == nil {
		return nil, nil
	}
	defer procFreeMibTable.Call(uintptr(unsafe.Pointer(table)))

	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	out := make([]winRoute, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		addr, ok := parseSockaddrInet(row.DestinationPrefix[:28])
		if !ok {
			continue
		}
		prefixLen := int(row.DestinationPrefix[28])
		if prefixLen < 0 || prefixLen > addr.BitLen() {
			continue
		}
		hop, _ := parseSockaddrInet(row.NextHop[:])
		out = append(out, winRoute{
			ifIndex: row.InterfaceIndex,
			dst:     netip.PrefixFrom(addr, prefixLen).Masked(),
			nextHop: hop,
			metric:  row.Metric,
		})
	}
	return out, nil
}

func parseSockaddrInet(b []byte) (netip.Addr, bool) {
	if len(b) < 28 {
		return netip.Addr{}, false
	}
	family := binary.LittleEndian.Uint16(b[:2])
	switch family {
	case windows.AF_INET:
		return netip.AddrFrom4([4]byte(b[4:8])), true
	case windows.AF_INET6:
		return netip.AddrFrom16([16]byte(b[8:24])).Unmap(), true
	default:
		return netip.Addr{}, false
	}
}

var (
	v4SplitLow  = netip.MustParsePrefix("0.0.0.0/1")
	v4SplitHigh = netip.MustParsePrefix("128.0.0.0/1")
	v6Splits    = map[netip.Prefix]bool{
		netip.MustParsePrefix("::/1"):     true,
		netip.MustParsePrefix("2000::/3"): true,
		netip.MustParsePrefix("8000::/1"): true,
	}
)

func detectWindowsTUN(adapters []adapter, routes []winRoute) (string, bool) {
	byIdx := make(map[uint32]adapter, len(adapters))
	for _, a := range adapters {
		byIdx[a.index] = a
	}
	low := map[uint32]bool{}
	high := map[uint32]bool{}
	const noMetric = ^uint32(0)
	bestPhysMetric := noMetric
	bestVPNMetric := noMetric
	var bestVPNIf uint32

	for _, r := range routes {
		a, ok := byIdx[r.ifIndex]
		if !ok {
			continue
		}
		switch r.dst {
		case v4SplitLow:
			low[r.ifIndex] = true
		case v4SplitHigh:
			high[r.ifIndex] = true
		}
		if r.dst.Bits() == 0 && r.dst.Addr().Is4() {
			eff := saturatedAdd(r.metric, a.metric)
			if a.vpn {
				if eff < bestVPNMetric {
					bestVPNMetric = eff
					bestVPNIf = r.ifIndex
				}
			} else if !a.tunnel && eff < bestPhysMetric {
				bestPhysMetric = eff
			}
		}
	}

	// Split default routes (0.0.0.0/1 + 128.0.0.0/1) on an active adapter.
	for ifIdx := range low {
		if !high[ifIdx] {
			continue
		}
		if a, ok := byIdx[ifIdx]; ok {
			return tunDisplayName(a), true
		}
	}
	// Full 0.0.0.0/0 captured by a VPN/Wintun adapter with lowest effective metric.
	if bestVPNMetric != noMetric && (bestPhysMetric == noMetric || bestVPNMetric <= bestPhysMetric) {
		return tunDisplayName(byIdx[bestVPNIf]), true
	}
	return "", false
}

func saturatedAdd(a, b uint32) uint32 {
	if ^uint32(0)-a < b {
		return ^uint32(0)
	}
	return a + b
}

func tunDisplayName(a adapter) string {
	if a.name != "" {
		return a.name
	}
	if a.ipv4 != "" {
		return "TUN（" + a.ipv4 + "）"
	}
	return "TUN"
}

func analyzeWindowsV6Routes(adapters []adapter, routes []winRoute) V6Route {
	byIdx := make(map[uint32]adapter, len(adapters))
	for _, a := range adapters {
		idx := a.ipv6Index
		if idx == 0 {
			idx = a.index
		}
		if idx != 0 {
			byIdx[idx] = a
		}
	}
	var out V6Route
	for _, r := range routes {
		if !r.dst.Addr().Is6() {
			continue
		}
		isDefault := r.dst.Bits() == 0
		if !isDefault && !v6Splits[r.dst] {
			continue
		}
		a, ok := byIdx[r.ifIndex]
		if !ok {
			continue
		}
		switch {
		case a.tunnel || a.vpn:
			out.ViaTUN = true
		case isDefault && out.Interface == "":
			if a.name != "" {
				out.Interface = a.name
			}
		}
	}
	return out
}
