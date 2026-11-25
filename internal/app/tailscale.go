package app

import (
	"fmt"
	"net/netip"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/tailscale"
	"lanflow/internal/profile"
)

// Tailscale 兼容 (Tailscale running beside Lanflow). Tailscale routes the
// tailnet (100.64.0.0/10, fd7a:115c:a1e0::/48 and subnet routes) into its
// own interface and answers MagicDNS names (*.ts.net) at 100.100.100.100.
// Left alone, Lanflow's fake-ip DNS hands out fake addresses for tailnet
// names, the subscription's MATCH rule sends tailnet requests to a remote
// node, and with the TUN on the core binds its outbounds to the physical
// interface, where the tailnet is unreachable. While Tailscale runs (and
// the setting is on, the default) the composed config therefore
// (profile/tailscale.go):
//   - keeps the tailnet ranges out of the TUN (route-exclude-address),
//     except a range holding the system's DNS server, which stays in the
//     TUN so the DNS hijack keeps working;
//   - sends the tailnet ranges and the MagicDNS suffix to a hidden direct
//     outbound bound to Tailscale's interface (profile.TailscaleOutbound),
//     first among the rules, so what still reaches the core (the system
//     proxy, apps set to use the proxy) gets there too;
//   - never gives tailnet / split DNS names a fake IP, never sniffs them,
//     and resolves them with Tailscale's DNS through that outbound, ahead
//     of every other nameserver-policy entry;
//
// and the system proxy bypasses the tailnet. Detection is repeated every
// 10 s (30 s while the window is hidden). Tailscale going away is only
// acted on after tailscaleGrace detections in a row, so a short gap (a
// network change, sleep) does not reload the core twice.

// tailscaleGrace is how many detections in a row must miss Tailscale
// before its layer is taken out.
const tailscaleGrace = 3

// tailscaleDNSOnly is resolved by MagicDNS but gets no rule: names of
// other tailnets under it (Funnel) are public addresses.
const tailscaleDNSOnly = "ts.net"

// scanTailscale looks for Tailscale when the setting is on (the zero Info
// otherwise). It runs the detector, which may take seconds, and touches no
// state. Never call with mu held.
func (s *Service) scanTailscale() tailscale.Info {
	s.mu.Lock()
	on := s.state.settings.TailscaleCompat
	detect := s.d.tsDetect
	s.mu.Unlock()
	if !on || detect == nil || s.rootCtx.Err() != nil {
		return tailscale.Info{}
	}
	ctx, cancel := s.svcCtx(8 * time.Second)
	defer cancel()
	return detect(ctx, isOwnTUN)
}

// detectTailscale detects and stores the result at once (no grace): at a
// start and when the setting changes. It reports whether the layer
// changed. Never call with mu held.
func (s *Service) detectTailscale() bool {
	return s.storeTailscale(s.scanTailscale(), true)
}

// storeTailscale records a detection result and reports whether the layer
// changed. Without settle, a miss is only taken after tailscaleGrace in a
// row. A changed address alone refreshes the status. Never call with mu
// held.
func (s *Service) storeTailscale(info tailscale.Info, settle bool) bool {
	s.mu.Lock()
	set := s.state.settings
	if !set.TailscaleCompat {
		info = tailscale.Info{}
	}
	prev := s.state.tailscale
	if !info.Running() && prev.Running() && set.TailscaleCompat && !settle {
		s.state.tsMisses++
		if s.state.tsMisses < tailscaleGrace {
			s.mu.Unlock()
			return false
		}
	}
	s.state.tsMisses = 0
	if info.Key() == prev.Key() {
		addrs := !slices.Equal(info.Addrs, prev.Addrs)
		s.state.tailscale = info
		if addrs {
			s.state.st.Tailscale = tailscaleStatus(info, tailscaleInput(info, set, s.state.net.DNS))
		}
		s.mu.Unlock()
		if addrs {
			s.em.emit(TopicStatus)
		}
		return false
	}
	s.state.tailscale = info
	ts := tailscaleInput(info, set, s.state.net.DNS)
	s.state.st.Tailscale = tailscaleStatus(info, ts)
	tunOn := set.TUN
	first := info.Running() && !s.state.tsNotified
	if first {
		s.state.tsNotified = true
	}
	s.mu.Unlock()
	if info.Running() {
		s.alog.Infof("Tailscale: interface %s, suffix %q, MagicDNS %v, exit node %v, routes %v, resolvers %d", info.Iface, info.Suffix, info.MagicDNS, info.ExitNode, info.Routes, len(info.Resolvers))
	} else {
		s.alog.Infof("Tailscale: not running (compatibility setting %s)", onOff(set.TailscaleCompat))
	}
	s.em.emit(TopicStatus)
	if first {
		s.notify(Notice{Kind: "toast", Title: i18n.T("检测到 Tailscale，已启用兼容模式"),
			Detail: i18n.T("tailnet 地址和 MagicDNS 域名将直接经 Tailscale 访问，不经过澜流的节点和 TUN。")})
	}
	if info.ExitNode && !prev.ExitNode && tunOn {
		s.notify(Notice{Kind: "error", Title: i18n.T("Tailscale 正在使用出口节点"),
			Detail: i18n.T("出口节点和澜流的 TUN 都会接管全部流量，同时开启会互相冲突。请在 Tailscale 中关闭出口节点，或关闭澜流的 TUN 只用系统代理。")})
	}
	return true
}

