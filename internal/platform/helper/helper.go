// Package helper installs and talks to lanflow-helper, the privileged
// service that runs Mihomo as root (macOS LaunchDaemon) or LocalSystem
// (Windows service) so TUN mode works without running the whole app
// elevated.
//
// Exported signatures are a stable contract; the implementation is here
// plus cmd/lanflow-helper.
//
// Security requirements (must hold):
//   - The helper listens only on a local endpoint: a unix socket
//     /var/run/lanflow-helper.sock owned by root, chmod 0600 then chown to
//     the installing user's uid (macOS); a named pipe \\.\pipe\lanflow-helper
//     with a DACL granting only SYSTEM, Administrators and the installing
//     user's SID (Windows).
//   - The helper runs only the Mihomo binary recorded at install time
//     (path + SHA-256 in a root/SYSTEM-owned config next to the helper);
//     StartCore with any other binary or a changed hash is refused.
//   - The binary recorded is the one the app hashed before the prompt:
//     install passes its SHA-256 (--core-sha256) and the elevated
//     installer refuses a copy with another digest, or no digest at all.
//   - Work dir, config and log paths must be inside the installing user's
//     Lanflow data directory recorded at install time.
//   - Mihomo never runs on those user-owned paths. The helper runs it in
//     a work directory of its own that only root/SYSTEM can write
//     (service.WorkDir), on a copy of the config that it read without
//     following a link, checked and cleaned (no controller or web UI
//     keys, provider paths inside the work directory); databases and
//     provider files are copied in the same way. A hot reload goes
//     through the helper again (protocol.OpStage, core.ConfigStager).
//     The log file and the controller endpoint stay the user's.
//   - Install/uninstall prompt for administrator rights: osascript
//     "do shell script … with administrator privileges" (macOS),
//     ShellExecute "runas" of the helper binary with an install verb (Windows).
//   - Uninstall stops Mihomo, removes the service and its files.
package helper

import (
	"context"
	"errors"
	"net"

	"lanflow/internal/core"
)

// InstallOptions describe what the helper may run.
type InstallOptions struct {
	HelperBin string // path of the lanflow-helper binary shipped with the app
	CoreBin   string // the Mihomo binary the helper is allowed to run
	// CoreSHA256, when set, is the hex SHA-256 CoreBin must have (a pinned
	// release). Install always hashes CoreBin before the administrator
	// prompt and the elevated installer only accepts that content; this
	// additionally refuses a file that is not the expected one.
	CoreSHA256 string
	DataDir    string // the user's Lanflow data dir (work dir/config/log must be inside)
}

// Installed reports whether the helper is installed and answers a ping
// with this app's protocol and helper version. A helper of another
// version reports false so the app offers to reinstall; Check says why.
func Installed(ctx context.Context) bool { return installed(ctx) }

// Check returns nil when Installed would be true, else an error matching
// ErrNotRunning, ErrProtocolMismatch or ErrVersionMismatch (errors.Is);
// a version mismatch is a *VersionError carrying both versions.
func Check(ctx context.Context) error { return check(ctx) }

// SetBundledHelper records the helper binary shipped with the app: its
// version (`lanflow-helper version`) becomes the version Installed and
// Check require. Call it once at startup unless ExpectedVersion is set at
// build time.
func SetBundledHelper(ctx context.Context, path string) error {
	_, err := learnBundledVersion(ctx, path)
	return err
}

// Install installs (or upgrades) and starts the helper. Prompts for admin
// rights. It refuses (ErrDowngrade) to replace a running helper that is
// newer than HelperBin.
func Install(ctx context.Context, o InstallOptions) error { return install(ctx, o) }

// Uninstall removes the helper. Prompts for admin rights.
func Uninstall(ctx context.Context) error { return uninstall(ctx) }

// NewRunner returns a core.Runner that starts Mihomo through the helper.
// Stop asks the helper to stop it; Exited reports unexpected exits that the
// helper reports (poll Status every second if push is not implemented).
// The runner is also a core.ConfigStager: the helper's core runs on a copy
// of the config, so a reload must stage it first.
func NewRunner() core.Runner { return newRunner() }

// CoreRunning reports whether the helper runs Mihomo for workDir. The
// helper only starts cores of the data directory it was installed for; a
// Lanflow of another data directory (a second install, a test) gets false
// for that core.
func CoreRunning(ctx context.Context, workDir string) (bool, error) {
	return coreRunning(ctx, newClient(), workDir)
}

// Disable makes every call to the helper fail as if none were installed,
// until the returned function is called. For tests that run the real
// service on a machine where a helper may be serving the user's Lanflow.
func Disable() (restore func()) {
	old := dialHelper
	dialHelper = func(context.Context) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: errors.New("helper disabled")}
	}
	return func() { dialHelper = old }
}
