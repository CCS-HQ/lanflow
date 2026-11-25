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
