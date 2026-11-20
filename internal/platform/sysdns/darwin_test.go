package sysdns

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeMac simulates networksetup with per-service DNS state.
type fakeMac struct {
	dns  map[string][]string
	list string
	log  []string
}

func (f *fakeMac) run(_ context.Context, name string, args ...string) (string, error) {
	f.log = append(f.log, name+" "+strings.Join(args, " "))
	if name != networksetup {
		return "", nil
	}
	switch args[0] {
	case "-listallnetworkservices":
		return f.list, nil
	case "-getdnsservers":
		s, ok := f.dns[args[1]]
		if !ok {
			return "", fmt.Errorf("%s is not a recognized network service", args[1])
		}
		if len(s) == 0 {
			return "There aren't any DNS Servers set on " + args[1] + ".\n", nil
		}
		return strings.Join(s, "\n") + "\n", nil
	case "-setdnsservers":
		if args[2] == "Empty" {
			f.dns[args[1]] = nil
		} else {
			f.dns[args[1]] = append([]string(nil), args[2:]...)
		}
		return "", nil
	}
	return "", fmt.Errorf("unexpected %v", args)
}

func newFakeMac() *fakeMac {
	return &fakeMac{
		list: "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB 10/100/1000 LAN\n*Thunderbolt Bridge\n",
		dns:  map[string][]string{"Wi-Fi": nil, "USB 10/100/1000 LAN": {"192.168.1.1", "8.8.8.8"}, "*Thunderbolt Bridge": nil},
	}
}

func TestDarwinSetAndRestore(t *testing.T) {
	ctx := context.Background()
	f := newFakeMac()
	saved, err := darwinSnapshot(ctx, f.run)
	if err != nil {
		t.Fatal(err)
	}
	if err := darwinSet(ctx, f.run, TUNServers, nil); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"Wi-Fi", "USB 10/100/1000 LAN"} {
		if !slices.Equal(f.dns[svc], TUNServers) {
			t.Fatalf("%s = %v while TUN", svc, f.dns[svc])
		}
	}
	if f.dns["*Thunderbolt Bridge"] != nil {
		t.Fatal("disabled service must not be touched")
	}
	if err := darwinRestore(ctx, f.run, saved, TUNServers); err != nil {
		t.Fatal(err)
	}
	if f.dns["Wi-Fi"] != nil {
		t.Fatalf("Wi-Fi = %v, want automatic", f.dns["Wi-Fi"])
	}
	if !slices.Equal(f.dns["USB 10/100/1000 LAN"], []string{"192.168.1.1", "8.8.8.8"}) {
		t.Fatalf("LAN = %v", f.dns["USB 10/100/1000 LAN"])
	}
}

func TestDarwinRestoreLeavesForeignChange(t *testing.T) {
	ctx := context.Background()
	f := newFakeMac()
	saved, _ := darwinSnapshot(ctx, f.run)
	darwinSet(ctx, f.run, TUNServers, nil)
	f.dns["Wi-Fi"] = []string{"9.9.9.9"} // the user changed it meanwhile
	if err := darwinRestore(ctx, f.run, saved, TUNServers); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.dns["Wi-Fi"], []string{"9.9.9.9"}) {
		t.Fatalf("foreign change overwritten: %v", f.dns["Wi-Fi"])
	}
	if !slices.Equal(f.dns["USB 10/100/1000 LAN"], []string{"192.168.1.1", "8.8.8.8"}) {
		t.Fatalf("LAN not restored: %v", f.dns["USB 10/100/1000 LAN"])
	}
}

func TestDarwinRestoreCorrupt(t *testing.T) {
	if err := darwinRestore(context.Background(), newFakeMac().run, Saved("x"), TUNServers); err == nil {
		t.Fatal("corrupt snapshot must error")
	}
}

// A service the snapshot could not read (or one that appeared after it)
// must not be pointed at the TUN servers: Restore only knows the services
// of the snapshot and would leave it there for good.
func TestDarwinSetOnlySnapshotServices(t *testing.T) {
	ctx := context.Background()
	f := newFakeMac()
	f.list = "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB 10/100/1000 LAN\nBroken VPN\n"
	// "Broken VPN" is listed but -getdnsservers fails for it (not in f.dns).
	saved, err := darwinSnapshot(ctx, f.run)
	if err != nil {
		t.Fatal(err)
	}
	only, err := snapshotServices(saved)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(only, []string{"USB 10/100/1000 LAN", "Wi-Fi"}) {
		t.Fatalf("snapshot services = %v", only)
	}
	// A service that shows up between the snapshot and Set.
	f.list += "Late Dongle\n"
	f.dns["Late Dongle"] = []string{"10.0.0.1"}
	f.log = nil
	if err := darwinSet(ctx, f.run, TUNServers, only); err != nil {
		t.Fatal(err)
	}
	for _, line := range f.log {
		if strings.Contains(line, "-setdnsservers Broken VPN") || strings.Contains(line, "-setdnsservers Late Dongle") {
			t.Fatalf("changed a service outside the snapshot: %s", line)
		}
	}
	if _, ok := f.dns["Broken VPN"]; ok {
		t.Fatal("the unreadable service was written")
	}
	if !slices.Equal(f.dns["Late Dongle"], []string{"10.0.0.1"}) {
		t.Fatalf("late service changed: %v", f.dns["Late Dongle"])
	}
	if err := darwinRestore(ctx, f.run, saved, TUNServers); err != nil {
		t.Fatal(err)
	}
	for svc, dns := range f.dns {
		if slices.Equal(dns, TUNServers) {
			t.Fatalf("%s still uses the TUN servers after restore", svc)
		}
	}

	// A snapshot without any readable service changes nothing and says so.
	if err := darwinSet(ctx, f.run, TUNServers, []string{}); err == nil {
		t.Fatal("Set with an empty snapshot must fail")
	}
	if _, err := snapshotServices(Saved("x")); err == nil {
		t.Fatal("corrupt snapshot must error")
	}
}
