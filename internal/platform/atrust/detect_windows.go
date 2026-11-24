//go:build windows

package atrust

import (
	"context"
	"encoding/binary"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	ifTypePropVirtual       = 53
	ifTypePPP               = 23
	tcpTableOwnerPIDConns   = 4
	nrptPolicyConfigRegPath = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`
)

var (
	modIPHlpAPI             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIpForwardTable2  = modIPHlpAPI.NewProc("GetIpForwardTable2")
	procFreeMibTable        = modIPHlpAPI.NewProc("FreeMibTable")
	procGetExtendedTcpTable = modIPHlpAPI.NewProc("GetExtendedTcpTable")
)

type winAdapter struct {
	index     uint32
	name      string
	desc      string
	dnsSuffix string
	vpn       bool
	dns       []string
}

type winRoute struct {
	ifIndex uint32
	prefix  netip.Prefix
}

type nrptRule struct {
	domains []string
	servers []string
}

// mibIPForwardRow2 matches Windows MIB_IPFORWARD_ROW2 (104 bytes).
type mibIPForwardRow2 struct {
	InterfaceLuid        uint64
	InterfaceIndex       uint32
	DestinationPrefix    [32]byte
	NextHop              [28]byte
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

type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

func detect(_ context.Context, own func(string) bool) Info {
	procNames, pids := aTrustProcesses()
	procs := matchProcesses(procNames)
	if len(procs) == 0 {
		return Info{}
	}
	adapters := winAdapters()
	routes := winIPv4Routes()
	nrpt := readNRPTRules()
	info := buildWindowsInfo(procs, adapters, routes, nrpt, own)

	var ranges []netip.Prefix
	for _, c := range info.CIDRs {
		if p, err := netip.ParsePrefix(c); err == nil {
			ranges = append(ranges, p)
		}
	}
	info.Gateways = winTCPGateways(pids, ranges)
	return info
}

func aTrustProcesses() ([]string, map[uint32]bool) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, nil
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil, nil
	}
	self := uint32(os.Getpid())
	var names []string
	pids := map[uint32]bool{}
	for {
		if pe.ProcessID != self {
			exe := windows.UTF16ToString(pe.ExeFile[:])
			if IsATrustProcess(exe) {
				names = append(names, exe)
				pids[pe.ProcessID] = true
			}
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return names, pids
}

func winAdapters() []winAdapter {
	const flags = windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST
	size := uint32(16 * 1024)
	var buf []byte
	for attempt := 0; attempt < 4; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil
		}
		buf = nil
	}
	if buf == nil || size == 0 {
		return nil
	}
	var out []winAdapter
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp || aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		name := windows.UTF16PtrToString(aa.FriendlyName)
		desc := windows.UTF16PtrToString(aa.Description)
		suffix := ""
		if aa.DnsSuffix != nil {
			suffix = strings.Trim(strings.ToLower(windows.UTF16PtrToString(aa.DnsSuffix)), ".")
		}
		a := winAdapter{
			index:     aa.IfIndex,
			name:      name,
			desc:      desc,
			dnsSuffix: suffix,
			vpn:       isVPNAdapter(aa.IfType, name, desc),
		}
		for d := aa.FirstDnsServerAddress; d != nil; d = d.Next {
			ip := d.Address.IP()
			if ip == nil {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				a.dns = append(a.dns, ip4.String())
			}
		}
		out = append(out, a)
	}
	return out
}

func isVPNAdapter(ifType uint32, name, desc string) bool {
	if isVMSwitch(name, desc) {
		return false
	}
	if ifType == windows.IF_TYPE_TUNNEL || ifType == ifTypePPP {
		return true
	}
	joined := strings.ToLower(name + " " + desc)
	for _, hint := range []string{"sangfor", "atrust", "easyconnect", "wintun", "tap-windows", "tap-provider", "wireguard", "tailscale", "openvpn", "vpn", "tun"} {
		if strings.Contains(joined, hint) {
			return true
		}
	}
	return false
}

func isVMSwitch(name, desc string) bool {
	joined := strings.ToLower(name + " " + desc)
	return strings.Contains(joined, "hyper-v") || strings.Contains(joined, "vethernet") ||
		strings.Contains(joined, "vmware") || strings.Contains(joined, "virtualbox")
}

func winIPv4Routes() []winRoute {
	if procGetIpForwardTable2.Find() != nil || procFreeMibTable.Find() != nil {
		return nil
	}
	var table *mibIPForwardTable2
	r, _, _ := procGetIpForwardTable2.Call(uintptr(windows.AF_INET), uintptr(unsafe.Pointer(&table)))
	if r != 0 || table == nil {
		return nil
	}
	defer procFreeMibTable.Call(uintptr(unsafe.Pointer(table)))

	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	out := make([]winRoute, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if binary.LittleEndian.Uint16(row.DestinationPrefix[:2]) != windows.AF_INET {
			continue
		}
		addr := netip.AddrFrom4([4]byte(row.DestinationPrefix[4:8]))
		bits := int(row.DestinationPrefix[28])
		if bits < 0 || bits > 32 {
			continue
		}
		out = append(out, winRoute{
			ifIndex: row.InterfaceIndex,
			prefix:  netip.PrefixFrom(addr, bits).Masked(),
		})
	}
	return out
}

func readNRPTRules() []nrptRule {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptPolicyConfigRegPath, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}
	var out []nrptRule
	for _, sub := range names {
		k, err := registry.OpenKey(root, sub, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		var domains []string
		if ms, _, err := k.GetStringsValue("Name"); err == nil {
			domains = ms
		} else if s, _, err := k.GetStringValue("Name"); err == nil {
			domains = []string{s}
		}
		servers, _, _ := k.GetStringValue("GenericDNSServers")
		k.Close()
		if r, ok := parseNRPTEntry(domains, servers); ok {
			out = append(out, r)
		}
	}
	return out
}

func parseNRPTEntry(rawDomains []string, rawServers string) (nrptRule, bool) {
	var r nrptRule
	for _, d := range rawDomains {
		for _, part := range strings.FieldsFunc(d, func(ch rune) bool { return ch == ';' || ch == ',' || ch == ' ' || ch == '\t' }) {
			part = strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(part)), "."), ".")
			if part == "" || part == "local" || strings.HasSuffix(part, ".arpa") {
				continue
			}
			r.domains = append(r.domains, part)
		}
	}
	for _, s := range strings.FieldsFunc(rawServers, func(ch rune) bool { return ch == ';' || ch == ',' || ch == ' ' || ch == '\t' }) {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil && a.Is4() && !a.IsLoopback() {
			r.servers = append(r.servers, a.String())
		}
	}
	return r, len(r.domains) > 0 || len(r.servers) > 0
}

func buildWindowsInfo(procs []string, adapters []winAdapter, routes []winRoute, nrpt []nrptRule, own func(string) bool) Info {
	byIdx := make(map[uint32]winAdapter, len(adapters))
	for _, a := range adapters {
		byIdx[a.index] = a
	}
	// Detect full tunnels (holding 0.0.0.0/0 or 0.0.0.0/1+128.0.0.0/1) on VPN
	// adapters so another proxy's full-route TUN is ignored, just like on macOS.
	v4Default := netip.MustParsePrefix("0.0.0.0/0")
	v4Low := netip.MustParsePrefix("0.0.0.0/1")
	v4High := netip.MustParsePrefix("128.0.0.0/1")
	low, high, full := map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}
	for _, r := range routes {
		switch r.prefix {
		case v4Default:
			full[r.ifIndex] = true
		case v4Low:
			low[r.ifIndex] = true
		case v4High:
			high[r.ifIndex] = true
		}
	}
	for idx := range low {
		if high[idx] {
			full[idx] = true
		}
	}

	ifaces := map[string]bool{}
	cidrs := map[netip.Prefix]bool{}
	domains := map[string]bool{}
	dns := map[string]bool{}

	for _, r := range routes {
		a, ok := byIdx[r.ifIndex]
		if !ok || !a.vpn || own(a.name) || full[r.ifIndex] {
			continue
		}
		if skipPrefix(r.prefix) {
			continue
		}
		ifaces[a.name] = true
		cidrs[r.prefix] = true
	}
	for _, a := range adapters {
		if !a.vpn || own(a.name) || full[a.index] {
			continue
		}
		if !ifaces[a.name] && a.dnsSuffix == "" && len(a.dns) == 0 {
			continue
		}
		ifaces[a.name] = true
		if d := a.dnsSuffix; d != "" && d != "local" && !strings.HasSuffix(d, ".arpa") {
			domains[d] = true
		}
		for _, s := range a.dns {
			if addr, err := netip.ParseAddr(s); err == nil && addr.Is4() && !addr.IsLoopback() {
				dns[addr.String()] = true
				cidrs[netip.PrefixFrom(addr, 32)] = true
			}
		}
	}
	for _, rule := range nrpt {
		for _, d := range rule.domains {
			domains[d] = true
		}
		for _, s := range rule.servers {
			if addr, err := netip.ParseAddr(s); err == nil && addr.Is4() && !addr.IsLoopback() {
				dns[addr.String()] = true
				cidrs[netip.PrefixFrom(addr, 32)] = true
			}
		}
	}

	info := Info{Processes: procs}
	for i := range ifaces {
		info.Ifaces = append(info.Ifaces, i)
	}
	sort.Strings(info.Ifaces)

	ps := make([]netip.Prefix, 0, len(cidrs))
	for p := range cidrs {
		ps = append(ps, p)
	}
	for _, p := range collapse(ps) {
		info.CIDRs = append(info.CIDRs, p.String())
	}
	for d := range domains {
		info.Domains = append(info.Domains, d)
	}
	sort.Strings(info.Domains)
	for d := range dns {
		info.DNS = append(info.DNS, d)
	}
	sort.Strings(info.DNS)
	return info
}

func winTCPGateways(pids map[uint32]bool, cidrs []netip.Prefix) []string {
	if len(pids) == 0 || procGetExtendedTcpTable.Find() != nil {
		return nil
	}
	size := uint32(16 * 1024)
	var buf []byte
	for attempt := 0; attempt < 4; attempt++ {
		buf = make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&size)),
			0,
			uintptr(windows.AF_INET),
			uintptr(tcpTableOwnerPIDConns),
			0,
		)
		if r == 0 {
			break
		}
		if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			return nil
		}
		buf = nil
	}
	if len(buf) < 4 {
		return nil
	}
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	rowSize := uint32(unsafe.Sizeof(mibTCPRowOwnerPID{}))
	if int64(4)+int64(count)*int64(rowSize) > int64(len(buf)) {
		return nil
	}
	rows := unsafe.Slice((*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[4])), int(count))

	tunRange := netip.MustParsePrefix("198.18.0.0/15")
	seen := map[netip.Addr]bool{}
	for i := range rows {
		row := &rows[i]
		if !pids[row.OwningPid] || row.RemoteAddr == 0 {
			continue
		}
		var b [4]byte
		*(*uint32)(unsafe.Pointer(&b[0])) = row.RemoteAddr
		a := netip.AddrFrom4(b)
		if a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() {
			continue
		}
		if tunRange.Contains(a) || slices.ContainsFunc(cidrs, func(p netip.Prefix) bool { return p.Contains(a) }) {
			continue
		}
		seen[a] = true
	}
	var addrs []netip.Addr
	for a := range seen {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Less(addrs[j]) })
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}
