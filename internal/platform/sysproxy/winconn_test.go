package sysproxy

import (
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// fakeWin is an in-memory winSystem: per-connection options keyed by name
// ("" = the LAN), the RAS phone book, and a log of every write.
type fakeWin struct {
	conns   map[string]winConn
	ras     []string
	rasErr  error
	failGet map[string]bool
	failSet map[string]bool
	// legacy is what legacySnapshot reports; legacyErr fails every
	// legacy write.
	legacy    windowsSaved
	legacyErr error
	notifyErr error
	calls     []string
}

func newFakeWin(ras ...string) *fakeWin {
	f := &fakeWin{conns: map[string]winConn{}, ras: ras, failGet: map[string]bool{}, failSet: map[string]bool{}}
	f.conns[winLAN] = winConn{Flags: winProxyTypeDirect}
	for _, n := range ras {
		f.conns[n] = winConn{Name: n, Flags: winProxyTypeDirect}
	}
	return f
}

func (f *fakeWin) rasEntries() ([]string, error) { return f.ras, f.rasErr }

func (f *fakeWin) queryConn(name string) (winConn, error) {
	if f.failGet[name] {
		return winConn{}, errors.New("query failed")
	}
	c := f.conns[name]
	c.Name = name
	return c, nil
}

func (f *fakeWin) setConn(c winConn, fields winField) error {
	if f.failSet[c.Name] {
		return errors.New("set failed")
	}
	cur := f.conns[c.Name]
	cur.Name = c.Name
	if fields&winFieldFlags != 0 {
		cur.Flags = c.Flags
	}
	if fields&winFieldServer != 0 {
		cur.Server = c.Server
	}
	if fields&winFieldBypass != 0 {
		cur.Bypass = c.Bypass
	}
	if fields&winFieldAutoConfig != 0 {
		cur.AutoConfig = c.AutoConfig
	}
	f.conns[c.Name] = cur
	f.calls = append(f.calls, "set:"+c.Name)
	return nil
}

func (f *fakeWin) notify() error {
	f.calls = append(f.calls, "notify")
	return f.notifyErr
}

func (f *fakeWin) legacyCall(what string) error {
	if f.legacyErr != nil {
		return f.legacyErr
	}
	f.calls = append(f.calls, what)
	return nil
}

func (f *fakeWin) legacySet(server, override string) error {
	return f.legacyCall("legacySet:" + server + "|" + override)
}
func (f *fakeWin) legacyClear() error { return f.legacyCall("legacyClear") }
func (f *fakeWin) legacySetPAC(url string) error {
	return f.legacyCall("legacySetPAC:" + url)
}
func (f *fakeWin) legacySnapshot() (windowsSaved, error) {
	if f.legacyErr != nil {
		return windowsSaved{}, f.legacyErr
	}
	s := f.legacy
	s.OS = "windows"
	return s, nil
}
func (f *fakeWin) legacyRestore(saved windowsSaved) error {
	server := "<nil>"
	if saved.Server != nil {
		server = *saved.Server
	}
	return f.legacyCall("legacyRestore:" + server)
}

const (
	testServer   = "http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7890"
	testOverride = "localhost;<local>"
)

func wantCalls(t *testing.T, f *fakeWin, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", f.calls, want)
	}
}

// Without RAS entries only the LAN is written, through WinINet, followed by
// one notification: what the registry write used to do.
func TestWinSetLANOnly(t *testing.T) {
	f := newFakeWin()
	if err := winSet(f, "127.0.0.1", 7890, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "set:", "notify")
	want := winConn{Flags: winProxyTypeDirect | winProxyTypeProxy, Server: testServer, Bypass: testOverride}
	if got := f.conns[winLAN]; got != want {
		t.Fatalf("LAN = %+v, want %+v", got, want)
	}
}

