package app

import (
	"context"
	"runtime"

	"lanflow/internal/core"
	"lanflow/internal/i18n"
	"lanflow/internal/platform/helper"
	"lanflow/internal/platform/sysproxy"
	"lanflow/internal/platform/tunnel"
)

// onPhone reports whether this build runs on a phone (iOS or Android),
// where the system VPN (package tunnel) replaces the system proxy and the
// TUN helper. A variable so that tests on a desktop can play a phone.
var onPhone = func() bool { return runtime.GOOS == "ios" || runtime.GOOS == "android" }

// phoneDeps adapts the desktop dependencies to a phone: the core runs in
// the system VPN t (nil when this build registered none: every start then
// fails with tunnel.ErrNoTunnel), there is no system proxy, no system DNS
// to set, no helper to install, no stray core of another run to find, no
// other VPN client to detect and no process icons.
//
// The VPN plays the part of the helper: TUN is always available, and the
// exec and helper runners are one and the same, the VPN's.
func phoneDeps(d deps, t tunnel.Tunnel) deps {
	d.core = phoneCore{t}
	d.sys = phoneSysProxy{}
	d.dns = nil
	d.helper = phoneHelper{}
	d.stray = nil
	d.otherTUN = nil
	d.portOwner = nil
	d.v6Route = nil
	d.vpnDetect = nil
	d.tsDetect = nil
	d.fileIcon = nil
	return d
}

// phoneControllerAddress is the controller endpoint on a phone: the
// tunnel's when it names one, else core's default.
func phoneControllerAddress(t tunnel.Tunnel) string {
	if t != nil {
		if a := t.ControllerAddress(); a != "" {
			return a
		}
	}
	return core.ControllerAddress()
}

// phoneCore is coreTools over a tunnel.
type phoneCore struct{ t tunnel.Tunnel }

func (c phoneCore) FindBinary(string) (string, error) {
	if c.t == nil {
		return "", tunnel.ErrNoTunnel
	}
	return c.t.Binary()
}

func (c phoneCore) Version(ctx context.Context, _ string) (string, error) {
	if c.t == nil {
		return "", tunnel.ErrNoTunnel
	}
	return c.t.Version(ctx)
}

func (c phoneCore) Validate(ctx context.Context, _, workDir, config string) error {
	if c.t == nil {
		return tunnel.ErrNoTunnel
	}
	return c.t.Validate(ctx, workDir, config)
}

func (c phoneCore) ExecRunner() core.Runner   { return c.runner() }
func (c phoneCore) HelperRunner() core.Runner { return c.runner() }

func (c phoneCore) runner() core.Runner {
	if c.t == nil {
		return noTunnelRunner{}
	}
	return c.t.Runner()
}

// noTunnelRunner refuses to start: a phone build without a tunnel.
type noTunnelRunner struct{}

func (noTunnelRunner) Start(context.Context, core.Spec) error { return tunnel.ErrNoTunnel }
func (noTunnelRunner) Stop(context.Context) error             { return nil }
func (noTunnelRunner) Running() bool                          { return false }
func (noTunnelRunner) Exited() <-chan error                   { return nil }

// phoneSysProxy: phones have no system proxy an app may set.
type phoneSysProxy struct{}

var errPhoneSysProxy = i18n.Error("手机上没有系统代理，请使用 VPN 模式")

func (phoneSysProxy) Set(string, int, []string) error   { return errPhoneSysProxy }
func (phoneSysProxy) Clear() error                      { return nil }
func (phoneSysProxy) SetPAC(string) error               { return errPhoneSysProxy }
func (phoneSysProxy) Get() (sysproxy.State, error)      { return sysproxy.State{}, nil }
func (phoneSysProxy) Snapshot() (sysproxy.Saved, error) { return nil, nil }
func (phoneSysProxy) Restore(sysproxy.Saved) error      { return nil }

// phoneHelper: the system VPN needs no helper; it is always "installed".
type phoneHelper struct{}

func (phoneHelper) Check(context.Context) error                          { return nil }
func (phoneHelper) SetBundled(context.Context, string) error             { return nil }
func (phoneHelper) Install(context.Context, helper.InstallOptions) error { return nil }
func (phoneHelper) Uninstall(context.Context) error                      { return nil }