// tailscaleStatus is the Status.Tailscale for a detection result and the
// layer built from it.
func tailscaleStatus(i tailscale.Info, ts profile.Tailscale) model.TailscaleState {
	st := model.TailscaleState{
		Running: i.Running(), Interface: i.Iface, Addresses: i.Addrs, Suffix: i.Suffix, ExitNode: i.ExitNode,
		Applied: ts.Iface != "",
	}
	if st.Applied {
		st.Outbound = profile.TailscaleOutbound
		st.CIDRs = append(append([]string(nil), ts.ExcludeCIDRs...), ts.DirectCIDRs...)
		st.Domains = append([]string(nil), ts.Domains...)
		for _, e := range ts.DNS {
			for _, sv := range e.Servers {
				if !slices.Contains(st.DNS, sv) {
					st.DNS = append(st.DNS, sv)
				}
			}
			if !slices.Contains(st.Domains, e.Domain) {
				st.Domains = append(st.Domains, e.Domain)
			}
		}
	}
	return st
}

// tailscaleInput builds the composer's layer from the detection result,
// the user's lists (Settings.Tailscale) and the system's DNS servers. The
// zero value means no layer.
func tailscaleInput(i tailscale.Info, set model.Settings, sysDNS []string) profile.Tailscale {
	if !set.TailscaleCompat || !i.Running() {
		return profile.Tailscale{}
	}
	cfg := cleanTailscaleConfig(set.Tailscale)
	ts := profile.Tailscale{Iface: i.Iface}

	// a range holding the system's DNS server stays in the TUN, so the
	// hijack sees the queries sent to it (Tailscale's own resolver is the
	// exception: it is only reachable through Tailscale)
	var dnsAddrs []netip.Addr
	for _, s := range sysDNS {
		if a, err := netip.ParseAddr(s); err == nil && a.Unmap() != tailscale.Quad100 && !tailscale.ULA.Contains(a) {
			dnsAddrs = append(dnsAddrs, a.Unmap())
		}
	}
	add := func(dst *[]string, v string) {
		if v != "" && !slices.Contains(*dst, v) {
			*dst = append(*dst, v)
		}
	}
	ranges := append([]string{tailscale.CGNAT.String(), tailscale.ULA.String()}, i.Routes...)
	ranges = append(ranges, cfg.Routes...)
	for _, r := range ranges {
		p, err := netip.ParsePrefix(r)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(dnsAddrs, p.Contains) {
			add(&ts.DirectCIDRs, p.String())
		} else {
			add(&ts.ExcludeCIDRs, p.String())
		}
	}

	// names: the tailnet's suffix (from the CLI, or a resolver serving a
	// *.ts.net domain) and the user's go to Tailscale; split DNS domains
	// only get Tailscale's DNS
	serversOf := map[string][]string{}
	for _, r := range i.Resolvers {
		serversOf[r.Domain] = r.Servers
	}
	if i.Suffix != "" {
		add(&ts.Domains, i.Suffix)
	}
	for _, r := range i.Resolvers {
		if strings.HasSuffix(r.Domain, "."+tailscaleDNSOnly) {
			add(&ts.Domains, r.Domain)
		}
	}
	for _, d := range cfg.Domains {
		add(&ts.Domains, d)
	}
	quad := []string{tailscale.Quad100.String()}
	seen := map[string]bool{}
	dns := func(d string, servers []string) {
		if !seen[d] && len(servers) > 0 {
			seen[d] = true
			ts.DNS = append(ts.DNS, profile.DomainDNS{Domain: d, Servers: servers})
		}
	}
	for _, d := range ts.Domains {
		switch {
		case slices.Contains(cfg.Domains, d) && len(cfg.DNS) > 0:
			dns(d, cfg.DNS)
		case len(serversOf[d]) > 0:
			dns(d, serversOf[d])
		default:
			dns(d, quad)
		}
	}
	for _, r := range i.Resolvers {
		dns(r.Domain, r.Servers)
	}
	if i.MagicDNS || i.Suffix != "" {
		dns(tailscaleDNSOnly, quad)
	}
	return ts
}