// Every dial-up / VPN entry gets the proxy too, and a connection's PAC
// setting survives (Set never touched AutoConfigURL).
func TestWinSetCoversRASEntries(t *testing.T) {
	f := newFakeWin("宽带连接", "Work VPN", "work vpn")
	f.conns["Work VPN"] = winConn{Flags: winProxyTypeDirect | winProxyTypeAutoURL, AutoConfig: "http://pac.corp/x.pac"}
	if err := winSet(f, "127.0.0.1", 7890, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	// The duplicate entry (same name in both phone books) is written once.
	wantCalls(t, f, "set:", "set:宽带连接", "set:Work VPN", "notify")
	for _, n := range []string{winLAN, "宽带连接", "Work VPN"} {
		c := f.conns[n]
		if c.Flags&winProxyTypeProxy == 0 || c.Server != testServer || c.Bypass != testOverride {
			t.Errorf("%q not set: %+v", n, c)
		}
	}
	if c := f.conns["Work VPN"]; c.Flags&winProxyTypeAutoURL == 0 || c.AutoConfig != "http://pac.corp/x.pac" {
		t.Errorf("PAC of Work VPN was lost: %+v", c)
	}
}

func TestWinSetRejectsBadAddress(t *testing.T) {
	for _, tc := range []struct {
		host string
		port int
	}{{"", 7890}, {"127.0.0.1", 0}, {"127.0.0.1", 70000}} {
		f := newFakeWin("PPPoE")
		if err := winSet(f, tc.host, tc.port, nil); err == nil {
			t.Errorf("winSet(%q, %d) succeeded", tc.host, tc.port)
		}
		wantCalls(t, f)
	}
}

// RAS trouble never fails the call or the LAN: the enumeration failing, an
// entry that cannot be read, an entry that cannot be written.
func TestWinRASFailuresDoNotFailLAN(t *testing.T) {
	ops := map[string]func(*fakeWin) error{
		"set":    func(f *fakeWin) error { return winSet(f, "127.0.0.1", 7890, []string{"localhost"}) },
		"clear":  func(f *fakeWin) error { return winClear(f) },
		"setPAC": func(f *fakeWin) error { return winSetPAC(f, "http://127.0.0.1:11233/proxy.pac") },
	}
	for name, op := range ops {
		t.Run(name+"/enumeration fails", func(t *testing.T) {
			f := newFakeWin("PPPoE")
			f.rasErr = errors.New("rasapi32 missing")
			if err := op(f); err != nil {
				t.Fatal(err)
			}
			wantCalls(t, f, "set:", "notify")
		})
		t.Run(name+"/one entry fails", func(t *testing.T) {
			f := newFakeWin("A", "B", "C")
			f.failGet["A"] = true
			f.failSet["B"] = true
			if err := op(f); err != nil {
				t.Fatal(err)
			}
			wantCalls(t, f, "set:", "set:C", "notify")
		})
	}
}

// When WinINet refuses the LAN (reading or writing), the registry values
// are written instead and RAS entries are still handled; only both failing
// is an error, and then nothing is notified.
func TestWinLANFallsBackToRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   func(*fakeWin) error
		want string
	}{
		{"set", func(f *fakeWin) error { return winSet(f, "127.0.0.1", 7890, []string{"localhost"}) }, "legacySet:" + testServer + "|" + testOverride},
		{"clear", func(f *fakeWin) error { return winClear(f) }, "legacyClear"},
		{"setPAC", func(f *fakeWin) error { return winSetPAC(f, "http://p/x.pac") }, "legacySetPAC:http://p/x.pac"},
		{"setPAC off", func(f *fakeWin) error { return winSetPAC(f, "") }, "legacySetPAC:"},
	} {
		for _, how := range []string{"query", "set"} {
			t.Run(tc.name+"/"+how, func(t *testing.T) {
				f := newFakeWin("PPPoE")
				if how == "query" {
					f.failGet[winLAN] = true
				} else {
					f.failSet[winLAN] = true
				}
				if err := tc.op(f); err != nil {
					t.Fatal(err)
				}
				wantCalls(t, f, tc.want, "set:PPPoE", "notify")

				f = newFakeWin("PPPoE")
				f.failSet[winLAN] = true
				f.legacyErr = errors.New("写入 ProxyServer 失败")
				if err := tc.op(f); !errors.Is(err, f.legacyErr) {
					t.Fatalf("err = %v, want the registry error", err)
				}
				wantCalls(t, f)
			})
		}
	}
}

