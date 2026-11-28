package sysproxy

//lint:file-ignore U1000 some of this file is used on Linux only.

// The GNOME system proxy (gsettings org.gnome.system.proxy), which GNOME,
// Cinnamon, Budgie, MATE and most apps built on GLib, Chromium and Firefox
// read on Linux. Platform-independent so it can be unit tested on any host;
// sysproxy_linux.go and snapshot_linux.go wire it to the system.
//
// One setting, mode, chooses between none, manual (the HTTP, HTTPS and
// SOCKS proxies) and auto (a PAC URL), so the manual proxies and PAC
// exclude each other here: Clear turns off only a manual proxy and
// SetPAC("") only PAC, leaving whatever else the user chose.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	gsettings      = "gsettings"
	gnomeSchema    = "org.gnome.system.proxy"
	gnomeSaveOS    = "linux-gnome"
	gnomeModeNone  = "none"
	gnomeModeHand  = "manual"
	gnomeModeAuto  = "auto"
	gnomeMemoryMsg = "memory" // GLib's warning when it falls back to the memory backend
)

// gnomeProxies are the per-protocol schemas Set writes.
var gnomeProxies = []string{"http", "https", "socks"}

// gnomeProxy is one protocol's host and port.
type gnomeProxy struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

// gnomeSaved is the snapshot: every key Set, Clear and SetPAC write.
type gnomeSaved struct {
	OS      string                `json:"os"`
	Mode    string                `json:"mode"`
	PACURL  string                `json:"pac,omitempty"`
	Ignore  []string              `json:"ignore,omitempty"`
	Proxies map[string]gnomeProxy `json:"proxies,omitempty"`
}

// gnomeRun runs gsettings and turns GLib's silent fallback to the memory
// backend (no dconf, no session bus) into an error: the value would be
// accepted and lost.
func gnomeRun(ctx context.Context, run executor, args ...string) (string, error) {
	out, err := run(ctx, gsettings, args...)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			return "", errors.New("没有找到 gsettings：当前桌面不支持由澜流设置系统代理，请在应用或浏览器中手动填写代理地址")
		}
		if strings.Contains(err.Error(), "No such schema") {
			return "", errors.New("当前桌面没有 GNOME 的代理设置（org.gnome.system.proxy），请在应用或浏览器中手动填写代理地址")
		}
		return out, err
	}
	if strings.Contains(out, "GLib-GIO") && strings.Contains(out, gnomeMemoryMsg) {
		return out, errors.New("无法写入桌面设置（dconf 不可用），请在应用或浏览器中手动填写代理地址")
	}
	return out, nil
}

