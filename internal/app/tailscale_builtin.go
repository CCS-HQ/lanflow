package app

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"lanflow/internal/controller"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/platform/tailscale"
	"lanflow/internal/profile"
)

// 内置 Tailscale (Settings.TailscaleBuiltin): the core joins the tailnet
// itself through Mihomo's tailscale outbound (tsnet, in the bundled core
// built with gvisor), under the same name as the 兼容 outbound
// (profile.TailscaleOutbound), so the rules, the hidden lists and the
// scrubbing written for that one apply unchanged. The tailnet ranges stay
// in the TUN (there is no Tailscale interface to hand them to); rules send
// them, ts.net and the user's domains to the node, and MagicDNS answers
// through the tailscale:// nameserver.
//
// Mihomo starts the node lazily, on the first connection or DNS query that
// reaches it, so after every start and reload Lanflow sends it one probe
// (tailscaleAfterConfig). What the node does is only visible in the core's
// log: its own stream (runTailscaleLogs) reads the login URL and the
// backend state at info level, and for a while after a (re)start at debug
// level, where the node's addresses and account appear.
//
// Limits (docs/tailscale.md): only traffic passing through Lanflow reaches
// the tailnet; the machine is not a node of its own, so others cannot
// reach its services, and Taildrop and Tailscale SSH are not available;
// quitting Lanflow leaves the tailnet. The Tailscale app wins: while it
// runs (detection, tailscale.go) the node is left out of the config.

const (
	tsAuthKeyKey  = "tailscale-auth-key"
	tsAuthKeyFile = "tailscale-auth-key.secret"

	// tsKickURL is what the probe asks for: the node's own service
	// address, so the probe never leaves the tailnet.
	tsKickURL = "http://100.100.100.100/"
	// tsDetailFor is how long after a (re)start the node's log is read at
	// debug level, unless its addresses turn up first.
	tsDetailFor = 3 * time.Minute
)

// tsBuiltinState is the built-in node's bookkeeping that is not status.
type tsBuiltinState struct {
	key secretSlot // the auth key

	mu sync.Mutex
	// relevel ends the node's log stream in progress so the next one is
	// opened at the right level; nil when none runs.
	relevel context.CancelFunc
	wake    chan struct{} // the stream loop waits on it while the node is not configured
	detail  time.Time     // debug level until then
	urlSeen string        // the login URL last notified
}

func (t *tsBuiltinState) wakeCh() chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.wake == nil {
		t.wake = make(chan struct{}, 1)
	}
	return t.wake
}

func (s *Service) tsKeySlot() *secretSlot {
	return s.tsb.key.init(tsAuthKeyKey, tsAuthKeyFile)
}

// tsStateDir is the node's state directory, relative to the core's work
// directory (Mihomo keeps it inside: the user's for a user-run core, the
// helper's for a root-run one, see docs/tailscale.md).
func tsStateDir(gen int) string {
	if gen <= 0 {
		return "tailscale"
	}
	return fmt.Sprintf("tailscale-%d", gen)
}

// osHostname is os.Hostname; tests replace it.
var osHostname = os.Hostname

var tsHostBad = regexp.MustCompile(`[^a-z0-9-]+`)

// tsHostname is the device name asked for: the setting, or this
// computer's name with -lanflow, so it is never taken for the Tailscale
// app's device of the same computer.
func tsHostname(set string) string {
	if h := normTSHostname(set); h != "" {
		return h
	}
	h, _ := osHostname()
	h, _, _ = strings.Cut(strings.ToLower(h), ".")
	h = strings.Trim(tsHostBad.ReplaceAllString(h, "-"), "-")
	if h == "" {
		h = "mac"
	}
	if len(h) > 54 {
		h = h[:54]
	}
	return h + "-lanflow"
}