func TestWinNotifyErrorIsReported(t *testing.T) {
	f := newFakeWin()
	f.notifyErr = errors.New("refresh failed")
	if err := winClear(f); !errors.Is(err, f.notifyErr) {
		t.Fatalf("err = %v", err)
	}
}

// Clear turns the manual proxy off everywhere and keeps server, bypass and
// PAC as they are.
func TestWinClear(t *testing.T) {
	f := newFakeWin("PPPoE")
	on := winConn{Flags: winProxyTypeDirect | winProxyTypeProxy | winProxyTypeAutoURL, Server: testServer, Bypass: testOverride, AutoConfig: "http://p/x.pac"}
	f.conns[winLAN], f.conns["PPPoE"] = on, on
	if err := winClear(f); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "set:", "set:PPPoE", "notify")
	want := on
	want.Flags = winProxyTypeDirect | winProxyTypeAutoURL
	for _, n := range []string{winLAN, "PPPoE"} {
		got := f.conns[n]
		got.Name = ""
		if got != want {
			t.Errorf("%q = %+v, want %+v", n, got, want)
		}
	}
}

// SetPAC turns PAC on with the address, or off forgetting it, on every
// connection, and leaves the manual proxy alone.
func TestWinSetPAC(t *testing.T) {
	f := newFakeWin("PPPoE")
	manual := winConn{Flags: winProxyTypeDirect | winProxyTypeProxy, Server: "corp:8080", Bypass: "<local>"}
	f.conns[winLAN] = manual
	if err := winSetPAC(f, "http://127.0.0.1:11233/proxy.pac"); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "set:", "set:PPPoE", "notify")
	wantLAN := manual
	wantLAN.Flags |= winProxyTypeAutoURL
	wantLAN.AutoConfig = "http://127.0.0.1:11233/proxy.pac"
	if got := f.conns[winLAN]; got != wantLAN {
		t.Fatalf("LAN = %+v, want %+v", got, wantLAN)
	}
	if got := f.conns["PPPoE"]; got.Flags != winProxyTypeDirect|winProxyTypeAutoURL || got.AutoConfig == "" {
		t.Fatalf("PPPoE = %+v", got)
	}

	if err := winSetPAC(f, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.conns[winLAN]; got != manual {
		t.Fatalf("after PAC off LAN = %+v, want %+v", got, manual)
	}
	if got := f.conns["PPPoE"]; got.Flags != winProxyTypeDirect || got.AutoConfig != "" {
		t.Fatalf("after PAC off PPPoE = %+v", got)
	}
}

