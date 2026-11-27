//go:build ios

package tunnel

import (
	"context"

	"lanflow/internal/core"
	"lanflow/internal/platform/tunnel/iosvpn"
)

// On iOS the core is Mihomo built into the packet tunnel extension
// (package iosvpn).
func init() { Register(iosTunnel{iosvpn.New()}) }

type iosTunnel struct{ vpn *iosvpn.VPN }

func (t iosTunnel) Runner() core.Runner { return t.vpn }

func (iosTunnel) Binary() (string, error) { return BuiltinBinary, nil }

func (iosTunnel) Version(context.Context) (string, error) { return iosvpn.CoreVersion, nil }

func (iosTunnel) Validate(_ context.Context, _, config string) error {
	return iosvpn.Validate(config)
}

func (t iosTunnel) ControllerAddress() string { return t.vpn.ControllerAddress() }
