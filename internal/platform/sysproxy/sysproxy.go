// Package sysproxy sets and clears the OS system proxy.
//
// Exported signatures are a stable contract. Implementations: darwin (networksetup on every enabled network service) and
// windows (WinINet per-connection options on the LAN and on every dial-up /
// VPN phone-book entry, with the per-user registry values as the LAN
// fallback, then InternetSetOption INTERNET_OPTION_SETTINGS_CHANGED /
// REFRESH via golang.org/x/sys/windows)
// in build-tagged files, plus a no-op/unsupported implementation elsewhere.
package sysproxy

// State is the current system proxy setting (HTTP/HTTPS/SOCKS as one view).
type State struct {
	Enabled bool
	Host    string
	Port    int
	Bypass  []string
	// PACURL is the automatic proxy configuration URL when PAC is on
	// ("" when off or unset). Enabled does not cover it.
	PACURL string
}

// Set points the HTTP, HTTPS and SOCKS proxies at host:port with bypass.
func Set(host string, port int, bypass []string) error { return set(host, port, bypass) }

// Clear turns the system proxy off (HTTP, HTTPS, SOCKS) without touching PAC settings.
func Clear() error { return clear() }

// SetPAC points the system's automatic proxy configuration (PAC) at url
// and turns it on; "" turns PAC off, leaving the URL stored. The manual
// HTTP/HTTPS/SOCKS proxies are not touched (docs/design.md §5.8 高级 ·
// PAC 模式).
func SetPAC(url string) error { return setPAC(url) }

// Get reads the current setting (from the primary service on macOS).
func Get() (State, error) { return get() }

// Saved is an opaque, JSON-serializable snapshot of the system proxy
// settings taken before Lanflow changes them, so they can be put back
// exactly (another app's proxy, a PAC-less manual proxy, or "off").
type Saved []byte

// Snapshot records the current HTTP/HTTPS/SOCKS proxy settings (every
// enabled network service on macOS; the LAN and every dial-up / VPN entry
// on Windows).
func Snapshot() (Saved, error) { return snapshot() }

// Restore puts back what Snapshot recorded. Services that did not exist at
// snapshot time get their proxies turned off.
func Restore(s Saved) error { return restore(s) }

// ReleaseDialUp is for the moment Lanflow gives up a system proxy that is no
// longer its own (the user or another program changed it), so that Restore
// and Clear are not called. On Windows Set and SetPAC also wrote every
// dial-up / VPN entry, and those may still point at Lanflow: each entry
// that still has the manual proxy at host:port, or the PAC address pac
// ("" = none), gets what s recorded for it back, or has that proxy switched
// off when s has nothing for it. The LAN and entries changed since are not
// touched. s may be empty. A no-op on other systems, where the settings
// Get reads are the only ones Lanflow writes.
func ReleaseDialUp(s Saved, host string, port int, pac string) error {
	return releaseDialUp(s, host, port, pac)
}

// DefaultBypass is the default bypass list.
var DefaultBypass = []string{"localhost", "127.0.0.1", "::1", "192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "*.local", "*.lan", "<local>"}

// AuthenticatedServices names the network services in s whose proxy had a
// login (macOS "Authenticated Proxy Enabled") when the snapshot was taken.
// Restore puts their server and port back but cannot put back the login:
// the caller should tell the user to enter it again in System Settings.
// Always empty on other systems.
func AuthenticatedServices(s Saved) []string { return darwinAuthServices(s) }
