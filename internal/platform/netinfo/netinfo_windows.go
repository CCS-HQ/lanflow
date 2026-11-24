//go:build windows

package netinfo

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"lanflow/internal/model"
)

// prepareCommand hides the console window that netsh would otherwise flash.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

const ifTypePropVirtual = 53 // IF_TYPE_PROP_VIRTUAL (Wintun, Hyper-V and other virtual adapters)

// adapter is the subset of IP_ADAPTER_ADDRESSES we present.
type adapter struct {
	name      string
	guid      string // AdapterName ANSI GUID ("{...}")
	desc      string // interface Description (e.g. "Wintun Userspace Tunnel")
	index     uint32 // IPv4 interface index (IfIndex)
	ipv6Index uint32 // IPv6 interface index (Ipv6IfIndex)
	ipv4      string
	ipv6      string
	gateway   string
	dns       []string
	metric    uint32
	metric6   uint32
	wifi      bool
	tunnel    bool
	vpn       bool // true for Wintun / TUN / VPN drivers (excludes VM switches)
}

func get(parent context.Context) (model.NetInfo, error) {
	ctx, cancel := withBudget(parent)
	defer cancel()

	var info model.NetInfo
	a, err := primaryAdapter()
	if err != nil || a == nil {
		info.LANIP = lanAddress()
		if info.LANIP == "" {
			return info, errors.New("未检测到可用的网络连接")
		}
		return info, nil
	}
	info.Interface = a.name
	info.LANIP = a.ipv4
	info.IPv6 = a.ipv6
	info.Gateway = a.gateway
	info.DNS = a.dns
	if a.wifi {
		if ssid, ok := wlanSSID(a.guid); ok {
			info.SSID = ssid
		} else if out, err := runCommand(ctx, "netsh", "wlan", "show", "interfaces"); err == nil {
			info.SSID = parseNetshWlan(oemToUTF8(out), a.name)
		}
	}
	return info, nil
}

// primaryAdapter picks the up, non-loopback adapter that has an IPv4
// default gateway with the lowest IPv4 metric. Tunnel adapters (Mihomo's
// TUN uses 198.18.0.0/15) lose to physical ones so the LAN view stays
// meaningful while TUN mode is on.
func primaryAdapter() (*adapter, error) {
	adapters, err := listAdapters()
	if err != nil {
		return nil, err
	}
	var best *adapter
	for i := range adapters {
		a := &adapters[i]
		if a.gateway == "" {
			continue
		}
		if best == nil ||
			(best.tunnel && !a.tunnel) ||
			(best.tunnel == a.tunnel && a.metric < best.metric) {
			best = a
		}
	}
	return best, nil
}

func listAdapters() ([]adapter, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST
	size := uint32(16 * 1024)
	var buf []byte
	for attempt := 0; attempt < 4; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, err
		}
		buf = nil
	}
	if buf == nil {
		return nil, windows.ERROR_BUFFER_OVERFLOW
	}
	if size == 0 {
		return nil, nil
	}

	var out []adapter
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp || aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		name := windows.UTF16PtrToString(aa.FriendlyName)
		desc := windows.UTF16PtrToString(aa.Description)
		vpn := aa.IfType == windows.IF_TYPE_TUNNEL || isVPNDesc(desc) || isVPNName(name)
		tun := vpn || aa.IfType == ifTypePropVirtual || isVirtualDesc(desc) || isVirtualName(name)
		a := adapter{
			name:      name,
			guid:      windows.BytePtrToString(aa.AdapterName),
			desc:      desc,
			index:     aa.IfIndex,
			ipv6Index: aa.Ipv6IfIndex,
			metric:    aa.Ipv4Metric,
			metric6:   aa.Ipv6Metric,
			wifi:      aa.IfType == windows.IF_TYPE_IEEE80211,
			tunnel:    tun,
			vpn:       vpn,
		}
		for u := aa.FirstUnicastAddress; u != nil; u = u.Next {
			ip := u.Address.IP()
			if v4 := ip.To4(); v4 != nil {
				if a.ipv4 == "" {
					a.ipv4 = v4.String()
				}
				if v4[0] == 198 && v4[1]&0xFE == 18 { // 198.18.0.0/15: Mihomo TUN
					a.tunnel = true
					a.vpn = true
				}
			} else if a.ipv6 == "" && isPublicIPv6(ip) {
				a.ipv6 = ip.String()
			}
		}
		for g := aa.FirstGatewayAddress; g != nil; g = g.Next {
			ip := g.Address.IP()
			if v4 := ip.To4(); v4 != nil && !v4.IsUnspecified() {
				a.gateway = v4.String()
				break
			}
		}
		for d := aa.FirstDnsServerAddress; d != nil; d = d.Next {
			ip := d.Address.IP()
			if ip == nil || isWindowsPlaceholderDNS(ip) {
				continue
			}
			a.dns = append(a.dns, ip.String())
		}
		a.dns = dedup(a.dns)
		out = append(out, a)
	}
	return out, nil
}