// tailscaleBypass extends the system proxy bypass list with the tailnet:
// its ranges and every name the layer resolves with Tailscale's DNS.
func tailscaleBypass(base []string, ts profile.Tailscale) []string {
	out := append([]string(nil), base...)
	add := func(v string) {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	for _, e := range ts.DNS {
		add(e.Domain)
		add("*." + e.Domain)
	}
	for _, d := range ts.Domains {
		add(d)
		add("*." + d)
	}
	for _, c := range ts.ExcludeCIDRs {
		add(c)
	}
	for _, c := range ts.DirectCIDRs {
		add(c)
	}
	return out
}

// tailscaleLocked is the layer for the current state. Call with mu held.
func (s *Service) tailscaleLocked() profile.Tailscale {
	return tailscaleInput(s.state.tailscale, s.state.settings, s.state.net.DNS)
}

// tailscaleOwn extends isOwnTUN with Tailscale's interface while its layer
// is on, so the aTrust detection never takes the tailnet for aTrust's.
func (s *Service) tailscaleOwn() func(string) bool {
	s.mu.Lock()
	iface := ""
	if s.state.settings.TailscaleCompat {
		iface = s.state.tailscale.Iface
	}
	s.mu.Unlock()
	return func(name string) bool { return isOwnTUN(name) || (iface != "" && name == iface) }
}

// tailscaleSettingsChanged reports whether going from old to in (already
// stored) changes the layer, and refreshes Status.Tailscale when it does.
// Never call with mu held.
func (s *Service) tailscaleSettingsChanged(old, in model.Settings) bool {
	if reflect.DeepEqual(old.Tailscale, in.Tailscale) && old.TailscaleCompat == in.TailscaleCompat {
		return false
	}
	s.mu.Lock()
	info, sysDNS := s.state.tailscale, s.state.net.DNS
	before, after := tailscaleInput(info, old, sysDNS), tailscaleInput(info, in, sysDNS)
	if reflect.DeepEqual(before, after) {
		s.mu.Unlock()
		return false
	}
	s.state.st.Tailscale = tailscaleStatus(info, after)
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return true
}

// recheckTailscale detects Tailscale again and, when the layer changes,
// re-applies the config, the system proxy bypass and the system DNS. The
// detection runs without opMu; opMu is only taken when the result differs
// from the stored one.
func (s *Service) recheckTailscale() {
	info := s.scanTailscale()
	s.mu.Lock()
	same := info.Key() == s.state.tailscale.Key()
	s.mu.Unlock()
	if same {
		s.storeTailscale(info, false) // addresses only
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.rootCtx.Err() != nil {
		return
	}
	if s.storeTailscale(info, false) {
		s.reapplyLayers(i18n.T("Tailscale 兼容设置未能生效"))
	}
}

// watchTailscale polls for Tailscale until shutdown.
func (s *Service) watchTailscale() {
	s.recheckTailscale()
	t := time.NewTicker(coexistInterval)
	defer t.Stop()
	ticks := 0
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			ticks++
			if s.background.Load() && ticks%coexistTicksHidden != 0 {
				continue
			}
			s.recheckTailscale()
		}
	}
}

