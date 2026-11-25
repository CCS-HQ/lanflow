package app

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/netx"
	"lanflow/internal/platform/netinfo"
)

// TestLiveTUNModes checks the TUN stack choice (mips) and 高级 › DNS 模式
// against the real core through the installed helper. VM only, set up as
// for TestLiveTUN (scripts/vm-tun-test.sh with LANFLOW_TUN_TEST=LiveTUNModes).
//
// For the crash case the test writes <root>/kill-now and waits for
// <root>/killed: the script kills the root core with SIGKILL and saves
// the dynamic store's sing-tun DNS keys right after into
// <root>/scutil-after-kill.txt.
func TestLiveTUNModes(t *testing.T) {
	if os.Getenv("LANFLOW_LIVE_TUN") != "1" {
		t.Skip("set LANFLOW_LIVE_TUN=1 (in a VM) to test TUN for real")
	}
	root, res := os.Getenv("LANFLOW_TUN_DATA"), os.Getenv("LANFLOW_TUN_RES")
	if root == "" || res == "" {
		t.Fatal("LANFLOW_TUN_DATA and LANFLOW_TUN_RES are required")
	}
	if name, ok := netinfo.OtherTUN(context.Background()); ok {
		t.Fatalf("another TUN (%s) is already active; run this on a clean machine", name)
	}
	s, err := NewService(Options{DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "log"), CacheDir: filepath.Join(root, "cache"), ResourceDir: res})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	tunLog := filepath.Join(root, "data", "core", "mihomo-tun.log")
	t.Cleanup(func() {
		if t.Failed() {
			if b, err := os.ReadFile(tunLog); err == nil {
				t.Logf("mihomo-tun.log (tail):\n%s", tail(string(b), 40))
			}
		}
	})

	set := s.Settings()
	set.CorePath, set.MixedPort, set.ProbeExits = filepath.Join(res, "mihomo"), freePort(t), false
	if err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub.yaml")
	os.WriteFile(sub, []byte("proxies:\n  - name: 直连节点\n    type: direct\nproxy-groups:\n  - name: 节点选择\n    type: select\n    proxies: [直连节点]\nrules:\n  - MATCH,节点选择\n"), 0o600)
	if _, err := s.ImportSubscriptionFile(sub, true); err != nil {
		t.Fatal(err)
	}
	e2eWait(t, 20*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })

	baseline := tunResolvers(t)
	t.Logf("TUN resolvers before: %v", baseline)

	type tc struct {
		name, stack, dnsMode string
		setDNS               bool
		wantStack            string // as the core logs it
		wantResolver         bool   // a 198.18.x resolver registered by the core
	}
	for _, c := range []tc{
		{"mips + auto", "mips", "", true, "Mips", false},
		{"mixed + native", "mixed", "native", false, "Mixed", true},
		// Disabled leaves the system DNS alone, so Lanflow's own setting must
		// point it into the TUN (in the VM the gateway's DNS hands out the
		// host's 198.18 fake IPs, which collide with the core's range).
		{"mips + disabled", "mips", "disabled", true, "Mips", false},
		{"mips + hijack", "mips", "hijack", false, "Mips", true},
		{"mips + native + auto system DNS", "mips", "native", true, "Mips", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			set := s.Settings()
			set.TUNStack, set.TUNSetDNS, set.Advanced.TUNDNSMode = c.stack, c.setDNS, c.dnsMode
			if err := s.SaveSettings(set); err != nil {
				t.Fatal(err)
			}
			logBefore := fileSize(tunLog)
			t0 := time.Now()
			if err := s.SetTUN(true); err != nil {
				t.Fatalf("SetTUN(true): %v", err)
			}
			defer func() {
				if err := s.SetTUN(false); err != nil {
					t.Errorf("SetTUN(false): %v", err)
				}
				e2eWait(t, 30*time.Second, "TUN off", func() bool { st := s.Status(); return st.Core == model.CoreRunning && !st.TUN })
				e2eWait(t, 15*time.Second, "routes restored", func() bool { _, ok := netinfo.OtherTUN(context.Background()); return !ok })
				e2eWait(t, 10*time.Second, "core resolver gone", func() bool { return len(tunResolvers(t)) == len(baseline) })
			}()
			e2eWait(t, 30*time.Second, "TUN running", func() bool { st := s.Status(); return st.Core == model.CoreRunning && st.TUN })
			e2eWait(t, 15*time.Second, "TUN routes", func() bool { _, ok := netinfo.OtherTUN(context.Background()); return ok })
			t.Logf("TUN on after %v", time.Since(t0).Round(time.Millisecond))

			s.mu.Lock()
			rt := string(s.state.runtimeYAML)
			s.mu.Unlock()
			if !strings.Contains(rt, "stack: "+c.stack) {
				t.Errorf("runtime config lacks stack: %s", c.stack)
			}
			if c.dnsMode != "" && !strings.Contains(rt, "dns-mode: "+c.dnsMode) {
				t.Errorf("runtime config lacks dns-mode: %s", c.dnsMode)
			}
			var line string
			e2eWait(t, 10*time.Second, "TUN listener log line", func() bool {
				line = lastMatch(tunLog, logBefore, "Tun adapter listening at")
				return line != ""
			})
			t.Logf("core: %s", line)
			if !strings.Contains(line, "ip stack: "+c.wantStack) {
				t.Errorf("core runs another stack, want %s", c.wantStack)
			}
			if c.dnsMode != "" && !strings.Contains(line, "dns mode: "+c.dnsMode) {
				t.Errorf("core reports another dns mode, want %s", c.dnsMode)
			}

			// TCP without any proxy setting is captured.
			host := "captive.apple.com"
			var reqErr error
			for i := 0; i < 20; i++ {
				resp, err := netx.Direct(10 * time.Second).Get("http://" + host + "/hotspot-detect.html")
				if reqErr = err; err == nil {
					resp.Body.Close()
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			if reqErr != nil {
				t.Fatalf("TCP under TUN: %v", reqErr)
			}
			e2eWait(t, 10*time.Second, "TUN TCP connection", func() bool {
				for _, cn := range append(s.Connections(), s.ClosedConnections()...) {
					if strings.HasPrefix(cn.Host, host) && strings.EqualFold(cn.Type, "tun") && !cn.Start.Before(t0) {
						return true
					}
				}
				return false
			})

			// UDP: a DNS query straight to a public resolver is hijacked by
			// the core (dns-hijack any:53) and answered with a fake IP.
			r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "udp", "8.8.8.8:53")
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			ips, err := r.LookupHost(ctx, "example.com")
			cancel()
			if err != nil {
				t.Errorf("UDP DNS under TUN: %v", err)
			} else {
				t.Logf("UDP DNS under TUN: example.com → %v", ips)
				if !strings.HasPrefix(ips[0], "198.18.") {
					t.Errorf("UDP DNS answer %v is not the core's fake IP", ips)
				}
			}
			if out, err := exec.Command("/sbin/ping", "-c", "2", "-t", "4", "1.1.1.1").CombinedOutput(); err != nil {
				t.Logf("ICMP under TUN failed (informational): %v\n%s", err, tail(string(out), 4))
			} else {
				t.Logf("ICMP under TUN: %s", tail(string(out), 1))
			}

			got := tunResolvers(t)
			t.Logf("TUN resolvers: %v", got)
			if has := len(got) > len(baseline); has != c.wantResolver {
				t.Errorf("core-registered resolver present = %v, want %v", has, c.wantResolver)
			}
			// The system resolver must work in every mode.
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			_, err = net.DefaultResolver.LookupHost(ctx, "apple.com")
			cancel()
			if err != nil {
				t.Errorf("system resolver: %v", err)
			}
		})
	}

	// A core killed with SIGKILL must not leave its resolver behind.
	t.Run("native resolver after core SIGKILL", func(t *testing.T) {
		set := s.Settings()
		set.TUNStack, set.TUNSetDNS, set.Advanced.TUNDNSMode = "mips", false, "native"
		if err := s.SaveSettings(set); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTUN(true); err != nil {
			t.Fatalf("SetTUN(true): %v", err)
		}
		e2eWait(t, 30*time.Second, "TUN running", func() bool { st := s.Status(); return st.Core == model.CoreRunning && st.TUN })
		e2eWait(t, 10*time.Second, "core resolver", func() bool { return len(tunResolvers(t)) > len(baseline) })
		before := tunDNSKeys(t)
		t.Logf("sing-tun DNS keys before SIGKILL: %v", before)
		if len(before) == 0 {
			t.Fatal("no sing-tun DNS key registered in native mode")
		}
		os.Remove(filepath.Join(root, "killed"))
		if err := os.WriteFile(filepath.Join(root, "kill-now"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		e2eWait(t, 90*time.Second, "the script to kill the core", func() bool {
			_, err := os.Stat(filepath.Join(root, "killed"))
			return err == nil
		})
		after, _ := os.ReadFile(filepath.Join(root, "scutil-after-kill.txt"))
		left := dnsKeyRE.FindAllString(string(after), -1)
		t.Logf("sing-tun DNS keys right after SIGKILL: %v", left)
		for _, k := range before {
			if strings.Contains(string(after), k) {
				t.Errorf("the killed core's key %s stayed registered", k)
			}
		}
		// A key that outlived the core would also show up later.
		e2eWait(t, 30*time.Second, "core back", func() bool { return s.Status().Core == model.CoreRunning })
		e2eWait(t, 10*time.Second, "new key", func() bool { return len(tunDNSKeys(t)) > 0 })
		later := tunDNSKeys(t)
		t.Logf("sing-tun DNS keys after the restart: %v", later)
		if len(later) != 1 {
			t.Errorf("want exactly one key after the restart, got %v", later)
		}
		// Turn TUN off cleanly afterwards.
		if err := s.SetTUN(false); err != nil {
			t.Errorf("SetTUN(false): %v", err)
		}
		e2eWait(t, 30*time.Second, "TUN off", func() bool { st := s.Status(); return st.Core == model.CoreRunning && !st.TUN })
		e2eWait(t, 10*time.Second, "core resolver gone", func() bool { return len(tunResolvers(t)) == len(baseline) })
		if k := tunDNSKeys(t); len(k) != 0 {
			t.Errorf("sing-tun DNS keys left after TUN off: %v", k)
		}
	})
	if err := s.StopCore(); err != nil {
		t.Fatal(err)
	}
}

var (
	resolverRE = regexp.MustCompile(`nameserver\[\d+\] : 198\.18\.\d+\.\d+`)
	dnsKeyRE   = regexp.MustCompile(`State:/Network/Service/sing-tun-[0-9a-f]+/DNS`)
)

// tunDNSKeys lists the sing-tun DNS keys in the dynamic store.
func tunDNSKeys(t *testing.T) []string {
	cmd := exec.Command("/usr/sbin/scutil")
	cmd.Stdin = strings.NewReader("list State:/Network/Service/sing-tun-.*/DNS\n")
	out, err := cmd.Output()
	if err != nil {
		t.Logf("scutil list: %v", err)
	}
	return dnsKeyRE.FindAllString(string(out), -1)
}

// tunResolvers lists the 198.18.x nameservers in `scutil --dns`.
func tunResolvers(t *testing.T) []string {
	out, err := exec.Command("/usr/sbin/scutil", "--dns").Output()
	if err != nil {
		t.Logf("scutil --dns: %v", err)
	}
	return resolverRE.FindAllString(string(out), -1)
}

func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// lastMatch returns the last line containing sub written to path after offset.
func lastMatch(path string, offset int64, sub string) string {
	b, err := os.ReadFile(path)
	if err != nil || int64(len(b)) < offset {
		return ""
	}
	var found string
	for _, l := range strings.Split(string(b[offset:]), "\n") {
		if strings.Contains(l, sub) {
			found = l
		}
	}
	return found
}
