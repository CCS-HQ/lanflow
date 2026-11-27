// Package android is Android's system VPN for Lanflow: a native module of
// MyGo whose Kotlin (kotlin/) runs a VpnService as a foreground service and
// hands the file descriptor of its interface to Go. It also ships the
// Mihomo program, as libmihomo.so among the app's native libraries
// (kotlin/src/main/jniLibs, built by scripts/build-core-android.sh): an
// app may run a program only from its APK or its native library directory,
// never from its data.
//
// Package tunnel (tunnel_android.go) runs Mihomo on that descriptor; this
// package only speaks to the system. Elsewhere than on Android every
// constructor returns errors.ErrUnsupported. See AGENTS.md for how a native
// module is laid out, and docs/phone-android.md.
package android

import (
	"context"
	"iter"
)

// VPN is Lanflow's VpnService.
//
//mygo:native
type VPN interface {
	// Prepare asks the user, the first time, to let Lanflow set up a VPN
	// (VpnService.prepare), and reports whether it may. It also asks, without
	// waiting for the answer, for the notification permission the VPN's
	// foreground notice needs on Android 13 and later; a refusal there only
	// hides the notice.
	Prepare(ctx context.Context) (bool, error)
	// Start brings the VPN up as a foreground service with cfg, and returns
	// the file descriptor of its interface, which the caller then owns and
	// closes. A VPN already up is taken down first.
	Start(ctx context.Context, cfg Config) (int, error)
	// Stop takes the VPN down and ends the foreground service. The
	// interface goes away once every copy of its descriptor is closed.
	Stop() error
	// Events yields why the VPN went down without Stop: "revoked" when
	// the user turned it off or another VPN took over, "destroyed" when
	// the system ended the service.
	Events(ctx context.Context) iter.Seq2[string, error]
	// NativeLibraryDir is the directory the app's native libraries are
	// extracted to at install. MyGo's build leaves them in the APK
	// (extractNativeLibs false), so it is usually empty.
	NativeLibraryDir() (string, error)
	// APKPath is the path of the app's APK, where libmihomo.so lies,
	// uncompressed, at lib/arm64-v8a/.
	APKPath() (string, error)
}

// Config is the VPN's interface: the addresses Mihomo's TUN expects on it,
// its routes and its DNS server.
type Config struct {
	Session  string   // the name the system shows for the VPN
	MTU      int      // the interface's MTU
	Address4 string   // an IPv4 address with its prefix, as 198.18.0.1/30
	Address6 string   // an IPv6 address with its prefix, or "" for no IPv6
	DNS      []string // the DNS servers apps are given, inside the VPN
	// Exclude are prefixes routed outside the VPN, as 192.168.0.0/16
	// (Android 13 and later; ignored before).
	Exclude []string
}
