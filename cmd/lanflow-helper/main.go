// Command lanflow-helper is the privileged service that runs Mihomo for
// TUN mode. See internal/platform/helper for the contract.
//
// Usage:
//
//	lanflow-helper run                                     service entry (launchd / SCM)
//	lanflow-helper install --core <path> --core-sha256 <hex> --data-dir <dir> --uid <uid|sid> [--allow-downgrade]
//	lanflow-helper uninstall
//	lanflow-helper version
//
// install and uninstall must run elevated; the app invokes them through
// osascript (macOS) or ShellExecute "runas" (Windows). --core names a file
// the user can rewrite, so install only accepts a copy with the digest the
// app took before asking for administrator rights (--core-sha256), and
// refuses to run without one.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"lanflow/internal/platform/helper/protocol"
	"lanflow/internal/platform/helper/service"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Printf("lanflow-helper %s (protocol %d)\n", version, protocol.Version)
		return
	case "run":
		logger, closeLog := openLogger(service.LogPath)
		defer closeLog()
		logger.Printf("lanflow-helper %s starting", version)
		err = service.Run(ctx, version, logger.Printf)
	case "install":
		var o service.InstallOptions
		if o, err = parseInstall(os.Args[2:], os.Stderr); err != nil {
			os.Exit(2)
		}
		logger, closeLog := openLogger(filepath.Join(service.LogDir, "install.log"))
		defer closeLog()
		logger.Printf("install %s core=%s sha256=%s dataDir=%s uid=%s", version, o.CoreBin, o.CoreSHA256, o.DataDir, o.UID)
		if err = service.Install(ctx, o, logger.Printf); err != nil {
			logger.Printf("install failed: %v", err)
		} else {
			logger.Printf("install ok")
		}
	case "uninstall":
		logger, closeLog := openLogger(filepath.Join(service.LogDir, "install.log"))
		logger.Printf("uninstall %s", version)
		if err = service.Uninstall(ctx, logger.Printf); err != nil {
			logger.Printf("uninstall failed: %v", err)
			closeLog()
		} else {
			logger.Printf("uninstall ok")
			// Close install.log before removing LogDir so Windows does not
			// fail RemoveDirectory with ERROR_DIR_NOT_EMPTY.
			closeLog()
			service.CleanupLogDir()
		}
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanflow-helper:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lanflow-helper run | install --core <path> --core-sha256 <hex> --data-dir <dir> --uid <uid> [--allow-downgrade] | uninstall | version")
}

// parseInstall reads the arguments of `lanflow-helper install`; flag
// errors go to errOut. Values are checked by service.Install.
func parseInstall(args []string, errOut io.Writer) (service.InstallOptions, error) {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var o service.InstallOptions
	fs.StringVar(&o.CoreBin, "core", "", "Mihomo binary to allow")
	fs.StringVar(&o.CoreSHA256, "core-sha256", "", "hex SHA-256 the copied Mihomo binary must have")
	fs.StringVar(&o.DataDir, "data-dir", "", "user's Lanflow data directory")
	fs.StringVar(&o.UID, "uid", "", "installing user's uid (macOS) or SID (Windows)")
	fs.BoolVar(&o.AllowDowngrade, "allow-downgrade", false, "replace a newer installed helper")
	if err := fs.Parse(args); err != nil {
		return service.InstallOptions{}, err
	}
	if fs.NArg() > 0 {
		err := fmt.Errorf("unexpected argument %q", fs.Arg(0))
		fmt.Fprintln(errOut, err)
		return service.InstallOptions{}, err
	}
	o.Version = version
	return o, nil
}

// newLogger logs to stderr and to path.
func newLogger(path string) *log.Logger {
	l, _ := openLogger(path)
	return l
}

// openLogger logs to stderr and to path, returning a close function for the
// opened log file (if any). Under launchd stderr already is LogPath, so the
// file is skipped there to avoid duplicate lines; under the Windows SCM there
// is no console (stderr writes fail with ERROR_INVALID_HANDLE), so errors
// from a writer must not stop subsequent writers.
func openLogger(path string) (*log.Logger, func()) {
	noop := func() {}
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode().IsRegular() && path == service.LogPath {
		return log.New(os.Stderr, "", log.LstdFlags), noop
	}
	writers := []io.Writer{os.Stderr}
	// LogDir is created (or re-secured) for SYSTEM/root only, and the
	// file is opened without following links: install runs elevated, so a
	// log path redirected by an unprivileged user must not be written.
	if err := service.PrepareLogDir(); err != nil {
		fmt.Fprintln(os.Stderr, "lanflow-helper: log dir:", err)
	} else if f, err := service.OpenAdminLog(path); err == nil {
		writers = append(writers, f)
		return log.New(tolerantMultiWriter(writers...), "", log.LstdFlags), func() { _ = f.Close() }
	} else {
		fmt.Fprintln(os.Stderr, "lanflow-helper: log file:", err)
	}
	return log.New(tolerantMultiWriter(writers...), "", log.LstdFlags), noop
}

type tolerantWriter struct {
	writers []io.Writer
}

func tolerantMultiWriter(writers ...io.Writer) io.Writer {
	return tolerantWriter{writers: writers}
}

func (t tolerantWriter) Write(p []byte) (int, error) {
	for _, w := range t.writers {
		_, _ = w.Write(p)
	}
	return len(p), nil
}
