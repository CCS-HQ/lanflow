package app

import (
	"reflect"
	"sync"
	"testing"

	"lanflow/internal/platform/sysproxy"
)

// Regression tests of giving up a system proxy that is no longer ours: the
// Windows dial-up / VPN entries Lanflow also wrote must be released before
// the marker (and with it the snapshot) is dropped. All on the harness's
// fakes; dialSys only records the call.

type dialRelease struct {
	saved string
	host  string
	port  int
	pac   string
}

type dialSys struct {
	sysProxy
	mu    sync.Mutex
	calls []dialRelease
	// markerAtCall says whether the marker still existed during the call.
	markerAtCall func() bool
	hadMarker    []bool
}

func (d *dialSys) ReleaseDialUp(saved sysproxy.Saved, host string, port int, pac string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, dialRelease{string(saved), host, port, pac})
	if d.markerAtCall != nil {
		d.hadMarker = append(d.hadMarker, d.markerAtCall())
	}
	return nil
}

func (d *dialSys) taken() []dialRelease {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.calls
	d.calls = nil
	return out
}

// withDialSys puts a recording dialSys in front of the harness's system proxy.
func withDialSys(h *harness) *dialSys {
	d := &dialSys{sysProxy: h.s.d.sys, markerAtCall: h.s.markerExists}
	h.s.d.sys = d
	return d
}

// foreignProxy makes the system proxy look changed by another program.
func foreignProxy(h *harness) {
	h.sys.mu.Lock()
	h.sys.state = sysproxy.State{Enabled: true, Host: "127.0.0.1", Port: 7897}
	h.sys.mu.Unlock()
}

// Turning the system proxy off after someone else changed it leaves their
// setting alone but releases the dial-up entries, with the snapshot and
// the address Lanflow had set, while the marker still exists.
func TestReleaseProxyNotOursReleasesDialUp(t *testing.T) {
	h := newHarness(t, nil)
	d := withDialSys(h)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	m, ok := h.s.readMarker()
	if !ok || len(m.Saved) == 0 {
		t.Fatalf("marker = %+v, %v", m, ok)
	}
	foreignProxy(h)
	if err := h.s.SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	want := []dialRelease{{string(m.Saved), "127.0.0.1", m.Port, ""}}
	if got := d.taken(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReleaseDialUp calls = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(d.hadMarker, []bool{true}) {
		t.Fatalf("marker during the call: %v", d.hadMarker)
	}
	if h.s.markerExists() {
		t.Fatal("marker should be gone")
	}
	if st, _ := h.sys.Get(); st.Port != 7897 || h.sys.restores != 0 || h.sys.clears != 0 {
		t.Fatalf("the foreign proxy was touched: %+v restores=%d clears=%d", st, h.sys.restores, h.sys.clears)
	}

	// Still ours: the ordinary restore, no separate dial-up release.
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetSystemProxy(false); err != nil {
		t.Fatal(err)
	}
	if got := d.taken(); len(got) != 0 {
		t.Fatalf("ReleaseDialUp called while the proxy was ours: %+v", got)
	}
}

// The periodic check that finds the proxy changed by another program
// releases the dial-up entries before dropping the marker.
func TestRecheckForeignProxyReleasesDialUp(t *testing.T) {
	h := newHarness(t, nil)
	d := withDialSys(h)
	if err := h.s.SetSystemProxy(true); err != nil {
		t.Fatal(err)
	}
	m, _ := h.s.readMarker()
	foreignProxy(h)
	h.s.recheck()
	want := []dialRelease{{string(m.Saved), "127.0.0.1", m.Port, ""}}
	if got := d.taken(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReleaseDialUp calls = %+v, want %+v", got, want)
	}
	if h.s.markerExists() || h.s.Status().SystemProxy {
		t.Fatal("ownership should be dropped")
	}
}

// A stale marker found at start (the proxy was changed while Lanflow was
// not running) also releases the dial-up entries; a marker that is still
// ours does not.
func TestLeftoverProxyStaleMarkerReleasesDialUp(t *testing.T) {
	h := newHarness(t, nil)
	d := withDialSys(h)
	h.s.writeMarkerPAC(7890, sysproxy.Saved(`{"os":"windows"}`), "http://127.0.0.1:11233/proxy.pac")
	h.sys.mu.Lock()
	h.sys.state = sysproxy.State{PACURL: "http://127.0.0.1:11233/proxy.pac"}
	h.sys.mu.Unlock()
	if _, ours := h.s.leftoverProxy(); !ours {
		t.Fatal("the PAC proxy is ours")
	}
	if got := d.taken(); len(got) != 0 || !h.s.markerExists() {
		t.Fatalf("ours: calls %+v", got)
	}
	foreignProxy(h)
	if _, ours := h.s.leftoverProxy(); ours {
		t.Fatal("the proxy is not ours any more")
	}
	want := []dialRelease{{`{"os":"windows"}`, "127.0.0.1", 7890, "http://127.0.0.1:11233/proxy.pac"}}
	if got := d.taken(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReleaseDialUp calls = %+v, want %+v", got, want)
	}
	if h.s.markerExists() {
		t.Fatal("stale marker should be gone")
	}
}

// The wrapper every real Service puts around the system proxy passes the
// call on, and a system proxy without it is skipped.
func TestNotingSysProxyForwardsReleaseDialUp(t *testing.T) {
	d := &dialSys{sysProxy: &fakeSys{}}
	var noted []string
	var sys sysProxy = notingSysProxy{d, func(kind string) { noted = append(noted, kind) }}
	r, ok := sys.(dialUpReleaser)
	if !ok {
		t.Fatal("notingSysProxy does not implement dialUpReleaser")
	}
	if err := r.ReleaseDialUp(nil, "127.0.0.1", 7890, ""); err != nil {
		t.Fatal(err)
	}
	if len(d.taken()) != 1 || !reflect.DeepEqual(noted, []string{selfProxy}) {
		t.Fatalf("not forwarded: noted %v", noted)
	}
	plain := notingSysProxy{&fakeSys{}, func(string) { t.Fatal("noted without a release") }}
	if err := plain.ReleaseDialUp(nil, "127.0.0.1", 7890, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := sysProxy(realSysProxy{}).(dialUpReleaser); !ok {
		t.Fatal("realSysProxy does not implement dialUpReleaser")
	}
}
