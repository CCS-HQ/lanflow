package sysproxy

import (
	"context"
	"reflect"
	"testing"
)

func TestParseGetProxy(t *testing.T) {
	got := parseGetProxy("Enabled: Yes\nServer: 10.0.0.2\nPort: 3128\nAuthenticated Proxy Enabled: 0\n")
	if got != (darwinProxy{On: true, Server: "10.0.0.2", Port: 3128}) {
		t.Fatalf("got %+v", got)
	}
	if got := parseGetProxy("Enabled: No\nServer: \nPort: 0\n"); got != (darwinProxy{}) {
		t.Fatalf("off: got %+v", got)
	}
}

func TestParseBypassDomains(t *testing.T) {
	if got := parseBypassDomains("There aren't any bypass domains set on Wi-Fi.\n"); got != nil {
		t.Fatalf("empty: %q", got)
	}
	if got := parseBypassDomains("*.local\n169.254/16\n"); !reflect.DeepEqual(got, []string{"*.local", "169.254/16"}) {
		t.Fatalf("got %q", got)
	}
}

// Another app's manual proxy on Wi-Fi must come back exactly, off services
// stay off, and a service added since the snapshot is cleared.
func TestDarwinSnapshotRestoreRoundTrip(t *testing.T) {
	f := newFake()
	f.outputs["/usr/sbin/networksetup -listallnetworkservices"] = "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB LAN\n"
	f.outputs["/usr/sbin/networksetup -getwebproxy Wi-Fi"] = "Enabled: Yes\nServer: 127.0.0.1\nPort: 7897\n"
	f.outputs["/usr/sbin/networksetup -getsecurewebproxy Wi-Fi"] = "Enabled: Yes\nServer: 127.0.0.1\nPort: 7897\n"
	f.outputs["/usr/sbin/networksetup -getsocksfirewallproxy Wi-Fi"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["/usr/sbin/networksetup -getproxybypassdomains Wi-Fi"] = "*.local\nlocalhost\n"
	f.outputs["/usr/sbin/networksetup -getwebproxy USB LAN"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["/usr/sbin/networksetup -getsecurewebproxy USB LAN"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["/usr/sbin/networksetup -getsocksfirewallproxy USB LAN"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["/usr/sbin/networksetup -getproxybypassdomains USB LAN"] = "There aren't any bypass domains set on USB LAN.\n"

	saved, err := darwinSnapshot(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	f.outputs["/usr/sbin/networksetup -listallnetworkservices"] = "Wi-Fi\nUSB LAN\nThunderbolt Bridge\n"
	if err := darwinRestore(context.Background(), f.run, saved); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/sbin/networksetup -listallnetworkservices",
		"/usr/sbin/networksetup -setwebproxy Wi-Fi 127.0.0.1 7897",
		"/usr/sbin/networksetup -setwebproxystate Wi-Fi on",
		"/usr/sbin/networksetup -setsecurewebproxy Wi-Fi 127.0.0.1 7897",
		"/usr/sbin/networksetup -setsecurewebproxystate Wi-Fi on",
		"/usr/sbin/networksetup -setsocksfirewallproxystate Wi-Fi off",
		"/usr/sbin/networksetup -setproxybypassdomains Wi-Fi *.local localhost",
		"/usr/sbin/networksetup -setautoproxystate Wi-Fi off",
		"/usr/sbin/networksetup -setwebproxystate USB LAN off",
		"/usr/sbin/networksetup -setsecurewebproxystate USB LAN off",
		"/usr/sbin/networksetup -setsocksfirewallproxystate USB LAN off",
		"/usr/sbin/networksetup -setproxybypassdomains USB LAN Empty",
		"/usr/sbin/networksetup -setautoproxystate USB LAN off",
		"/usr/sbin/networksetup -setwebproxystate Thunderbolt Bridge off",
		"/usr/sbin/networksetup -setsecurewebproxystate Thunderbolt Bridge off",
		"/usr/sbin/networksetup -setsocksfirewallproxystate Thunderbolt Bridge off",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("restore commands:\n got %q\nwant %q", f.calls, want)
	}
}

func TestDarwinRestoreRejectsForeignSnapshot(t *testing.T) {
	if err := darwinRestore(context.Background(), newFake().run, Saved(`{"os":"windows"}`)); err == nil {
		t.Fatal("expected an error")
	}
}

// Every command goes to the system tool by its absolute path, never to
// whatever "networksetup" or "scutil" is first in PATH.
func TestDarwinCommandsUseAbsolutePaths(t *testing.T) {
	var names []string
	run := func(_ context.Context, name string, args ...string) (string, error) {
		names = append(names, name)
		if len(args) > 0 && args[0] == "-listallnetworkservices" {
			return "Wi-Fi\n", nil
		}
		return "", nil
	}
	ctx := context.Background()
	saved, err := darwinSnapshot(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	_ = darwinSet(ctx, run, "127.0.0.1", 7890, nil)
	_ = darwinSetPAC(ctx, run, "http://127.0.0.1:1/proxy.pac")
	_ = darwinClear(ctx, run)
	_ = darwinRestore(ctx, run, saved)
	_, _ = darwinGet(ctx, run)
	if len(names) < 10 {
		t.Fatalf("only %d commands ran", len(names))
	}
	for _, n := range names {
		if n != "/usr/sbin/networksetup" && n != "/usr/sbin/scutil" {
			t.Fatalf("command %q is not an absolute system path", n)
		}
	}
}

// The snapshot records which proxies had a login, so the user can be told
// that Restore brings back the server but not the credentials.
func TestDarwinSnapshotRecordsAuthenticatedProxy(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want darwinProxy
	}{
		{"Enabled: Yes\nServer: proxy.corp.example\nPort: 8080\nAuthenticated Proxy Enabled: 1\n", darwinProxy{On: true, Server: "proxy.corp.example", Port: 8080, Auth: true}},
		{"Enabled: Yes\nServer: 127.0.0.1\nPort: 7897\nAuthenticated Proxy Enabled: 0\n", darwinProxy{On: true, Server: "127.0.0.1", Port: 7897}},
		{"Enabled: No\nServer: \nPort: 0\n", darwinProxy{}},
	} {
		if got := parseGetProxy(tc.out); got != tc.want {
			t.Errorf("parseGetProxy(%q) = %+v, want %+v", tc.out, got, tc.want)
		}
	}

	f := newFake()
	f.outputs["/usr/sbin/networksetup -listallnetworkservices"] = "Wi-Fi\nUSB LAN\nVPN\n"
	for _, svc := range []string{"Wi-Fi", "USB LAN", "VPN"} {
		for _, get := range []string{"-getwebproxy", "-getsecurewebproxy", "-getsocksfirewallproxy"} {
			f.outputs["/usr/sbin/networksetup "+get+" "+svc] = "Enabled: No\nServer: \nPort: 0\nAuthenticated Proxy Enabled: 0\n"
		}
	}
	f.outputs["/usr/sbin/networksetup -getsecurewebproxy Wi-Fi"] = "Enabled: Yes\nServer: proxy.corp.example\nPort: 8080\nAuthenticated Proxy Enabled: 1\n"
	// The flag without a server means nothing to bring back.
	f.outputs["/usr/sbin/networksetup -getwebproxy VPN"] = "Enabled: No\nServer: \nPort: 0\nAuthenticated Proxy Enabled: 1\n"
	saved, err := darwinSnapshot(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if got := AuthenticatedServices(saved); len(got) != 1 || got[0] != "Wi-Fi" {
		t.Fatalf("AuthenticatedServices = %v, want [Wi-Fi]", got)
	}
	for _, s := range []Saved{nil, Saved("x"), Saved(`{"os":"windows"}`)} {
		if got := AuthenticatedServices(s); got != nil {
			t.Fatalf("AuthenticatedServices(%q) = %v", s, got)
		}
	}
}
