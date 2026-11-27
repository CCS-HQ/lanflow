package app

import (
	"context"
	"errors"
	"testing"

	"lanflow/internal/core"
	"lanflow/internal/platform/tunnel"
)

type fakeTunnel struct {
	r       core.Runner
	addr    string
	checked string
}

func (t *fakeTunnel) Runner() core.Runner     { return t.r }
func (t *fakeTunnel) Binary() (string, error) { return tunnel.BuiltinBinary, nil }
func (t *fakeTunnel) Version(context.Context) (string, error) {
	return "v1.19.34", nil
}
func (t *fakeTunnel) Validate(_ context.Context, workDir, config string) error {
	t.checked = workDir + "|" + config
	return nil
}
func (t *fakeTunnel) ControllerAddress() string { return t.addr }

func TestPhoneDepsRunInTheTunnel(t *testing.T) {
	ft := &fakeTunnel{r: noTunnelRunner{}, addr: "/group/lanflow.sock"}
	d := phoneDeps(deps{sys: realSysProxy{}, dns: realSysDNS{}, helper: realHelper{}, stray: realStray{}}, ft)
	ctx := context.Background()

	if bin, err := d.core.FindBinary("/custom/mihomo"); err != nil || bin != tunnel.BuiltinBinary {
		t.Fatalf("FindBinary = %q, %v; want the tunnel's binary", bin, err)
	}
	if v, err := d.core.Version(ctx, tunnel.BuiltinBinary); err != nil || v != "v1.19.34" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	if err := d.core.Validate(ctx, tunnel.BuiltinBinary, "/w", "/w/c.yaml"); err != nil || ft.checked != "/w|/w/c.yaml" {
		t.Fatalf("Validate = %v, checked %q", err, ft.checked)
	}
	if d.core.ExecRunner() != ft.r || d.core.HelperRunner() != ft.r {
		t.Fatal("both runners must be the tunnel's")
	}
	if err := d.helper.Check(ctx); err != nil {
		t.Fatalf("helper.Check = %v; the VPN needs no helper", err)
	}
	if err := d.sys.Set("127.0.0.1", 7890, nil); !errors.Is(err, errPhoneSysProxy) {
		t.Fatalf("sys.Set = %v; want errPhoneSysProxy", err)
	}
	if err := d.sys.Clear(); err != nil {
		t.Fatalf("sys.Clear = %v; nothing to clear on a phone", err)
	}
	if d.dns != nil || d.stray != nil || d.vpnDetect != nil || d.tsDetect != nil || d.fileIcon != nil {
		t.Fatal("system DNS, stray cores, VPN detection and icons must be off on a phone")
	}
	if got := phoneControllerAddress(ft); got != "/group/lanflow.sock" {
		t.Fatalf("controller = %q; want the tunnel's", got)
	}
}

func TestPhoneDepsWithoutTunnel(t *testing.T) {
	d := phoneDeps(deps{}, nil)
	ctx := context.Background()
	if _, err := d.core.FindBinary(""); !errors.Is(err, tunnel.ErrNoTunnel) {
		t.Fatalf("FindBinary = %v; want ErrNoTunnel", err)
	}
	if err := d.core.ExecRunner().Start(ctx, core.Spec{}); !errors.Is(err, tunnel.ErrNoTunnel) {
		t.Fatalf("Start = %v; want ErrNoTunnel", err)
	}
	if got := phoneControllerAddress(nil); got != core.ControllerAddress() {
		t.Fatalf("controller = %q; want core's default", got)
	}
}

// On a phone the core runs in the system VPN even with TUN off in the
// settings (tunRunner false): a reload still names the config the tunnel
// staged, never runtime.yaml, which lacks the VPN's descriptor.
func TestPhoneReloadAlwaysStages(t *testing.T) {
	saved := onPhone
	onPhone = func() bool { return true }
	t.Cleanup(func() { onPhone = saved })

	h := newHarness(t, nil)
	var staged []core.Spec
	h.s.d.core = stagingCore{fakeCore: h.core, runner: stagingRunner{namedRunner: namedRunner{"tunnel"}, specs: &staged, path: "/data/core/android-runtime.yaml"}}
	h.s.mu.Lock()
	h.s.state.tunRunner = false
	h.s.mu.Unlock()
	path, err := h.s.reloadPath(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/data/core/android-runtime.yaml" || len(staged) != 1 || staged[0].Config != h.s.runtime {
		t.Fatalf("reloadPath = %q, staged %+v", path, staged)
	}
}