// Another client's proxy on the LAN and on a VPN entry comes back exactly;
// an entry created since the snapshot (which Set covered) is switched off;
// an entry deleted since is not written.
func TestWinSnapshotRestoreRoundTrip(t *testing.T) {
	f := newFakeWin("PPPoE", "Work VPN", "Old")
	other := "127.0.0.1:7897"
	one := uint64(1)
	f.legacy = windowsSaved{Enable: &one, Server: &other, HasAutoConfig: true}
	before := map[string]winConn{
		winLAN:     {Flags: winProxyTypeDirect | winProxyTypeProxy, Server: other, Bypass: "<local>"},
		"PPPoE":    {Name: "PPPoE", Flags: winProxyTypeDirect},
		"Work VPN": {Name: "Work VPN", Flags: winProxyTypeDirect | winProxyTypeAutoURL, AutoConfig: "http://pac.corp/x.pac"},
		"Old":      {Name: "Old", Flags: winProxyTypeDirect},
	}
	for n, c := range before {
		f.conns[n] = c
	}
	saved, err := winSnapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f) // a snapshot writes nothing

	f.ras = []string{"PPPoE", "Work VPN", "New"}
	delete(f.conns, "Old")
	f.conns["New"] = winConn{Name: "New", Flags: winProxyTypeDirect}
	if err := winSet(f, "127.0.0.1", 7890, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	if err := winSetPAC(f, "http://127.0.0.1:11233/proxy.pac"); err != nil {
		t.Fatal(err)
	}
	f.calls = nil

	if err := winRestore(f, saved); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "set:", "set:PPPoE", "set:Work VPN", "set:New", "notify")
	for _, n := range []string{winLAN, "PPPoE", "Work VPN"} {
		if got := f.conns[n]; got != before[n] {
			t.Errorf("%q = %+v, want %+v", n, got, before[n])
		}
	}
	if got := f.conns["New"]; got.Flags&winProxyTypeProxy != 0 {
		t.Errorf("new entry still has the proxy on: %+v", got)
	}
	if _, ok := f.conns["Old"]; ok {
		t.Error("the deleted entry was written")
	}
}

// A snapshot written before per-connection support has only the LAN's
// registry values. The old version never wrote a RAS entry, but this one
// did when it set the proxy again on top of that snapshot: restore puts the
// LAN back from the registry values and takes our proxy off the entries
// that still carry it, leaving an entry's own proxy alone.
func TestWinRestoreOldSnapshotDropsOnlyOurRASProxy(t *testing.T) {
	old := Saved(`{"os":"windows","enable":1,"server":"127.0.0.1:7897","override":"<local>","hasAutoConfig":true}`)
	for _, pac := range []string{"", "http://127.0.0.1:11233/proxy.pac"} {
		f := newFakeWin("PPPoE", "Mine")
		if err := winSet(f, "127.0.0.1", 7890, []string{"localhost"}); err != nil {
			t.Fatal(err)
		}
		if pac != "" {
			if err := winSetPAC(f, pac); err != nil {
				t.Fatal(err)
			}
		}
		// The user pointed this entry somewhere else afterwards.
		mine := winConn{Name: "Mine", Flags: winProxyTypeDirect | winProxyTypeProxy | winProxyTypeAutoURL, Server: "corp:8080", AutoConfig: "http://pac.corp/x.pac"}
		f.conns["Mine"] = mine
		f.calls = nil
		if err := winRestore(f, old); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, f, "legacyRestore:127.0.0.1:7897", "set:PPPoE", "notify")
		if got := f.conns["PPPoE"]; got.Flags != winProxyTypeDirect || got.AutoConfig != "" {
			t.Fatalf("pac=%q: PPPoE still carries our proxy: %+v", pac, got)
		}
		if got := f.conns["Mine"]; got != mine {
			t.Fatalf("pac=%q: Mine was touched: %+v", pac, got)
		}
	}

	// The LAN shows another program's proxy (not a loopback one of ours):
	// an entry with the same server is not taken for ours.
	f := newFakeWin("PPPoE")
	corp := winConn{Flags: winProxyTypeDirect | winProxyTypeProxy, Server: "corp:8080"}
	f.conns[winLAN], f.conns["PPPoE"] = corp, corp
	if err := winRestore(f, old); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "legacyRestore:127.0.0.1:7897", "notify")

	// The oldest format (before PAC support) still parses.
	f = newFakeWin()
	if err := winRestore(f, Saved(`{"os":"windows"}`)); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "legacyRestore:<nil>", "notify")
}

