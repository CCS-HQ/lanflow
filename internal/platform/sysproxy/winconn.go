package sysproxy

// Windows per-connection proxy logic. WinINet keeps one set of proxy
// options for the LAN and one for every dial-up / VPN (RAS) phone-book
// entry; a PPPoE or dial-up VPN user only gets the system proxy when the
// entry in use carries it. This file decides which connections to touch
// and what to do when some of them fail. It has no build tag so it is unit
// tested on every host; wininet_windows.go does the WinINet and RAS calls
// and sysproxy_windows.go the legacy registry writes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// INTERNET_PER_CONN_FLAGS bits.
const (
	winProxyTypeDirect  = 0x1 // PROXY_TYPE_DIRECT
	winProxyTypeProxy   = 0x2 // PROXY_TYPE_PROXY
	winProxyTypeAutoURL = 0x4 // PROXY_TYPE_AUTO_PROXY_URL
)

// winLAN is the connection name of the LAN settings (pszConnection = NULL).
const winLAN = ""

// winConn is the proxy configuration of one connection: the LAN (Name "")
// or a RAS phone-book entry.
type winConn struct {
	Name       string `json:"name"`
	Flags      uint32 `json:"flags"`
	Server     string `json:"server,omitempty"`
	Bypass     string `json:"bypass,omitempty"`
	AutoConfig string `json:"autoConfig,omitempty"`
}

// winField selects which options of a winConn setConn writes.
type winField uint8

const (
	winFieldFlags winField = 1 << iota
	winFieldServer
	winFieldBypass
	winFieldAutoConfig

	winFieldAll = winFieldFlags | winFieldServer | winFieldBypass | winFieldAutoConfig
)

// winSystem is everything the Windows implementation asks of the system.
type winSystem interface {
	// rasEntries lists the names of the RAS phone-book entries.
	rasEntries() ([]string, error)
	// queryConn reads the options of one connection (winLAN or an entry).
	queryConn(name string) (winConn, error)
	// setConn writes the selected options of c to the connection c.Name.
	setConn(c winConn, fields winField) error
	// notify tells WinINet and running applications to reload the settings.
	notify() error

	// The legacy* methods read and write the per-user registry values
	// (ProxyEnable, ProxyServer, ProxyOverride, AutoConfigURL), which only
	// describe the LAN. They are the fallback when WinINet refuses the LAN.
	legacySet(server, override string) error
	legacyClear() error
	legacySetPAC(url string) error
	legacySnapshot() (windowsSaved, error)
	legacyRestore(saved windowsSaved) error
}

// winUpdateConn reads one connection, applies change and writes fields back.
func winUpdateConn(sys winSystem, name string, fields winField, change func(*winConn)) error {
	c, err := sys.queryConn(name)
	if err != nil {
		return err
	}
	c.Name = name
	change(&c)
	return sys.setConn(c, fields)
}

// winRASNames lists the RAS entries without duplicates (an entry can sit in
// both the user's and the all-users phone book) and without empty names.
func winRASNames(sys winSystem) ([]string, error) {
	names, err := sys.rasEntries()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(names))
	var out []string
	for _, n := range names {
		key := strings.ToLower(n)
		if _, dup := seen[key]; dup || n == "" {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, n)
	}
	return out, nil
}

// winApply makes one change on the LAN and on every RAS entry, then
// notifies. The LAN decides the result: when WinINet refuses it, legacy
// writes the registry values instead, and only a failure of both is an
// error. RAS entries are best effort: a failed enumeration or a failing
// entry never fails the call, so a machine without RAS entries (or without
// a working rasapi32) behaves as it did when only the LAN was written.
func winApply(sys winSystem, fields winField, change func(*winConn), legacy func() error) error {
	if err := winUpdateConn(sys, winLAN, fields, change); err != nil {
		if lerr := legacy(); lerr != nil {
			return lerr
		}
	}
	if names, err := winRASNames(sys); err == nil {
		for _, n := range names {
			_ = winUpdateConn(sys, n, fields, change)
		}
	}
	return sys.notify()
}

