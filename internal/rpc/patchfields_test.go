package rpc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"lanflow/internal/model"
)

// plainSettings are the settings an agent allowed to write may patch
// without a further question. Every other one is in patchHigh or
// patchLocked. A setting added to model.Settings fails the test until it
// is put in one of the three.
var plainSettings = strings.Fields(`
	OpenAtLogin SilentStart CloseToTray Appearance Language Bypass
	ATrustCompat ATrust.Always ATrust.CorpDomains ATrust.DirectDomains ATrust.CorpDNS ATrust.ExcludeCIDRs ATrust.DirectCIDRs
	ProbeExits Mode HideTraySpeed TraySpeed TrayGraph TrayNode TrayMode GlobalShortcut DisableUpdateCheck
	LogLevel CoreAutoCheck MainGroup DelayURL DelayTimeoutMs DisconnectOnSwitch
	Sniffer.Enable Sniffer.TakeOver Sniffer.HTTP Sniffer.TLS Sniffer.QUIC Sniffer.HTTPPorts Sniffer.TLSPorts Sniffer.QUICPorts
	Sniffer.OverrideDestination Sniffer.ParsePureIP Sniffer.ForceDomain Sniffer.SkipDomain Sniffer.SkipDstAddress
	Geo.Mode Geo.AutoUpdate Geo.IntervalHours Geo.UpdatedAt Sites Shortcuts
	Advanced.IPv6 Advanced.UnifiedDelay Advanced.TCPConcurrent Advanced.FindProcessMode Advanced.KeepAliveInterval
	Advanced.KeepAliveIdle Advanced.DisableKeepAlive Advanced.GeodataLoader Advanced.DisableETag Advanced.StoreFakeIP
	Advanced.GlobalUA Advanced.SnifferNoForceDNSMapping`)

func TestPatchFieldClasses(t *testing.T) {
	plain := map[string]bool{}
	for _, p := range plainSettings {
		plain[p] = true
	}
	used := map[string]bool{}
	for _, p := range model.SettingsPaths() {
		locked, high := pathIn(p, patchLocked), pathIn(p, patchHigh)
		for _, pat := range append(append([]string{}, patchLocked...), patchHigh...) {
			if pathIn(p, []string{pat}) {
				used[pat] = true
			}
		}
		switch {
		case locked && high:
			t.Errorf("%s is both locked and high-impact", p)
		case (locked || high) && plain[p]:
			t.Errorf("%s is listed as a plain write and as locked or high-impact", p)
		case !locked && !high && !plain[p]:
			t.Errorf("%s is in no class: add it to patchHigh, patchLocked or plainSettings", p)
		}
		delete(plain, p)
	}
	for p := range plain {
		t.Errorf("plainSettings names %s, which is no setting", p)
	}
	for _, pat := range append(append([]string{}, patchLocked...), patchHigh...) {
		if !used[pat] {
			t.Errorf("%s matches no setting (renamed?)", pat)
		}
	}
}

// TestPatchesHigh: the high-impact settings are found wherever they are
// in the patch and however their keys are written.
func TestPatchesHigh(t *testing.T) {
	for patch, want := range map[string]bool{
		`{"TUN":true}`:                                      true,
		`{"tun":false,"TrayNode":true}`:                     true,
		`{"TUNStack":"gvisor"}`:                             true,
		`{"Advanced":{"tunDevice":"utun9"}}`:                true,
		`{"advanced":{"TUNDevice":"utun9"}}`:                true,
		`{"Advanced":{"interfaceName":"en0"}}`:              true,
		`{"AllowLAN":true}`:                                 true,
		`{"Advanced":{"authentication":[]}}`:                true,
		`{"Advanced":{"lanAllowedIps":["0.0.0.0/0"]}}`:      true,
		`{"Advanced":{"skipAuthPrefixes":[]}}`:              true,
		`{"MixedPort":7891}`:                                true,
		`{"Advanced":{"httpPort":8080}}`:                    true,
		`{"Advanced":{"SocksPort":1080}}`:                   true,
		`{"Advanced":{"pacMode":true}}`:                     true,
		`{"DNS":{"TakeOver":true}}`:                         true,
		`{"dns":{"nameserver":["1.1.1.1"]}}`:                true,
		`{"Advanced":{"dnsFallbackFilter":{"geoip":true}}}`: true,
		`{"Advanced":{"dnsListen":"0.0.0.0:53"}}`:           true,
		`{"SSIDDefaultSub":"x"}`:                            true,
		`{"Geo":{"GeoIP":"https://example.org/x"}}`:         true,
		`{"AutoSystemProxy":true}`:                          true,
		`{"Zzz":1,"TUN":true}`:                              true, // refused later, asked for all the same

		`{"Mode":"global"}`:                    false,
		`{"LogLevel":"debug","TrayNode":true}`: false,
		`{"Advanced":{"ipv6":true}}`:           false,
		`{"Advanced":{"tcpConcurrent":true}}`:  false,
		`{"Sniffer":{"Enable":false}}`:         false,
		`{"Geo":{"AutoUpdate":true}}`:          false,
		`{"Bypass":["localhost"]}`:             false,
		`[]`:                                   false,
	} {
		if got := patchesHigh(json.RawMessage(patch)); got != want {
			t.Errorf("patchesHigh(%s) = %v, want %v", patch, got, want)
		}
	}
}