// The new snapshot keeps the registry values under their old keys, so an
// older version can still restore the LAN from it, and so can this one
// when WinINet refuses the LAN.
func TestWinSnapshotKeepsLegacyValues(t *testing.T) {
	f := newFakeWin("PPPoE")
	server := "corp:8080"
	one := uint64(1)
	f.legacy = windowsSaved{Enable: &one, Server: &server, HasAutoConfig: true}
	saved, err := winSnapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(saved, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["os"] != "windows" || raw["enable"] != float64(1) || raw["server"] != "corp:8080" || raw["hasAutoConfig"] != true {
		t.Fatalf("snapshot = %s", saved)
	}
	if conns, _ := raw["conns"].([]any); len(conns) != 2 {
		t.Fatalf("want LAN and PPPoE in conns: %s", saved)
	}

	f.failSet[winLAN] = true
	if err := winRestore(f, saved); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "legacyRestore:corp:8080", "set:PPPoE", "notify")

	f = newFakeWin()
	f.failSet[winLAN] = true
	f.legacyErr = errors.New("恢复 ProxyServer 失败")
	if err := winRestore(f, saved); !errors.Is(err, f.legacyErr) {
		t.Fatalf("err = %v, want the registry error", err)
	}
}

// What cannot be read at snapshot time is left out, not fatal. Restore has
// no recorded state for such an entry, so it only takes our proxy off it:
// the phone book failing at snapshot time but not when the proxy was set,
// one unreadable entry that ended up with our proxy, and one unreadable
// entry on which the user had (and keeps) a proxy of their own.
func TestWinSnapshotPartialFailures(t *testing.T) {
	f := newFakeWin("PPPoE")
	f.rasErr = errors.New("no rasapi32")
	f.failGet[winLAN] = true
	saved, err := winSnapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "conns") {
		t.Fatalf("want a registry-only snapshot, got %s", saved)
	}
	f.rasErr, f.failGet = nil, map[string]bool{}
	if err := winSet(f, "127.0.0.1", 7890, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := winRestore(f, saved); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "legacyRestore:<nil>", "set:PPPoE", "notify")
	if got := f.conns["PPPoE"]; got.Flags != winProxyTypeDirect {
		t.Fatalf("PPPoE still carries our proxy: %+v", got)
	}

	f = newFakeWin("A", "Ours", "Theirs")
	f.conns["A"] = winConn{Name: "A", Flags: winProxyTypeDirect | winProxyTypeProxy, Server: "a:1"}
	theirs := winConn{Name: "Theirs", Flags: winProxyTypeDirect | winProxyTypeProxy, Server: "corp:8080"}
	f.conns["Theirs"] = theirs
	f.failGet["Ours"], f.failGet["Theirs"] = true, true
	if saved, err = winSnapshot(f); err != nil {
		t.Fatal(err)
	}
	// Set reaches the entry that could not be read before; the other one
	// stays unreadable for Set and keeps the user's proxy.
	delete(f.failGet, "Ours")
	if err := winSet(f, "127.0.0.1", 7890, []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	f.failGet = map[string]bool{}
	f.calls = nil
	if err := winRestore(f, saved); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "set:", "set:A", "set:Ours", "notify")
	if got := f.conns["Ours"]; got.Flags != winProxyTypeDirect || got.Server != testServer {
		t.Fatalf("Ours = %+v", got)
	}
	if got := f.conns["Theirs"]; got != theirs {
		t.Fatalf("the user's proxy on the unreadable entry was changed: %+v", got)
	}

	f = newFakeWin()
	f.legacyErr = errors.New("无法打开 Internet 设置注册表项")
	if _, err := winSnapshot(f); !errors.Is(err, f.legacyErr) {
		t.Fatalf("err = %v, want the registry error", err)
	}
}