// winSet implements Set.
func winSet(sys winSystem, host string, port int, bypass []string) error {
	if host == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("代理地址无效：%s:%d", host, port)
	}
	server, override := formatProxyServer(host, port), formatProxyOverride(bypass)
	return winApply(sys, winFieldFlags|winFieldServer|winFieldBypass, func(c *winConn) {
		// The PAC and auto-detect bits stay as they are, like the
		// registry write that never touched AutoConfigURL.
		c.Flags |= winProxyTypeDirect | winProxyTypeProxy
		c.Server, c.Bypass = server, override
	}, func() error { return sys.legacySet(server, override) })
}

// winClearProxy turns the manual proxy of c off, leaving its server stored.
func winClearProxy(c *winConn) {
	c.Flags = c.Flags&^winProxyTypeProxy | winProxyTypeDirect
}

// winClear implements Clear.
func winClear(sys winSystem) error {
	return winApply(sys, winFieldFlags, winClearProxy, sys.legacyClear)
}

// winSetPAC implements SetPAC: pac is stored and turned on, "" turns PAC
// off and forgets the address (the registry write deleted AutoConfigURL).
// The manual proxy options are left as they are.
func winSetPAC(sys winSystem, pac string) error {
	return winApply(sys, winFieldFlags|winFieldAutoConfig, func(c *winConn) {
		c.Flags |= winProxyTypeDirect
		if pac == "" {
			c.Flags &^= winProxyTypeAutoURL
		} else {
			c.Flags |= winProxyTypeAutoURL
		}
		c.AutoConfig = pac
	}, func() error { return sys.legacySetPAC(pac) })
}

// winSnapshot implements Snapshot. The registry values are always recorded
// (they are what older versions restore from, and the LAN fallback here);
// on top of them come the WinINet options of the LAN and of every RAS
// entry that can be read. An entry that cannot be read is simply absent.
func winSnapshot(sys winSystem) (Saved, error) {
	saved, err := sys.legacySnapshot()
	if err != nil {
		return nil, err
	}
	saved.OS = "windows"
	saved.Conns = nil
	if c, err := sys.queryConn(winLAN); err == nil {
		c.Name = winLAN
		saved.Conns = append(saved.Conns, c)
	}
	if names, err := winRASNames(sys); err == nil {
		for _, n := range names {
			if c, err := sys.queryConn(n); err == nil {
				c.Name = n
				saved.Conns = append(saved.Conns, c)
			}
		}
	}
	return json.Marshal(saved)
}

// winOurs describes what Lanflow wrote to every connection: a manual proxy
// at host:port and/or a PAC address. The zero value matches nothing.
type winOurs struct {
	host string
	port int
	pac  string
}

// winOursFromLAN derives what Lanflow wrote from the LAN's options while
// the LAN still carries it (Set and SetPAC write the same values to every
// connection). Only loopback addresses count, which is all Lanflow ever
// writes, so another program's proxy on the LAN is never taken for ours.
func winOursFromLAN(lan winConn) winOurs {
	var o winOurs
	if lan.Flags&winProxyTypeProxy != 0 {
		if h, p := parseProxyServer(lan.Server); p > 0 && winLoopback(h) {
			o.host, o.port = h, p
		}
	}
	if lan.Flags&winProxyTypeAutoURL != 0 {
		if u, err := url.Parse(lan.AutoConfig); err == nil && winLoopback(u.Hostname()) {
			o.pac = lan.AutoConfig
		}
	}
	return o
}

func winLoopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

// manual reports whether c has the manual proxy on and pointing at ours.
func (o winOurs) manual(c winConn) bool {
	if o.port == 0 || c.Flags&winProxyTypeProxy == 0 {
		return false
	}
	h, p := parseProxyServer(c.Server)
	return p == o.port && strings.EqualFold(h, o.host)
}

// auto reports whether c has PAC on with our address.
func (o winOurs) auto(c winConn) bool {
	return o.pac != "" && c.Flags&winProxyTypeAutoURL != 0 && c.AutoConfig == o.pac
}