// TestApplyPatch: the patch is applied to a copy (the settings it was
// given stay as they were, lists included), under either spelling of a
// key, lists are replaced whole, and masked values stand for the stored
// ones.
func TestApplyPatch(t *testing.T) {
	cur := model.Settings{MixedPort: 7890, Bypass: []string{"a", "b"}, LogLevel: "info"}
	cur.Advanced.HTTPPort = 8080
	cur.Advanced.Authentication = []string{"u:secret", "v:other"}
	cur.DNS.Nameserver = []string{"1.1.1.1"}
	cur.DNS.Policy = []model.KV{{Key: "k", Value: "v"}}
	cur.Sync = model.ConfigSyncSettings{Mode: "folder", Dir: "/x"}
	cur.WebDAV.URL = "https://dav.example.com/dav"
	before := model.CloneSettings(cur)

	apply := func(patch string, agent bool) (model.Settings, []string) {
		t.Helper()
		next, changed, err := applyPatch(cur, json.RawMessage(patch), agent)
		if err != nil {
			t.Fatalf("%s: %v", patch, err)
		}
		if !reflect.DeepEqual(cur, before) {
			t.Fatalf("%s: applying the patch changed the settings it was given: %+v", patch, cur)
		}
		return next, changed
	}
	refused := func(patch string, agent bool, why string) {
		t.Helper()
		if _, _, err := applyPatch(cur, json.RawMessage(patch), agent); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: err = %v, want one about %q", patch, err, why)
		}
		if !reflect.DeepEqual(cur, before) {
			t.Fatalf("%s: a refused patch changed the settings it was given: %+v", patch, cur)
		}
	}

	// Either spelling of a key, at both levels, over a value that is set.
	for _, patch := range []string{`{"Advanced":{"HTTPPort":9090}}`, `{"Advanced":{"httpPort":9090}}`, `{"advanced":{"HTTPPORT":9090}}`} {
		if next, changed := apply(patch, true); next.Advanced.HTTPPort != 9090 || !reflect.DeepEqual(changed, []string{"Advanced"}) {
			t.Errorf("%s: HTTPPort %d, changed %v", patch, next.Advanced.HTTPPort, changed)
		}
	}
	if next, _ := apply(`{"Sync":{"dir":"/y"}}`, false); next.Sync.Dir != "/y" || next.Sync.Mode != "folder" {
		t.Errorf("Sync = %+v, want the folder changed and the mode kept", next.Sync)
	}
	if next, _ := apply(`{"webdav":{"URL":"https://other.example/dav"}}`, false); next.WebDAV.URL != "https://other.example/dav" {
		t.Errorf("WebDAV.URL = %q", next.WebDAV.URL)
	}
	refused(`{"Advanced":{"HTTPPort":1,"httpPort":2}}`, true, "两次")
	refused(`{"Advanced":{"nope":1}}`, true, "没有设置项 Advanced.nope")
	refused(`{"Advanced":7}`, true, "应为 JSON 对象")
	refused(`{"MixedPort":"x"}`, true, "MixedPort 的值无效")
	refused(`[1]`, true, "补丁应为 JSON 对象")

	// Lists are replaced, also by one of the same length; the other
	// options of the section stay.
	if next, changed := apply(`{"Bypass":["p","q"]}`, true); !reflect.DeepEqual(next.Bypass, []string{"p", "q"}) || !reflect.DeepEqual(changed, []string{"Bypass"}) {
		t.Errorf("Bypass = %v, changed %v", next.Bypass, changed)
	}
	if next, _ := apply(`{"Bypass":["x"]}`, true); !reflect.DeepEqual(next.Bypass, []string{"x"}) {
		t.Errorf("Bypass = %v", next.Bypass)
	}
	if next, changed := apply(`{"DNS":{"nameserver":["8.8.8.8"],"Policy":[{"Key":"n","Value":"w"}]}}`, true); !reflect.DeepEqual(next.DNS.Nameserver, []string{"8.8.8.8"}) ||
		!reflect.DeepEqual(next.DNS.Policy, []model.KV{{Key: "n", Value: "w"}}) || !reflect.DeepEqual(changed, []string{"DNS"}) {
		t.Errorf("DNS = %+v, changed %v", next.DNS, changed)
	}
	if _, changed := apply(`{"Bypass":["a","b"],"LogLevel":"info"}`, true); len(changed) != 0 {
		t.Errorf("a patch of the current values changed %v", changed)
	}

	// Masked entries read back are the stored ones; a mask of nothing is
	// refused instead of saved as a password.
	next, _ := apply(`{"Advanced":{"authentication":["u:***","n:pw"]}}`, true)
	if !reflect.DeepEqual(next.Advanced.Authentication, []string{"u:secret", "n:pw"}) {
		t.Errorf("Authentication = %v", next.Advanced.Authentication)
	}
	next, _ = apply(`{"Advanced":{"authentication":["v:***","u:***"]}}`, true)
	if !reflect.DeepEqual(next.Advanced.Authentication, []string{"v:other", "u:secret"}) {
		t.Errorf("Authentication = %v", next.Advanced.Authentication)
	}
	refused(`{"Advanced":{"authentication":["u:***","z:***"]}}`, true, "占位符")

	// Locked for agents, wherever in the patch; not for the window's use.
	for _, patch := range []string{
		`{"CorePath":"/tmp/x"}`, `{"coreversion":"v1"}`, `{"WebDAV":{"url":"https://x"}}`, `{"UI":{}}`, `{"AgentGrants":[]}`,
		`{"Feedback":{"Endpoint":"https://x"}}`, `{"feedback":{"autoReport":true}}`, `{"DownloadMirror":"https://x/"}`,
		`{"CoreChannel":"alpha"}`, `{"CoreAutoInstall":true}`, `{"Sync":{"mode":"folder","dir":"/x"}}`, `{"Mode":"global","sync":{"dir":"/z"}}`,
	} {
		refused(patch, true, "不能经 agent 修改")
	}
	if next, _ := apply(`{"DownloadMirror":"https://x/"}`, false); next.DownloadMirror != "https://x/" {
		t.Errorf("DownloadMirror = %q", next.DownloadMirror)
	}
}
