package settings

import (
	"slices"
	"strings"
	"testing"

	"lanflow/internal/model"
)

func TestTailscaleFormParse(t *testing.T) {
	f := tailscaleForm{
		domains: "+.Tail1234.ts.net\n\nlab.example.net\nlab.example.net",
		routes:  "10.9.0.0/16\n0.0.0.0/0",
		dns:     "10.9.0.53\nfd7a:115c:a1e0::53",
	}
	cfg, errs := f.parse()
	if !slices.Equal(cfg.Domains, []string{"tail1234.ts.net", "lab.example.net"}) || !slices.Equal(cfg.Routes, []string{"10.9.0.0/16"}) || !slices.Equal(cfg.DNS, []string{"10.9.0.53"}) {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !strings.Contains(errs[fieldTSRoutes], "2") || !strings.Contains(errs[fieldTSDNS], "2") || errs[fieldTSDomains] != "" {
		t.Fatalf("errs = %v", errs)
	}
	back := tailscaleFormFrom(cfg)
	if c2, e2 := back.parse(); len(e2) != 0 || !slices.Equal(c2.Domains, cfg.Domains) {
		t.Fatalf("round trip: %+v %v", c2, e2)
	}
}

func TestTailscaleRowDesc(t *testing.T) {
	st := model.TailscaleState{Running: true, Interface: "utun5", Suffix: "tail1234.ts.net", CIDRs: []string{"100.64.0.0/10"}, Domains: []string{"tail1234.ts.net", "ts.net"}}
	if d := tailscaleRowDesc(true, st, model.TailscaleConfig{}); !strings.Contains(d, "utun5") || !strings.Contains(d, "tail1234.ts.net") {
		t.Fatalf("desc = %q", d)
	}
	st.ExitNode = true
	if d := tailscaleRowDesc(true, st, model.TailscaleConfig{Routes: []string{"10.0.0.0/8"}}); !strings.Contains(d, "出口节点") || !strings.Contains(d, "1 个网段") {
		t.Fatalf("desc = %q", d)
	}
}

func TestTSBuiltinForm(t *testing.T) {
	f := tsBuiltinForm{hostname: "Work Mac", controlURL: "https://hs.example.com/", exitNode: "auto:any", acceptRoutes: true}
	b, errs := f.apply(model.TailscaleBuiltin{Enabled: true, StateGen: 2})
	if len(errs) != 0 || b.Hostname != "work-mac" || b.ControlURL != "https://hs.example.com" || !b.AcceptRoutes || !b.Enabled || b.StateGen != 2 {
		t.Fatalf("%+v %v", b, errs)
	}
	if _, errs := (tsBuiltinForm{controlURL: "hs.example.com", exitNode: "a b"}).apply(model.TailscaleBuiltin{}); errs["control"] == "" || errs["exit"] == "" {
		t.Fatalf("errs = %v", errs)
	}
	if got := tsBuiltinFormFrom(b); got.hostname != "work-mac" || !got.acceptRoutes {
		t.Fatalf("form = %+v", got)
	}
}

func TestTSBuiltinRowDesc(t *testing.T) {
	on := model.TailscaleBuiltin{Enabled: true}
	st := model.TailscaleState{Builtin: model.TailscaleBuiltinState{State: model.TSStateRunning, Hostname: "mac-lanflow", Addresses: []string{"100.101.1.2"}, LoginName: "u@example.com"}}
	if d := tsBuiltinRowDesc(on, st, true); !strings.Contains(d, "mac-lanflow") || !strings.Contains(d, "100.101.1.2") || !strings.Contains(d, "u@example.com") {
		t.Fatalf("desc = %q", d)
	}
	if d := tsBuiltinRowDesc(on, st, false); !strings.Contains(d, "内核未运行") {
		t.Fatalf("stopped desc = %q", d)
	}
	st.Builtin = model.TailscaleBuiltinState{State: model.TSStatePausedForApp}
	if d := tsBuiltinRowDesc(on, st, true); !strings.Contains(d, "暂停") {
		t.Fatalf("paused desc = %q", d)
	}
	if d := tsBuiltinRowDesc(model.TailscaleBuiltin{}, st, true); !strings.Contains(d, "无需运行 Tailscale App") {
		t.Fatalf("off desc = %q", d)
	}
}