// Lanflow lets go of a proxy that is no longer its own on the LAN (the
// user switched it off in Windows, another client took it): the LAN is
// left alone, but every RAS entry that still points at Lanflow goes back
// to its snapshot, or has our proxy switched off when the snapshot has
// nothing for it. Entries changed since are left alone.
func TestWinReleaseDialUp(t *testing.T) {
	const pacURL = "http://127.0.0.1:11233/proxy.pac"
	for _, tc := range []struct {
		name string
		pac  string
	}{{"manual", ""}, {"pac", pacURL}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeWin("PPPoE", "Work VPN", "Changed")
			before := winConn{Name: "Work VPN", Flags: winProxyTypeDirect | winProxyTypeProxy, Server: "corp:8080", Bypass: "<local>"}
			f.conns["Work VPN"] = before
			saved, err := winSnapshot(f)
			if err != nil {
				t.Fatal(err)
			}
			f.ras = append(f.ras, "New")
			f.conns["New"] = winConn{Name: "New", Flags: winProxyTypeDirect}
			if tc.pac == "" {
				err = winSet(f, "127.0.0.1", 7890, []string{"localhost"})
			} else {
				err = winSetPAC(f, tc.pac)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Someone else took the LAN and one entry.
			theirs := winConn{Flags: winProxyTypeDirect | winProxyTypeProxy, Server: "127.0.0.1:7897"}
			f.conns[winLAN] = theirs
			changed := theirs
			changed.Name = "Changed"
			f.conns["Changed"] = changed
			f.calls = nil

			if err := winReleaseDialUp(f, saved, "127.0.0.1", 7890, tc.pac); err != nil {
				t.Fatal(err)
			}
			wantCalls(t, f, "set:PPPoE", "set:Work VPN", "set:New", "notify")
			if got := f.conns[winLAN]; got != theirs {
				t.Errorf("the LAN was touched: %+v", got)
			}
			if got := f.conns["Changed"]; got != changed {
				t.Errorf("the changed entry was touched: %+v", got)
			}
			if got := f.conns["Work VPN"]; got != before {
				t.Errorf("Work VPN = %+v, want %+v", got, before)
			}
			for _, n := range []string{"PPPoE", "New"} {
				if got := f.conns[n]; got.Flags != winProxyTypeDirect || got.AutoConfig != "" {
					t.Errorf("%q still points at us: %+v", n, got)
				}
			}

			// Nothing left to do: nothing is written, nobody is notified.
			f.calls = nil
			if err := winReleaseDialUp(f, saved, "127.0.0.1", 7890, tc.pac); err != nil {
				t.Fatal(err)
			}
			wantCalls(t, f)
		})
	}

	// Without a usable snapshot (none, an old one, another system's) our
	// proxy is switched off; failures are swallowed.
	for _, s := range []Saved{nil, Saved(`{"os":"windows","enable":1}`), Saved(`{"os":"darwin"}`)} {
		f := newFakeWin("PPPoE", "Broken")
		if err := winSet(f, "127.0.0.1", 7890, nil); err != nil {
			t.Fatal(err)
		}
		f.failSet["Broken"] = true
		f.calls = nil
		if err := winReleaseDialUp(f, s, "127.0.0.1", 7890, ""); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, f, "set:PPPoE", "notify")
		// Another port is not ours.
		f.calls = nil
		if err := winReleaseDialUp(f, s, "127.0.0.1", 7891, ""); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, f)
	}
	f := newFakeWin("PPPoE")
	f.rasErr = errors.New("no rasapi32")
	if err := winReleaseDialUp(f, nil, "127.0.0.1", 7890, ""); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f)
}

// On systems without per-connection settings the exported call does nothing.
func TestReleaseDialUpIsNoOpOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("would read the real phone book")
	}
	if err := ReleaseDialUp(Saved(`{"os":"darwin"}`), "127.0.0.1", 7890, ""); err != nil {
		t.Fatal(err)
	}
}

func TestWinRestoreRejectsForeignSnapshot(t *testing.T) {
	for _, s := range []Saved{nil, Saved("x"), Saved(`{"os":"darwin","services":[]}`)} {
		f := newFakeWin("PPPoE")
		if err := winRestore(f, s); err == nil {
			t.Errorf("winRestore(%q) succeeded", s)
		}
		wantCalls(t, f)
	}
}
