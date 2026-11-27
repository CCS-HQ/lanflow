// Package iosvpn is Lanflow's system VPN on iOS: the packet tunnel
// extension PacketTunnel.appex (mobile/ios/PacketTunnel), which runs Mihomo
// as a library (mobile/ios/tunnelcore), driven through the Swift plugin
// "lanflowtunnel" (ios/LanflowTunnelPlugin.swift, NETunnelProviderManager).
// Package tunnel registers it on iOS (tunnel_ios.go).
//
// The extension cannot read the app's sandbox. The app and the extension
// share an App Group container instead:
//
//	<group>/core/       Mihomo's home: geodata copied from the app's core
//	                    directory, providers, cache.db, mihomo.log,
//	                    source.yaml (the app's runtime config, handed over
//	                    here) and config.yaml (what the extension runs)
//	<group>/c.sock      the controller's unix socket
//
// The files of this package that only iOS builds are *_ios.go; the rest
// (handing the config over, checking it) is plain Go, tested on desktops.
package iosvpn

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// CoreVersion is the Mihomo built into the extension: tunnelcore's go.mod
// requires it, and the build script stamps it (version_test.go keeps them
// equal).
const CoreVersion = "v1.19.34"

// AppGroupKey is the Info.plist key of the App Group's identifier, in the
// app's (mygo.json ios.infoPlist) and the extension's.
const AppGroupKey = "LanflowAppGroup"

// File names in the core's home, shared with tunnelcore.
const (
	sourceName = "source.yaml"
	logName    = "mihomo.log"
)

// maxSocketPath is the usable length of a unix socket path (sun_path is 104
// bytes on Darwin, with the NUL).
const maxSocketPath = 103

// Paths are where the core's files live in the App Group container.
type Paths struct {
	Home       string // Mihomo's home directory
	Controller string // the controller's unix socket
}

// PathsIn returns the paths in an App Group container.
func PathsIn(group string) Paths {
	return Paths{Home: filepath.Join(group, "core"), Controller: filepath.Join(group, "c.sock")}
}

// errSocketPath: the container's path is too long for a unix socket. On a
// device it is /private/var/mobile/Containers/Shared/AppGroup/<UUID>, about
// 90 bytes with the socket's name; a simulator's is longer, but a simulator
// runs no packet tunnel.
func checkSocket(p string) error {
	if len(p) > maxSocketPath {
		return fmt.Errorf("controller socket path is %d bytes, over the %d a unix socket takes: %s", len(p), maxSocketPath, p)
	}
	return nil
}

// geoFile reports whether a file of the app's core directory is geodata
// the extension's core needs as well.
func geoFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mmdb", ".metadb", ".dat":
		return true
	}
	return false
}

// handOver gives the extension's core what it runs on: the runtime config
// as source.yaml, with paths into the app's core directory moved into the
// container, and the geodata of that directory, copied where it is new or
// changed.
func handOver(p Paths, workDir, config string) error {
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return fmt.Errorf("cannot create the tunnel's core directory: %w", err)
	}
	data, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	if workDir != "" {
		data = rebase(data, filepath.Clean(workDir), p.Home)
	}
	if err := writeFile(filepath.Join(p.Home, sourceName), data); err != nil {
		return err
	}
	return syncGeo(workDir, p.Home)
}

// rebase moves absolute paths under from to under to.
func rebase(data []byte, from, to string) []byte {
	if from == "" || from == "/" || from == to {
		return data
	}
	return bytes.ReplaceAll(data, []byte(from+string(filepath.Separator)), []byte(to+string(filepath.Separator)))
}

func syncGeo(from, to string) error {
	if from == "" {
		return nil
	}
	entries, err := os.ReadDir(from)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !geoFile(e.Name()) {
			continue
		}
		src, dst := filepath.Join(from, e.Name()), filepath.Join(to, e.Name())
		si, err := e.Info()
		if err != nil {
			continue
		}
		if di, err := os.Stat(dst); err == nil && di.Size() == si.Size() && !di.ModTime().Before(si.ModTime()) {
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("cannot copy %s to the tunnel: %w", e.Name(), err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Validate stands for `mihomo -t` in the app, which links no Mihomo: the
// core lives in the extension alone (linking it into the app as well would
// add tens of megabytes to it), and parsing a second config inside the
// extension would double its peak memory under the 50 MB limit. So the app
// checks that the config is a YAML mapping; Mihomo checks the rest as it
// loads it, at start (the VPN fails with its reason) and on a hot reload
// (PUT /configs answers 400 with it, and the service keeps the previous
// config).
func Validate(config string) error {
	data, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("配置不是有效的 YAML：%w", err)
	}
	if len(doc) == 0 {
		return errors.New("配置为空")
	}
	return nil
}

// tail returns the last n lines of a file, for an exit's reason.
func tail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > 64<<10 {
		data = data[len(data)-64<<10:]
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
