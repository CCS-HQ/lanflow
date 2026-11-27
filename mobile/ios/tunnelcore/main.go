// Command tunnelcore is Mihomo as a library for Lanflow's iOS packet tunnel
// extension (PacketTunnel.appex), built with -buildmode=c-archive and linked
// into the extension's Swift. iOS starts no programs, so the core runs
// inside the extension's process.
//
// The extension and the app share an App Group container. Its core
// directory is Mihomo's home: geodata, cache.db, providers, the app's
// runtime config (source.yaml) and the config the core runs (config.yaml,
// made from source.yaml by package stage). The controller listens on a
// unix socket in the container, which the app's controller client dials as
// it does the desktop core's.
//
// Every function returns a string the caller frees with LanflowCoreFree;
// errors are JSON {"error": "..."} or a plain message, as documented.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unsafe"

	mc "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"

	"lanflow/mobile/ios/tunnelcore/stage"
)

func main() {} // c-archive

// The packet tunnel extension is killed past 50 MB of physical footprint.
// The Go heap is held well under it: the rest is the executable's dirty
// pages, the Swift side, NetworkExtension and the stacks' buffers.
const (
	memoryLimit = 32 << 20
	gcPercent   = 50
	logMaxSize  = 2 << 20
)

var (
	mu      sync.Mutex
	started bool
	logFile *os.File
)

const (
	sourceName = "source.yaml" // the app's runtime config, with its paths moved into the container
	configName = "config.yaml" // what the core runs
	logName    = "mihomo.log"
)

func goString(s *C.char) string { return C.GoString(s) }

func cString(s string) *C.char { return C.CString(s) }

//export LanflowCoreFree
func LanflowCoreFree(p *C.char) { C.free(unsafe.Pointer(p)) }

//export LanflowCoreVersion
func LanflowCoreVersion() *C.char { return cString(mc.Version) }

// LanflowCoreStage writes config.yaml from source.yaml in home, with the
// controller socket and the TUN's fd (0 before the tunnel is up), and
// returns JSON {"path": ..., "network": {...}} or {"error": ...}. The
// network is what the tunnel's settings must match.
//
//export LanflowCoreStage
func LanflowCoreStage(home, controller *C.char, fd C.int) *C.char {
	path, n, err := stageConfig(goString(home), goString(controller), int(fd))
	if err != nil {
		return jsonString(map[string]any{"error": err.Error()})
	}
	return jsonString(map[string]any{"path": path, "network": n})
}

// LanflowCoreStart stages the config and runs the core on the TUN fd. It
// returns "" once the core runs, else the reason.
//
//export LanflowCoreStart
func LanflowCoreStart(home, controller *C.char, fd C.int) *C.char {
	if err := start(goString(home), goString(controller), int(fd)); err != nil {
		return cString(err.Error())
	}
	return cString("")
}

//export LanflowCoreStop
func LanflowCoreStop() {
	mu.Lock()
	defer mu.Unlock()
	if !started {
		return
	}
	executor.Shutdown()
	started = false
	if logFile != nil {
		_ = logFile.Sync()
	}
}

// LanflowCoreTunFD returns the descriptor of the utun interface
// NetworkExtension opened for the tunnel, or -1. The packet flow does not
// expose it; this is how sing-box and other cores find it, by asking each
// open descriptor for its utun interface name.
//
//export LanflowCoreTunFD
func LanflowCoreTunFD() C.int {
	for fd := 0; fd < 1024; fd++ {
		name, err := unix.GetsockoptString(fd, 2 /* SYSPROTO_CONTROL */, 2 /* UTUN_OPT_IFNAME */)
		if err == nil && strings.HasPrefix(name, "utun") {
			return C.int(fd)
		}
	}
	return -1
}

// LanflowCoreMemory reports the Go runtime's memory, as JSON, for the
// app's diagnostics.
//
//export LanflowCoreMemory
func LanflowCoreMemory() *C.char {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return jsonString(map[string]any{
		"heapInuse": m.HeapInuse, "heapSys": m.HeapSys, "stackSys": m.StackSys,
		"sys": m.Sys, "numGC": m.NumGC, "goroutines": runtime.NumGoroutine(),
		"limit": memoryLimit,
	})
}

func jsonString(v any) *C.char {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"error":"internal: cannot encode"}`)
	}
	return cString(string(b))
}

func stageConfig(home, controller string, fd int) (string, stage.Network, error) {
	if home == "" || !filepath.IsAbs(home) {
		return "", stage.Network{}, fmt.Errorf("bad core directory %q", home)
	}
	src, err := os.ReadFile(filepath.Join(home, sourceName))
	if err != nil {
		return "", stage.Network{}, fmt.Errorf("no config from the app: %w", err)
	}
	out, n, err := stage.Apply(src, stage.Params{FD: fd, Controller: controller})
	if err != nil {
		return "", stage.Network{}, err
	}
	path := filepath.Join(home, configName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return "", stage.Network{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", stage.Network{}, err
	}
	return path, n, nil
}

func start(home, controller string, fd int) error {
	mu.Lock()
	defer mu.Unlock()
	if started {
		return errors.New("Mihomo is already running")
	}
	if fd <= 0 {
		return errors.New("no TUN file descriptor")
	}
	debug.SetGCPercent(gcPercent)
	debug.SetMemoryLimit(memoryLimit)
	openLog(home)

	path, _, err := stageConfig(home, controller, fd)
	if err != nil {
		return err
	}
	mc.SetHomeDir(home)
	mc.SetConfig(path)
	_ = os.Remove(controller)
	if err := hub.Parse(nil, hub.WithExternalControllerUnix(controller)); err != nil {
		return fmt.Errorf("Mihomo cannot load the config: %w", err)
	}
	started = true
	return nil
}

// openLog sends Mihomo's log to mihomo.log in home, keeping one older file
// past logMaxSize. The app shows it as the core's log.
func openLog(home string) {
	if logFile != nil {
		return
	}
	p := filepath.Join(home, logName)
	rotateLog(p)
	go func() {
		for range time.Tick(time.Minute) {
			mu.Lock()
			rotateLog(p)
			mu.Unlock()
		}
	}()
}

// rotateLog (re)opens the log, moving it aside first when it is too large;
// mu is held or not needed yet.
func rotateLog(p string) {
	st, err := os.Stat(p)
	if logFile != nil && (err != nil || st.Size() <= logMaxSize) {
		return
	}
	if err == nil && st.Size() > logMaxSize {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	logrus.SetOutput(f)
	if logFile != nil {
		_ = logFile.Close()
	}
	logFile = f
}