func gnomeGetKey(ctx context.Context, run executor, schema, key string) (string, error) {
	out, err := gnomeRun(ctx, run, "get", schema, key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(lastLine(out)), nil
}

func gnomeSetKey(ctx context.Context, run executor, schema, key, value string) error {
	_, err := gnomeRun(ctx, run, "set", schema, key, value)
	return err
}

// lastLine drops warnings GLib prints before the value.
func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// gvString quotes s as a GVariant string.
func gvString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// gvStrings writes ss as a GVariant array of strings.
func gvStrings(ss []string) string {
	if len(ss) == 0 {
		return "@as []"
	}
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = gvString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// parseGVStrings reads the strings of a GVariant value as gsettings prints
// it: 'manual', ['a', "b'c"], @as [] — every quoted string, in order.
func parseGVStrings(v string) []string {
	var out []string
	for i := 0; i < len(v); i++ {
		q := v[i]
		if q != '\'' && q != '"' {
			continue
		}
		var b strings.Builder
		i++
		for ; i < len(v) && v[i] != q; i++ {
			if v[i] == '\\' && i+1 < len(v) {
				i++
			}
			b.WriteByte(v[i])
		}
		out = append(out, b.String())
	}
	return out
}

func parseGVString(v string) string {
	if s := parseGVStrings(v); len(s) > 0 {
		return s[0]
	}
	return ""
}

// gnomeBypass maps the bypass list to GNOME's ignore-hosts, which takes
// host names, *.suffix patterns, addresses and CIDR ranges; "<local>" is
// WinINet's and means nothing here.
func gnomeBypass(bypass []string) []string {
	out := make([]string, 0, len(bypass))
	for _, b := range bypass {
		if b = strings.TrimSpace(b); b != "" && b != "<local>" {
			out = append(out, b)
		}
	}
	return out
}

func gnomeSet(ctx context.Context, run executor, host string, port int, bypass []string) error {
	if host == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("代理地址无效：%s", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	for _, p := range gnomeProxies {
		schema := gnomeSchema + "." + p
		if err := gnomeSetKey(ctx, run, schema, "host", gvString(host)); err != nil {
			return fmt.Errorf("设置系统代理失败：%w", err)
		}
		if err := gnomeSetKey(ctx, run, schema, "port", strconv.Itoa(port)); err != nil {
			return fmt.Errorf("设置系统代理失败：%w", err)
		}
	}
	if err := gnomeSetKey(ctx, run, gnomeSchema, "ignore-hosts", gvStrings(gnomeBypass(bypass))); err != nil {
		return fmt.Errorf("设置系统代理失败：%w", err)
	}
	// The mode last: apps see the change once the proxies are all there.
	if err := gnomeSetKey(ctx, run, gnomeSchema, "mode", gvString(gnomeModeHand)); err != nil {
		return fmt.Errorf("设置系统代理失败：%w", err)
	}
	return nil
}

func gnomeMode(ctx context.Context, run executor) (string, error) {
	v, err := gnomeGetKey(ctx, run, gnomeSchema, "mode")
	if err != nil {
		return "", err
	}
	return parseGVString(v), nil
}

func gnomeClear(ctx context.Context, run executor) error {
	mode, err := gnomeMode(ctx, run)
	if err != nil {
		return fmt.Errorf("关闭系统代理失败：%w", err)
	}
	if mode != gnomeModeHand {
		return nil // off already, or PAC, which Clear leaves alone
	}
	if err := gnomeSetKey(ctx, run, gnomeSchema, "mode", gvString(gnomeModeNone)); err != nil {
		return fmt.Errorf("关闭系统代理失败：%w", err)
	}
	return nil
}

func gnomeSetPAC(ctx context.Context, run executor, url string) error {
	if url == "" {
		mode, err := gnomeMode(ctx, run)
		if err != nil {
			return fmt.Errorf("关闭自动代理配置失败：%w", err)
		}
		if mode != gnomeModeAuto {
			return nil
		}
		if err := gnomeSetKey(ctx, run, gnomeSchema, "mode", gvString(gnomeModeNone)); err != nil {
			return fmt.Errorf("关闭自动代理配置失败：%w", err)
		}
		return nil
	}
	if err := gnomeSetKey(ctx, run, gnomeSchema, "autoconfig-url", gvString(url)); err != nil {
		return fmt.Errorf("设置自动代理配置失败：%w", err)
	}
	if err := gnomeSetKey(ctx, run, gnomeSchema, "mode", gvString(gnomeModeAuto)); err != nil {
		return fmt.Errorf("设置自动代理配置失败：%w", err)
	}
	return nil
}

// gnomeGet reads only what the mode makes current: the first manual proxy
// with a host and port (HTTP, then HTTPS, then SOCKS), or the PAC URL.
func gnomeGet(ctx context.Context, run executor) (State, error) {
	fail := func(err error) (State, error) { return State{}, fmt.Errorf("无法读取系统代理设置：%w", err) }
	mode, err := gnomeMode(ctx, run)
	if err != nil {
		return fail(err)
	}
	var st State
	switch mode {
	case gnomeModeHand:
		for _, p := range gnomeProxies {
			pr, err := gnomeReadProxy(ctx, run, p)
			if err != nil {
				return fail(err)
			}
			if pr.Host != "" && pr.Port > 0 {
				st.Enabled, st.Host, st.Port = true, pr.Host, pr.Port
				break
			}
		}
		v, err := gnomeGetKey(ctx, run, gnomeSchema, "ignore-hosts")
		if err != nil {
			return fail(err)
		}
		st.Bypass = parseGVStrings(v)
	case gnomeModeAuto:
		v, err := gnomeGetKey(ctx, run, gnomeSchema, "autoconfig-url")
		if err != nil {
			return fail(err)
		}
		st.PACURL = parseGVString(v)
	}
	return st, nil
}

func gnomeReadProxy(ctx context.Context, run executor, proto string) (gnomeProxy, error) {
	schema := gnomeSchema + "." + proto
	h, err := gnomeGetKey(ctx, run, schema, "host")
	if err != nil {
		return gnomeProxy{}, err
	}
	pv, err := gnomeGetKey(ctx, run, schema, "port")
	if err != nil {
		return gnomeProxy{}, err
	}
	port, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(pv, "int32 ")))
	return gnomeProxy{Host: parseGVString(h), Port: port}, nil
}

// gnomeRead reads every key the snapshot keeps.
func gnomeRead(ctx context.Context, run executor) (gnomeSaved, error) {
	s := gnomeSaved{OS: gnomeSaveOS, Proxies: map[string]gnomeProxy{}}
	mode, err := gnomeMode(ctx, run)
	if err != nil {
		return s, err
	}
	s.Mode = mode
	v, err := gnomeGetKey(ctx, run, gnomeSchema, "autoconfig-url")
	if err != nil {
		return s, err
	}
	s.PACURL = parseGVString(v)
	v, err = gnomeGetKey(ctx, run, gnomeSchema, "ignore-hosts")
	if err != nil {
		return s, err
	}
	s.Ignore = parseGVStrings(v)
	for _, p := range gnomeProxies {
		pr, err := gnomeReadProxy(ctx, run, p)
		if err != nil {
			return s, err
		}
		s.Proxies[p] = pr
	}
	return s, nil
}

func gnomeSnapshot(ctx context.Context, run executor) (Saved, error) {
	s, err := gnomeRead(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("无法读取系统代理设置：%w", err)
	}
	return json.Marshal(s)
}

func gnomeRestore(ctx context.Context, run executor, saved Saved) error {
	var s gnomeSaved
	if err := json.Unmarshal(saved, &s); err != nil || s.OS != gnomeSaveOS {
		return errors.New("系统代理快照无法识别")
	}
	fail := func(err error) error { return fmt.Errorf("恢复系统代理失败：%w", err) }
	for _, p := range gnomeProxies {
		schema := gnomeSchema + "." + p
		pr := s.Proxies[p]
		if err := gnomeSetKey(ctx, run, schema, "host", gvString(pr.Host)); err != nil {
			return fail(err)
		}
		if err := gnomeSetKey(ctx, run, schema, "port", strconv.Itoa(pr.Port)); err != nil {
			return fail(err)
		}
	}
	if err := gnomeSetKey(ctx, run, gnomeSchema, "ignore-hosts", gvStrings(s.Ignore)); err != nil {
		return fail(err)
	}
	if err := gnomeSetKey(ctx, run, gnomeSchema, "autoconfig-url", gvString(s.PACURL)); err != nil {
		return fail(err)
	}
	mode := s.Mode
	switch mode {
	case gnomeModeNone, gnomeModeHand, gnomeModeAuto:
	default:
		mode = gnomeModeNone
	}
	if err := gnomeSetKey(ctx, run, gnomeSchema, "mode", gvString(mode)); err != nil {
		return fail(err)
	}
	return nil
}
