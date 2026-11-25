package tailscale

import (
	"net/netip"
	"reflect"
	"testing"

	"lanflow/internal/platform/atrust"
)

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func TestPickIface(t *testing.T) {
	none := func(string) bool { return false }
	ifs := []Iface{
		{Name: "en0", Up: true, Addrs: addrs("100.72.1.5", "fe80::1")}, // carrier-grade NAT on Wi-Fi
		{Name: "utun1700", Up: true, Addrs: addrs("198.18.0.1")},       // Lanflow
		{Name: "utun3", Up: false, Addrs: addrs("100.101.2.3")},        // down
		{Name: "utun5", Up: true, Addrs: addrs("100.101.2.3", "fd7a:115c:a1e0::1234")},
	}
	name, as := pickIface(ifs, none)
	if name != "utun5" || !reflect.DeepEqual(as, []string{"100.101.2.3", "fd7a:115c:a1e0::1234"}) {
		t.Fatalf("got %q %v", name, as)
	}
	// IPv4 only under a tunnel name counts; a physical adapter does not
	if name, _ := pickIface([]Iface{{Name: "en0", Up: true, Addrs: addrs("100.72.1.5")}}, none); name != "" {
		t.Fatalf("CGNAT Wi-Fi taken for Tailscale: %q", name)
	}
	if name, _ := pickIface([]Iface{{Name: "Tailscale", Up: true, Addrs: addrs("100.88.0.2")}}, none); name != "Tailscale" {
		t.Fatalf("Windows adapter: %q", name)
	}
	// Lanflow's own interface is never Tailscale's
	own := func(n string) bool { return n == "utun5" }
	if name, _ := pickIface(ifs, own); name != "" {
		t.Fatalf("own interface taken: %q", name)
	}
}

const netstatOut = `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            192.168.1.1        UGScg                 en0
default            link#25            UCSIg               utun5
100.64/10          link#25            UCS                 utun5
100.100.100.100/32 link#25            UCS                 utun5
100.101.2.3        100.101.2.3        UH                  utun5
192.168.50/24      link#25            UCS                 utun5
10.20.0.0/16       link#25            UCS                 utun5
10.20.5/24         link#25            UCS                 utun5
1                  198.18.0.1         UGSc             utun1700
192.168.1          link#6             UCS                   en0
`

func TestParseRoutes(t *testing.T) {
	routes, exit := parseRoutes(netstatOut, "utun5")
	if want := []string{"10.20.0.0/16", "192.168.50.0/24"}; !reflect.DeepEqual(routes, want) || exit {
		t.Fatalf("routes %v exit %v, want %v false", routes, exit, want)
	}
	exitTable := netstatOut + "0/1                link#25            UCS                 utun5\n128.0/1            link#25            UCS                 utun5\n"
	if _, exit := parseRoutes(exitTable, "utun5"); !exit {
		t.Fatal("exit node not seen")
	}
}

func TestResolvers(t *testing.T) {
	rs := []atrust.Resolver{
		{Domain: "", Servers: []string{"192.168.1.1"}, Iface: "en0"},
		{Domain: "tail1234.ts.net", Servers: []string{"100.100.100.100"}},
		{Domain: "corp.example.com.", Servers: []string{"10.20.0.53"}, Iface: "utun5"},
		{Domain: "64.100.in-addr.arpa", Servers: []string{"100.100.100.100"}},
		{Domain: "other.example", Servers: []string{"10.0.0.1"}, Iface: "utun9"}, // another VPN
	}
	got, magic := resolvers(rs, "utun5")
	want := []Resolver{
		{Domain: "corp.example.com", Servers: []string{"10.20.0.53"}},
		{Domain: "tail1234.ts.net", Servers: []string{"100.100.100.100"}},
	}
	if !reflect.DeepEqual(got, want) || !magic {
		t.Fatalf("got %+v magic %v", got, magic)
	}
}

func TestParseStatus(t *testing.T) {
	st, ok := parseStatus([]byte(`{"BackendState":"Running","MagicDNSSuffix":"tail1234.ts.net",
		"CurrentTailnet":{"Name":"x","MagicDNSSuffix":"Tail1234.ts.net.","MagicDNSEnabled":true},
		"ExitNodeStatus":{"ID":"nABC","Online":true}}`))
	if !ok || !st.Running || st.Suffix != "tail1234.ts.net" || !st.MagicDNS || !st.ExitNode {
		t.Fatalf("got %+v %v", st, ok)
	}
	st, ok = parseStatus([]byte(`{"BackendState":"Stopped","ExitNodeStatus":null}`))
	if !ok || st.Running || st.ExitNode {
		t.Fatalf("got %+v", st)
	}
	if _, ok := parseStatus([]byte("not json")); ok {
		t.Fatal("garbage accepted")
	}
}

func TestKey(t *testing.T) {
	if (Info{}).Key() != "" {
		t.Fatal("zero key")
	}
	a := Info{Iface: "utun5", Addrs: []string{"100.1.1.1"}}
	b := a
	b.Addrs = []string{"100.1.1.2"}
	if a.Key() != b.Key() {
		t.Fatal("addresses changed the key")
	}
	b.ExitNode = true
	if a.Key() == b.Key() {
		t.Fatal("exit node did not change the key")
	}
}
