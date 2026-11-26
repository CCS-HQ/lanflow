// Package tunnel is the system VPN of a phone, the only way an app takes
// over a phone's traffic: Android's VpnService, iOS's packet tunnel
// extension (NetworkExtension). It stands where the desktop has the system
// proxy and the TUN helper.
//
// The service (internal/app) knows nothing about either system. Each phone
// registers its Tunnel from a file built for it alone (tunnel_android.go,
// tunnel_ios.go), with Register in an init function; the service asks
// Current when it starts the core. Desktops register nothing.
//
// Contract for an implementation:
//   - Runner().Start(spec) brings the VPN up (asking the user the first
//     time, as the system wants) and runs Mihomo on spec.Config, with the
//     VPN's file descriptor as the core's TUN (tun.file-descriptor), and
//     returns once the core is spawned; the service then waits for the
//     controller at ControllerAddress to answer GET /version.
//     Runner().Stop takes the VPN down and ends the core. Exited reports a
//     VPN or core that went away without Stop (the user turned it off in
//     the system's settings, another VPN took over, the system ended the
//     extension).
//   - Binary names the Mihomo program when the core runs as a program
//     (Android, from the app's native libraries); it is BuiltinBinary when
//     the core is a library inside the tunnel (iOS, which starts no
//     programs).
//   - Version and Validate stand for `mihomo -v` and `mihomo -t`, which an
//     iOS app cannot run.
//   - ControllerAddress is the unix socket of the core's controller; ""
//     keeps core.ControllerAddress. On iOS it lies in the App Group
//     container the app shares with its extension.
package tunnel

import (
	"context"
	"sync"

	"lanflow/internal/core"
	"lanflow/internal/i18n"
)

// BuiltinBinary is what Binary returns for a core built into the tunnel.
const BuiltinBinary = "builtin:mihomo"

// ErrNoTunnel means no Tunnel was registered for this build.
var ErrNoTunnel = i18n.Error("此版本没有可用的系统 VPN")

// Tunnel is a phone's system VPN with Mihomo inside.
type Tunnel interface {
	Runner() core.Runner
	Binary() (string, error)
	Version(ctx context.Context) (string, error)
	Validate(ctx context.Context, workDir, config string) error
	ControllerAddress() string
}

var (
	mu      sync.Mutex
	current Tunnel
)

// Register makes t the tunnel of this build. A second call replaces the
// first.
func Register(t Tunnel) {
	mu.Lock()
	current = t
	mu.Unlock()
}

// Current returns the registered tunnel, or nil.
func Current() Tunnel {
	mu.Lock()
	defer mu.Unlock()
	return current
}