// tailscaleTUNConflict words the error for Lanflow's TUN while another TUN
// owns the default route, when that one is Tailscale's exit node ("" when
// it is not).
func (s *Service) tailscaleTUNConflict(other string) string {
	s.mu.Lock()
	info := s.state.tailscale
	s.mu.Unlock()
	if !info.Running() || other != info.Iface {
		return ""
	}
	return i18n.T("Tailscale 正在使用出口节点，接管了全部流量。请在 Tailscale 中关闭出口节点后再开启澜流的 TUN；不关闭时可只用系统代理，tailnet 仍可直接访问")
}

// cleanTailscaleConfig normalises the user's lists: valid entries only,
// canonical and without duplicates.
func cleanTailscaleConfig(c model.TailscaleConfig) model.TailscaleConfig {
	out, _ := CheckTailscaleConfig(c)
	return out
}

// CheckTailscaleConfig normalises the user's Tailscale lists and returns
// one message per invalid entry (the entry is left out of the result).
func CheckTailscaleConfig(c model.TailscaleConfig) (model.TailscaleConfig, []string) {
	var out model.TailscaleConfig
	var errs []string
	fill := func(field string, in []string, dst *[]string, norm func(string) (string, bool)) {
		for _, v := range in {
			if strings.TrimSpace(v) == "" {
				continue
			}
			n, ok := norm(v)
			if !ok {
				errs = append(errs, fmt.Sprintf("Tailscale.%s: %q", field, v))
				continue
			}
			if !slices.Contains(*dst, n) {
				*dst = append(*dst, n)
			}
		}
	}
	fill("Domains", c.Domains, &out.Domains, NormDomain)
	fill("Routes", c.Routes, &out.Routes, normTailscaleCIDR)
	fill("DNS", c.DNS, &out.DNS, normTailscaleDNS)
	return out, errs
}

// normTailscaleCIDR is NormCIDR without the default routes: keeping all
// traffic out of the TUN is never what a subnet route means.
func normTailscaleCIDR(s string) (string, bool) {
	v, ok := NormCIDR(s)
	if !ok {
		return "", false
	}
	if p, err := netip.ParsePrefix(v); err != nil || p.Bits() == 0 {
		return "", false
	}
	return v, true
}

// normTailscaleDNS accepts IPv4 resolvers (Mihomo's nameserver syntax
// needs brackets around IPv6 ones).
func normTailscaleDNS(s string) (string, bool) {
	v, ok := NormIP(s)
	if !ok {
		return "", false
	}
	if a, err := netip.ParseAddr(v); err != nil || !a.Is4() {
		return "", false
	}
	return v, true
}

// TailscaleReport implements API.
func (s *Service) TailscaleReport() model.TailscaleReport {
	s.mu.Lock()
	set := s.state.settings
	st := s.state.st.Tailscale
	info := s.state.tailscale
	s.mu.Unlock()
	return tailscaleReport(set, st, info.Resolvers, runtime.GOOS)
}

// tailscaleReport words the report and its notes.
func tailscaleReport(set model.Settings, st model.TailscaleState, resolvers []tailscale.Resolver, goos string) model.TailscaleReport {
	r := model.TailscaleReport{Compat: set.TailscaleCompat, Config: set.Tailscale.Clone(), State: st}
	note := func(s string) { r.Notes = append(r.Notes, s) }
	switch {
	case !set.TailscaleCompat:
		note(i18n.T("Tailscale 兼容已关闭：tailnet 地址和 MagicDNS 域名按订阅规则处理，开启 TUN 时通常无法访问。"))
	case !st.Running:
		note(i18n.T("未检测到 Tailscale。Tailscale 连上后，澜流会在 10 秒内自动应用兼容配置。"))
	default:
		if st.ExitNode {
			note(i18n.T("Tailscale 正在使用出口节点：它和澜流的 TUN 不能同时开启。可关闭出口节点，或只用澜流的系统代理。"))
		}
		if st.Suffix == "" && len(resolvers) == 0 {
			note(i18n.T("未能确定 tailnet 的 MagicDNS 后缀（未找到 tailscale 命令行，也没有读到它的解析器）。经系统代理访问 tailnet 域名时，请把后缀（如 tail1234.ts.net）加入「Tailscale 兼容 › 配置…」的域名（设置项 Tailscale.Domains）。"))
		}
		if goos == "windows" {
			note(i18n.T("Windows 上不读取 Tailscale 的子网路由和拆分 DNS：需要时请在「Tailscale 兼容 › 配置…」中填写（设置项 Tailscale.Routes、Tailscale.Domains）。"))
		}
	}
	return r
}