// isWindowsPlaceholderDNS filters the fec0:0:0:ffff::1-3 site-local
// addresses Windows lists on adapters without configured DNS servers.
func isWindowsPlaceholderDNS(ip net.IP) bool {
	return ip.To4() == nil && len(ip) == net.IPv6len && ip[0] == 0xfe && ip[1] == 0xc0
}

func isVPNDesc(desc string) bool {
	d := strings.ToLower(desc)
	for _, h := range []string{"wintun", "tap-windows", "tap-provider", "wireguard", "tailscale", "zerotier", "openvpn", "sangfor", "atrust", "cisco anyconnect", "fortinet", "clash", "mihomo", "sing-box", "singbox", "tun"} {
		if strings.Contains(d, h) {
			return true
		}
	}
	return false
}

func isVPNName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if isTunnelName(n) {
		return true
	}
	for _, h := range []string{"wintun", "meta", "mihomo", "clash", "lanflow", "sing-box", "singbox", "wireguard", "tailscale", "zerotier", "openvpn", "sangfor", "atrust"} {
		if strings.HasPrefix(n, h) || strings.Contains(n, h) {
			return true
		}
	}
	return false
}

func isVirtualDesc(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "hyper-v") || strings.Contains(d, "vmware") || strings.Contains(d, "virtualbox")
}

// oemToUTF8 converts OEM-codepage command output (netsh) to UTF-8.
func oemToUTF8(s string) string {
	if s == "" {
		return ""
	}
	b := []byte(s)
	const cpOEMCP = 1
	n, err := windows.MultiByteToWideChar(cpOEMCP, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n <= 0 {
		return s
	}
	w := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(cpOEMCP, 0, &b[0], int32(len(b)), &w[0], n); err != nil {
		return s
	}
	return string(utf16.Decode(w))
}

var (
	modWlanAPI             = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle     = modWlanAPI.NewProc("WlanOpenHandle")
	procWlanCloseHandle    = modWlanAPI.NewProc("WlanCloseHandle")
	procWlanFreeMemory     = modWlanAPI.NewProc("WlanFreeMemory")
	procWlanEnumInterfaces = modWlanAPI.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface = modWlanAPI.NewProc("WlanQueryInterface")
)

type wlanInterfaceInfo struct {
	InterfaceGuid windows.GUID
	Description   [256]uint16
	State         uint32
}

type wlanInterfaceInfoList struct {
	NumberOfItems uint32
	Index         uint32
	InterfaceInfo [1]wlanInterfaceInfo
}

type wlanConnectionAttributes struct {
	State          uint32
	ConnectionMode uint32
	ProfileName    [256]uint16
	SSIDLength     uint32
	SSID           [32]byte
}

// wlanSSID queries the connected Wi-Fi SSID natively through wlanapi.dll
// without flashing a console or parsing localized netsh output.
func wlanSSID(guidStr string) (string, bool) {
	if procWlanOpenHandle.Find() != nil || procWlanEnumInterfaces.Find() != nil ||
		procWlanQueryInterface.Find() != nil || procWlanFreeMemory.Find() != nil ||
		procWlanCloseHandle.Find() != nil {
		return "", false
	}
	var negVer uint32
	var handle windows.Handle
	r, _, _ := procWlanOpenHandle.Call(2, 0, uintptr(unsafe.Pointer(&negVer)), uintptr(unsafe.Pointer(&handle)))
	if r != 0 {
		return "", false
	}
	defer procWlanCloseHandle.Call(uintptr(handle), 0)

	var list *wlanInterfaceInfoList
	r, _, _ = procWlanEnumInterfaces.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&list)))
	if r != 0 || list == nil {
		return "", false
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(list)))

	items := unsafe.Slice(&list.InterfaceInfo[0], int(list.NumberOfItems))
	wantGUID, err := windows.GUIDFromString(guidStr)
	hasWantGUID := err == nil && guidStr != ""

	const (
		wlanStateConnected              = 1
		wlanIntfOpcodeCurrentConnection = 7
	)
	var picked *windows.GUID
	for i := range items {
		it := &items[i]
		if hasWantGUID && it.InterfaceGuid == wantGUID {
			if it.State != wlanStateConnected {
				return "", true
			}
			picked = &it.InterfaceGuid
			break
		}
		if picked == nil && it.State == wlanStateConnected {
			picked = &it.InterfaceGuid
		}
	}
	if picked == nil {
		return "", true
	}

	var dataSize uint32
	var attr *wlanConnectionAttributes
	r, _, _ = procWlanQueryInterface.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(picked)),
		wlanIntfOpcodeCurrentConnection,
		0,
		uintptr(unsafe.Pointer(&dataSize)),
		uintptr(unsafe.Pointer(&attr)),
		0,
	)
	if r != 0 || attr == nil {
		return "", false
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(attr)))
	if attr.SSIDLength == 0 || attr.SSIDLength > uint32(len(attr.SSID)) {
		return "", true
	}
	return string(attr.SSID[:attr.SSIDLength]), true
}