// winDropOurs switches off whatever on the entry name still is ours (the
// manual proxy, the PAC address) and leaves everything else of it alone.
// It reports whether the entry was written.
func winDropOurs(sys winSystem, name string, ours winOurs) bool {
	c, err := sys.queryConn(name)
	if err != nil {
		return false
	}
	c.Name = name
	manual, auto := ours.manual(c), ours.auto(c)
	if !manual && !auto {
		return false
	}
	fields := winFieldFlags
	if manual {
		winClearProxy(&c)
	}
	if auto {
		c.Flags = c.Flags&^winProxyTypeAutoURL | winProxyTypeDirect
		c.AutoConfig = ""
		fields |= winFieldAutoConfig
	}
	return sys.setConn(c, fields) == nil
}

// winRestore implements Restore.
//
// The LAN goes back through WinINet when the snapshot has its options, and
// through the registry values otherwise or when WinINet refuses.
//
// A RAS entry in the snapshot gets its options back. An entry that is not
// in it (the snapshot predates per-connection support, the phone book or
// the entry could not be read then, or the entry was created since) has
// no recorded state, so only what Lanflow put on it is taken off: its
// manual proxy or PAC address is switched off when it still equals what
// the LAN carries from us right now, and a proxy the user gave that entry
// is left as it is. Entries that are gone are skipped. As in winApply, RAS
// failures never fail the call.
func winRestore(sys winSystem, s Saved) error {
	var saved windowsSaved
	if err := json.Unmarshal(s, &saved); err != nil || saved.OS != "windows" {
		return errors.New("保存的系统代理设置无法识别")
	}
	byName := winConnsByName(saved)
	// Read what the LAN carries from us before it is put back.
	var ours winOurs
	if cur, err := sys.queryConn(winLAN); err == nil {
		ours = winOursFromLAN(cur)
	}
	lan, ok := byName[winLAN]
	if !ok || sys.setConn(lan, winFieldAll) != nil {
		if err := sys.legacyRestore(saved); err != nil {
			return err
		}
	}
	if names, err := winRASNames(sys); err == nil {
		for _, n := range names {
			if c, ok := byName[strings.ToLower(n)]; ok {
				c.Name = n
				_ = sys.setConn(c, winFieldAll)
			} else {
				winDropOurs(sys, n, ours)
			}
		}
	}
	return sys.notify()
}

func winConnsByName(saved windowsSaved) map[string]winConn {
	byName := make(map[string]winConn, len(saved.Conns))
	for _, c := range saved.Conns {
		byName[strings.ToLower(c.Name)] = c
	}
	return byName
}

// winReleaseDialUp implements ReleaseDialUp: the LAN is no longer ours
// (the user or another program changed it) and is not touched, but the RAS
// entries Set or SetPAC wrote may still point at Lanflow. Every entry that
// still does gets its snapshot options back, or, without any, has ours
// switched off; entries someone has changed since are left alone. Best
// effort: nothing here is reported as an error, and the system is only
// notified when an entry was written.
func winReleaseDialUp(sys winSystem, s Saved, host string, port int, pac string) error {
	ours := winOurs{host: host, port: port, pac: pac}
	if host == "" || port <= 0 {
		ours.host, ours.port = "", 0
	}
	var saved windowsSaved
	if json.Unmarshal(s, &saved) != nil || saved.OS != "windows" {
		saved = windowsSaved{}
	}
	byName := winConnsByName(saved)
	names, err := winRASNames(sys)
	if err != nil {
		return nil
	}
	changed := false
	for _, n := range names {
		c, ok := byName[strings.ToLower(n)]
		if !ok {
			changed = winDropOurs(sys, n, ours) || changed
			continue
		}
		cur, err := sys.queryConn(n)
		if err != nil || !ours.manual(cur) && !ours.auto(cur) {
			continue
		}
		c.Name = n
		if sys.setConn(c, winFieldAll) == nil {
			changed = true
		}
	}
	if changed {
		_ = sys.notify()
	}
	return nil
}