// normTSHostname makes a device name of s (a DNS label: letters, digits,
// hyphens, at most 63); "" when nothing is left.
func normTSHostname(s string) string {
	s = strings.Trim(tsHostBad.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

// CheckTailscaleBuiltin normalises the built-in node's settings and
// returns one message per invalid field (left at its zero value).
func CheckTailscaleBuiltin(b model.TailscaleBuiltin) (model.TailscaleBuiltin, []string) {
	var errs []string
	out := b
	if strings.TrimSpace(b.Hostname) != "" {
		out.Hostname = normTSHostname(b.Hostname)
		if out.Hostname == "" {
			errs = append(errs, fmt.Sprintf("TailscaleBuiltin.Hostname: %q", b.Hostname))
		}
	} else {
		out.Hostname = ""
	}
	out.ControlURL = strings.TrimSpace(b.ControlURL)
	if out.ControlURL != "" {
		u, err := url.Parse(out.ControlURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Sprintf("TailscaleBuiltin.ControlURL: %q", b.ControlURL))
			out.ControlURL = ""
		} else {
			out.ControlURL = strings.TrimRight(out.ControlURL, "/")
		}
	}
	out.ExitNode = strings.TrimSpace(b.ExitNode)
	if e := out.ExitNode; e != "" && !strings.HasPrefix(e, "auto:") {
		if _, err := netip.ParseAddr(e); err != nil && normTSHostname(e) != strings.ToLower(e) && !validFQDN(e) {
			errs = append(errs, fmt.Sprintf("TailscaleBuiltin.ExitNode: %q", b.ExitNode))
			out.ExitNode = ""
		}
	}
	if out.StateGen < 0 {
		out.StateGen = 0
	}
	return out, errs
}

// validFQDN accepts an exit node named by its MagicDNS name.
func validFQDN(s string) bool {
	_, ok := NormDomain(s)
	return ok
}

// tailscaleBuiltinOn reports whether the built-in node goes into the
// config: it is enabled and the Tailscale app is not running.
func tailscaleBuiltinOn(i tailscale.Info, set model.Settings) bool {
	return set.TailscaleBuiltin.Enabled && !i.Running()
}

// builtinTailscaleInput is the layer of the built-in node, without its
// auth key (buildInput adds it from the keychain).
func builtinTailscaleInput(set model.Settings) profile.Tailscale {
	b, _ := CheckTailscaleBuiltin(set.TailscaleBuiltin)
	cfg := cleanTailscaleConfig(set.Tailscale)
	ts := profile.Tailscale{Node: &profile.TailscaleNode{
		Hostname:     tsHostname(b.Hostname),
		ControlURL:   b.ControlURL,
		StateDir:     tsStateDir(b.StateGen),
		AcceptRoutes: b.AcceptRoutes,
		ExitNode:     b.ExitNode,
	}}
	add := func(dst *[]string, v string) {
		if v != "" && !slices.Contains(*dst, v) {
			*dst = append(*dst, v)
		}
	}
	// every range stays in the TUN: only the core reaches the tailnet
	for _, r := range append([]string{tailscale.CGNAT.String(), tailscale.ULA.String()}, cfg.Routes...) {
		if p, err := netip.ParsePrefix(r); err == nil {
			add(&ts.DirectCIDRs, p.String())
		}
	}
	// ts.net gets a rule here: no Tailscale interface stands behind the
	// system, so a MagicDNS name that reaches the core any other way would
	// go to a remote node (a Funnel name of another tailnet then fails,
	// docs/tailscale.md)
	add(&ts.Domains, tailscaleDNSOnly)
	for _, d := range cfg.Domains {
		add(&ts.Domains, d)
	}
	for _, d := range ts.Domains {
		servers := []string{profile.TailscaleDNS}
		if d != tailscaleDNSOnly && len(cfg.DNS) > 0 {
			servers = cfg.DNS
		}
		ts.DNS = append(ts.DNS, profile.DomainDNS{Domain: d, Servers: servers})
	}
	return ts
}

// tailscaleAuthKey is the auth key for the node's config ("" when none
// or the keychain refused it).
func (s *Service) tailscaleAuthKey() string {
	v, err := s.readSecret(s.tsKeySlot(), false)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// SetTailscaleAuthKey implements API: "" removes the key. A running core
// is reloaded when the node is in its config (a node already logged in
// ignores the key).
func (s *Service) SetTailscaleAuthKey(key string) (err error) {
	defer func() { s.logOp("set tailscale auth key", err) }()
	key = strings.TrimSpace(key)
	if key != "" && (strings.ContainsAny(key, " \t\r\n") || len(key) > 512) {
		return i18n.Error("auth key 格式不正确（不能含空白，通常以 tskey- 开头）。")
	}
	if err := s.writeSecret(s.tsKeySlot(), key); err != nil {
		return err
	}
	s.em.emit(TopicSettings)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	on := s.tailscaleLocked().Node != nil
	s.mu.Unlock()
	if on {
		s.reapplyLayers(i18n.T("内置 Tailscale 设置未能生效"))
	}
	return nil
}

// TailscaleAuthKeySet implements API (cheap: the cache; the first call
// reads the keychain in the background).
func (s *Service) TailscaleAuthKeySet() bool {
	has, _ := s.peekSecret(s.tsKeySlot())
	return has
}

// LogoutTailscale implements API: the node moves to a fresh state
// directory, so it starts without a login (the device stays in the
// tailnet's admin console until removed there). The old directory of a
// user-run core is removed; the helper drops its copy once the user's is
// gone (docs/tailscale.md).
func (s *Service) LogoutTailscale() (err error) {
	defer func() { s.logOp("logout tailscale", err) }()
	s.mu.Lock()
	old := s.state.settings.TailscaleBuiltin.StateGen
	s.mu.Unlock()
	if err := s.saveSettingsWith(func(cur model.Settings) model.Settings {
		cur.TailscaleBuiltin.StateGen = cur.TailscaleBuiltin.StateGen + 1
		return cur
	}, false); err != nil {
		return err
	}
	dir := filepath.Join(s.workDir, tsStateDir(old))
	if err := os.RemoveAll(dir); err != nil {
		s.alog.Warnf("tailscale: remove %s: %v", dir, err)
	}
	s.mu.Lock()
	s.state.st.Tailscale.Builtin.LoginName = ""
	s.state.st.Tailscale.Builtin.Addresses = nil
	s.mu.Unlock()
	s.em.emit(TopicStatus)
	return nil
}

// ---- status ----

// builtinStatus is Status.Tailscale.Builtin after a change of the layer:
// cur carries what the node's log told while the layer stays the same.
func builtinStatus(set model.Settings, i tailscale.Info, ts profile.Tailscale, cur model.TailscaleBuiltinState) model.TailscaleBuiltinState {
	switch {
	case !set.TailscaleBuiltin.Enabled:
		return model.TailscaleBuiltinState{}
	case ts.Node == nil && i.Running():
		return model.TailscaleBuiltinState{State: model.TSStatePausedForApp}
	case ts.Node == nil:
		return model.TailscaleBuiltinState{}
	}
	if cur.Hostname != ts.Node.Hostname || cur.State == model.TSStatePausedForApp {
		cur = model.TailscaleBuiltinState{}
	}
	cur.Hostname = ts.Node.Hostname
	return cur
}

// setTailscaleStatusLocked stores Status.Tailscale for a detection result
// and the layer built from it. Call with mu held.
func (s *Service) setTailscaleStatusLocked(i tailscale.Info, ts profile.Tailscale) {
	prev := s.state.st.Tailscale.Builtin
	st := tailscaleStatus(i, ts)
	st.Builtin = builtinStatus(s.state.settings, i, ts, prev)
	s.state.st.Tailscale = st
}

// tailscaleAfterConfig runs after every start and reload: with the node in
// the running config it is (re)starting, so its status starts over, its
// log is read in detail for a while, and the probe makes the core start
// it.
func (s *Service) tailscaleAfterConfig() {
	s.mu.Lock()
	ts := s.tailscaleLocked()
	on := ts.Node != nil
	s.setTailscaleStatusLocked(s.state.tailscale, ts)
	b := &s.state.st.Tailscale.Builtin
	if on {
		// the node starts over: what its log said before no longer holds
		b.State, b.LoginURL, b.Error, b.Addresses = model.TSStateStarting, "", "", nil
	}
	s.mu.Unlock()
	if !on {
		return
	}
	s.em.emit(TopicStatus)
	t := &s.tsb
	t.mu.Lock()
	t.detail = s.d.now().Add(tsDetailFor)
	relevel := t.relevel
	t.mu.Unlock()
	if relevel != nil {
		relevel()
	}
	select {
	case t.wakeCh() <- struct{}{}:
	default:
	}
	s.goBackground(func() {
		ctx, cancel := s.svcCtx(20 * time.Second)
		defer cancel()
		_, _ = s.d.ctrl.ProxyDelay(ctx, profile.TailscaleOutbound, tsKickURL, 15*time.Second)
	})
}

// runTailscaleLogs reads the node's lines from the core's log while the
// streams run: at debug level until tsnet's detailed lines have told the
// addresses (or tsDetailFor has passed), then at info level.
func (s *Service) runTailscaleLogs(ctx context.Context) {
	t := &s.tsb
	wake := t.wakeCh()
	for ctx.Err() == nil {
		s.mu.Lock()
		on := s.tailscaleLocked().Node != nil
		known := len(s.state.st.Tailscale.Builtin.Addresses) > 0
		s.mu.Unlock()
		if !on {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				continue
			}
		}
		t.mu.Lock()
		until := t.detail
		t.mu.Unlock()
		level := "info"
		if !known && s.d.now().Before(until) {
			level = "debug"
		}
		sctx, cancel := context.WithCancel(ctx)
		stop := func() bool { return false }
		if level == "debug" {
			stop = time.AfterFunc(until.Sub(s.d.now()), cancel).Stop
		}
		t.mu.Lock()
		t.relevel = cancel
		t.mu.Unlock()
		_ = s.d.ctrl.StreamLogs(sctx, level, func(l controller.Log) {
			if s.onTailscaleLog(logLevel(l.Type), l.Payload) && level == "debug" {
				cancel() // the addresses are known: back to info
			}
		})
		stop()
		cancel()
		t.mu.Lock()
		t.relevel = nil
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

var (
	tsLinePrefix = "[Tailscale](" + profile.TailscaleOutbound + ") "
	tsAuthURLRe  = regexp.MustCompile(`go to: (https?://\S+)`)
	tsAuthLoopRe = regexp.MustCompile(`AuthLoop: state is (\w+); done`)
	tsSwitchRe   = regexp.MustCompile(`Switching ipn state \w+ -> (\w+)`)
	tsStartingRe = regexp.MustCompile(`LocalBackend state is (\w+);`)
	tsPeerAPIRe  = regexp.MustCompile(`peerapi: serving on http://(?:\[([0-9a-fA-F:]+)\]|([0-9.]+)):\d+`)
	tsLoginRe    = regexp.MustCompile(`active login: "?([^"\s]+)"?`)
)

// tsParsed is what one log line of the node says.
type tsParsed struct {
	state, url, addr, login, err string
}

// parseTailscaleLog reads one line of the core's log (level in lower
// case); ok is false for lines of anything but the node.
func parseTailscaleLog(level, payload string) (p tsParsed, ok bool) {
	rest, found := strings.CutPrefix(payload, tsLinePrefix)
	if !found {
		return p, false
	}
	if level == "warning" || level == "error" {
		p.err = strings.TrimSpace(rest)
	}
	if m := tsAuthURLRe.FindStringSubmatch(rest); m != nil {
		p.state, p.url = model.TSStateNeedsLogin, m[1]
	}
	for _, re := range []*regexp.Regexp{tsAuthLoopRe, tsSwitchRe, tsStartingRe} {
		if m := re.FindStringSubmatch(rest); m != nil {
			p.state = tsStateOf(m[1])
		}
	}
	if m := tsPeerAPIRe.FindStringSubmatch(rest); m != nil {
		if a, err := netip.ParseAddr(m[1] + m[2]); err == nil && (tailscale.CGNAT.Contains(a) || tailscale.ULA.Contains(a)) {
			p.addr = a.String()
		}
	}
	if m := tsLoginRe.FindStringSubmatch(rest); m != nil {
		p.login = m[1]
	}
	return p, true
}

// tsStateOf maps an ipn.State name to TSState*.
func tsStateOf(s string) string {
	switch s {
	case "NeedsLogin":
		return model.TSStateNeedsLogin
	case "NeedsMachineAuth":
		return model.TSStateMachineAuth
	case "Running":
		return model.TSStateRunning
	case "Stopped":
		return model.TSStateStopped
	case "Starting", "NoState":
		return model.TSStateStarting
	}
	return ""
}

// onTailscaleLog applies one line of the core's log to the node's status;
// it reports whether the node's addresses are known after it.
func (s *Service) onTailscaleLog(level, payload string) (addrsKnown bool) {
	p, ok := parseTailscaleLog(level, payload)
	s.mu.Lock()
	b := &s.state.st.Tailscale.Builtin
	if !ok || s.tailscaleLocked().Node == nil {
		known := len(b.Addresses) > 0
		s.mu.Unlock()
		return known
	}
	before := *b
	before.Addresses = slices.Clone(b.Addresses)
	if p.state != "" {
		b.State = p.state
		if p.state != model.TSStateNeedsLogin {
			b.LoginURL = ""
		}
		if p.state == model.TSStateRunning || p.state == model.TSStateMachineAuth {
			b.Error = ""
		}
	}
	if p.url != "" {
		b.LoginURL = p.url
	}
	if p.addr != "" && !slices.Contains(b.Addresses, p.addr) {
		b.Addresses = append(b.Addresses, p.addr)
		slices.SortFunc(b.Addresses, func(x, y string) int { // IPv4 first
			return strings.Count(x, ":") - strings.Count(y, ":")
		})
	}
	if p.login != "" {
		b.LoginName = p.login
	}
	if p.err != "" {
		b.Error = p.err
	}
	cur := *b
	known := len(b.Addresses) > 0
	s.mu.Unlock()
	if !builtinStateEqual(before, cur) {
		s.em.emit(TopicStatus)
	}
	if cur.State != before.State || cur.LoginURL != before.LoginURL {
		s.alog.Infof("tailscale (built-in): %s %s", cur.State, cur.LoginURL)
	}
	s.notifyTailscaleBuiltin(before, cur)
	return known
}

func builtinStateEqual(a, b model.TailscaleBuiltinState) bool {
	return a.State == b.State && a.LoginURL == b.LoginURL && a.Hostname == b.Hostname &&
		a.LoginName == b.LoginName && a.Error == b.Error && slices.Equal(a.Addresses, b.Addresses)
}

// notifyTailscaleBuiltin tells the user what the node needs from them:
// a login (once per URL) or the admin's approval.
func (s *Service) notifyTailscaleBuiltin(before, cur model.TailscaleBuiltinState) {
	t := &s.tsb
	if cur.State == model.TSStateNeedsLogin && cur.LoginURL != "" {
		t.mu.Lock()
		first := t.urlSeen != cur.LoginURL
		t.urlSeen = cur.LoginURL
		t.mu.Unlock()
		if first {
			s.notify(Notice{Kind: "toast", Title: i18n.T("内置 Tailscale 需要登录"),
				Detail: i18n.T("请在「设置 › TUN › 内置 Tailscale」中点「登录…」，在浏览器里把这台设备加入 tailnet；也可以填写 auth key。")})
		}
	}
	if cur.State == model.TSStateMachineAuth && before.State != model.TSStateMachineAuth {
		s.notify(Notice{Kind: "toast", Title: i18n.T("内置 Tailscale 等待批准"),
			Detail: i18n.T("这台设备已登录，需要 tailnet 管理员在管理后台批准后才能连上。")})
	}
	t.mu.Lock()
	loggedIn := cur.State == model.TSStateRunning && before.State != model.TSStateRunning && t.urlSeen != ""
	if loggedIn {
		t.urlSeen = "" // a login of this run: say it worked
	}
	t.mu.Unlock()
	if loggedIn {
		s.notify(Notice{Kind: "toast", Title: i18n.T("内置 Tailscale 已连上 tailnet"),
			Detail: i18n.T("经过澜流的流量（TUN、系统代理）现在可以访问 tailnet 的地址和 MagicDNS 域名。")})
	}
}

// builtinNotes words what needs attention about the built-in node, for
// TailscaleReport.
func builtinNotes(set model.Settings, st model.TailscaleState, running bool) []string {
	b := set.TailscaleBuiltin
	if !b.Enabled {
		return nil
	}
	var out []string
	note := func(s string) { out = append(out, s) }
	switch st.Builtin.State {
	case model.TSStatePausedForApp:
		note(i18n.T("检测到 Tailscale App 正在运行，内置 Tailscale 已暂停（二者只能用一个）。退出 Tailscale App 后，内置节点会自动启用；或关闭内置 Tailscale，改用「Tailscale 兼容」。"))
	case model.TSStateNeedsLogin:
		if st.Builtin.LoginURL != "" {
			note(i18n.Tf("内置 Tailscale 需要登录：在浏览器打开 %s 把这台设备加入 tailnet，或设置 auth key（只能由用户在设置页填写）。", st.Builtin.LoginURL))
		} else {
			note(i18n.T("内置 Tailscale 需要登录，登录链接稍后出现在设置页。"))
		}
	case model.TSStateMachineAuth:
		note(i18n.T("内置 Tailscale 已登录，等待 tailnet 管理员批准这台设备。"))
	case model.TSStateStarting:
		if !running {
			note(i18n.T("内核未运行：内置 Tailscale 随内核启动。"))
		}
	}
	if st.Builtin.Error != "" {
		note(i18n.Tf("内置 Tailscale 报告了错误：%s", st.Builtin.Error))
	}
	if b.ExitNode != "" {
		note(i18n.T("出口节点只用于自定义规则中指向 LANFLOW-TAILSCALE 的流量；其他流量仍按订阅规则走。"))
	}
	if b.AcceptRoutes && len(set.Tailscale.Routes) == 0 {
		note(i18n.T("已接受子网路由，但没有填写网段：请在「Tailscale 兼容 › 配置…」的子网路由中填写要经 tailnet 访问的网段（设置项 Tailscale.Routes），规则才会把它们交给内置节点。"))
	}
	note(i18n.T("内置模式的局限：只有经过澜流的流量（TUN、系统代理、设置了代理的应用）能访问 tailnet；本机不会以自己的身份成为 tailnet 节点，别的设备无法经 tailnet 访问本机的服务（SSH、文件共享），Taildrop 和 Tailscale SSH 不可用；退出澜流即断开 tailnet。"))
	return out
}
