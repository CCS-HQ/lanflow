//go:build darwin

package app

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// TestLiveAggregate builds an aggregate subscription with the real core
// (docs/aggregate.md): a stored subscription, a link serving Base64 share
// links and typed links are merged, the profile is checked by the core,
// GEOSITE rules load from the bundled databases, and traffic goes through
// a node that came from a share link. No system proxy, no TUN; everything
// lives in a temp directory. Opt-in, in a VM:
//
//	LANFLOW_LIVE_AGG=1
//	LANFLOW_RES       the app's Resources (mihomo, geo/)
//	LANFLOW_AGG_URL   a link serving Base64 share links incl. a SOCKS5 node
//	                  on 127.0.0.1:17891, with subscription-userinfo
//	LANFLOW_AGG_SOCKSLOG  that SOCKS server's log, to see traffic arrive
func TestLiveAggregate(t *testing.T) {
	if os.Getenv("LANFLOW_LIVE_AGG") != "1" {
		t.Skip("set LANFLOW_LIVE_AGG=1 (in a VM) to run")
	}
	res, link := os.Getenv("LANFLOW_RES"), os.Getenv("LANFLOW_AGG_URL")
	root := t.TempDir()
	local := filepath.Join(root, "local.yaml")
	if err := os.WriteFile(local, []byte("proxies:\n  - {name: local-a, type: direct}\n  - {name: local-b, type: direct}\nproxy-groups:\n  - {name: 节点选择, type: select, proxies: [local-a, local-b, DIRECT]}\nrules:\n  - MATCH,节点选择\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(Options{DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "log"), CacheDir: filepath.Join(root, "cache"), ResourceDir: res})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	port := freePort(t)
	set := s.Settings()
	set.CorePath, set.MixedPort, set.ProbeExits, set.DisableUpdateCheck = filepath.Join(res, "mihomo"), port, false, true
	set.TUN, set.Mode, set.AutoSystemProxy = false, model.ModeRule, false
	if err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	localSub, err := s.ImportSubscriptionFile(local, true)
	if err != nil {
		t.Fatal(err)
	}
	e2eWait(t, 30*time.Second, "core running", func() bool { return s.Status().Core == model.CoreRunning })

	spec := profile.DefaultAggregateSpec()
	spec.Sources = []model.AggregateSource{
		{Kind: model.AggSourceSub, SubID: localSub.ID},
		{Kind: model.AggSourceURL, URL: link, Prefix: "L·"},
		{Kind: model.AggSourceLinks, Links: "trojan://pw@203.0.113.20:443?sni=x.example.com#%E8%87%AA%E5%BB%BA%20Trojan\nnot a link"},
	}
	rec := model.Subscription{Name: "聚合测试", AutoUpdateHours: 12}

	// Preview: nothing stored.
	pv, err := s.PreviewAggregate(rec, spec)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	var names []string
	for _, n := range pv.Nodes {
		names = append(names, n.Name)
	}
	t.Logf("preview nodes: %q; groups %q; %d rules; warnings %q", names, pv.Groups, pv.Rules, pv.Warnings)
	for _, r := range pv.Sources {
		t.Logf("source %s: found %d kept %d skipped %d err %q usage %d/%d", r.ID, r.Found, r.Kept, r.Skipped, r.Error, r.Upload+r.Download, r.Total)
	}
	want := []string{"local-a", "local-b", "L·VM SOCKS 01", "L·香港 02", "L·日本 东京 03", "L·美国 04", "L·新加坡 05", "自建 Trojan"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("nodes = %q, want %q", names, want)
	}
	if r := pv.Sources[1]; r.Found != 6 || r.Kept != 5 || r.Skipped != 1 || r.Total != 100<<30 {
		t.Fatalf("link source = %+v", r)
	}
	if r := pv.Sources[2]; r.Kept != 1 {
		t.Fatalf("typed links = %+v", r)
	}
	if len(s.d.store.List()) != 1 {
		t.Fatal("preview stored a subscription")
	}

	// Save and use: the core validates and loads it.
	agg, err := s.SaveAggregate(rec, spec, true)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if cur, _ := s.d.store.Current(); cur.ID != agg.ID {
		t.Fatal("not switched to the aggregate subscription")
	}
	if agg.Total != 100<<30 || agg.Upload != 1<<30 || agg.Download != 2<<30 || agg.Expire.Unix() != 1798761600 || agg.LastError != "" {
		t.Fatalf("record = %+v", agg)
	}
	mod := func(id string) string { return profile.AggModule(id).Name() }
	sel := mod(profile.AggModSelect)
	e2eWait(t, 30*time.Second, "aggregate groups loaded", func() bool { return groupNow(s, sel) != "" && groupNow(s, mod("github")) != "" })
	for _, g := range []string{mod("auto"), mod("google"), mod("youtube"), mod("final"), mod("telegram"), mod("cn")} {
		if groupNow(s, g) == "" {
			t.Errorf("group %s missing from the running core", g)
		}
	}
	for host, policy := range map[string]string{"www.youtube.com": mod("youtube"), "www.google.com": mod("google"), "www.baidu.com": mod("cn"), "192.168.1.1": mod("private"), "example.org": mod("final")} {
		m, err := s.TestRule(host)
		if err != nil {
			// TestRule watches the connection list for its request,
			// which depends on the network; only a wrong answer fails.
			t.Logf("TestRule %s: %v", host, err)
			continue
		}
		t.Logf("rule %s → %s (%s)", host, m.Policy, m.Rule)
		if m.Policy != policy {
			t.Errorf("%s goes to %s via %s, want %s", host, m.Policy, m.Rule, policy)
		}
	}

	// Traffic: through a direct node, then through the share-link SOCKS node.
	get := func() (int, error) {
		proxy, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
		c := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
		resp, err := c.Get("http://www.gstatic.com/generate_204")
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	for _, node := range []string{"local-a", "L·VM SOCKS 01"} {
		if err := s.SelectProxy(sel, node); err != nil {
			t.Fatal(err)
		}
		before := socksLogLen()
		code, err := get()
		t.Logf("via %s: %d %v", node, code, err)
		if err != nil || code != 204 {
			t.Errorf("request via %s: %d %v", node, code, err)
		}
		if node == "L·VM SOCKS 01" && socksLogLen() <= before {
			t.Errorf("the SOCKS server saw no connection")
		}
	}

	// Update rebuilds; a source that fails is reported, the rest stays.
	spec2, _ := s.AggregateSpec(agg.ID)
	spec2.Sources = append(spec2.Sources, model.AggregateSource{Kind: model.AggSourceURL, URL: strings.Replace(link, "links.txt", "missing.txt", 1)})
	if agg, err = s.SaveAggregate(agg, spec2, false); err != nil {
		t.Fatalf("save with a failing source: %v", err)
	}
	t.Logf("after a failing source: LastError %q", agg.LastError)
	if !strings.Contains(agg.LastError, "1 个来源失败") {
		t.Errorf("LastError = %q", agg.LastError)
	}
	if err := s.UpdateSubscription(agg.ID); err != nil {
		t.Fatalf("update: %v", err)
	}
	raw, _ := s.SubscriptionRaw(agg.ID)
	if !strings.Contains(raw, "L·VM SOCKS 01") || s.Status().Core != model.CoreRunning {
		t.Fatalf("after update: core %v\n%s", s.Status().Core, raw)
	}
}

func socksLogLen() int64 {
	fi, err := os.Stat(os.Getenv("LANFLOW_AGG_SOCKSLOG"))
	if err != nil {
		return 0
	}
	return fi.Size()
}
