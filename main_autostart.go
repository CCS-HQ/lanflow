package main

import (
	"log"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/egoist/mygo"

	autostartpkg "lanflow/internal/autostart"
)

var migrateLogin sync.Once

// autostart runs applyAutostart on a goroutine of its own, the latest
// setting winning: removing the old login item (SMAppService) and writing
// the launch agent can take a while when macOS shows its 「后台活动」 or
// login item notices, and must never hold up the main thread or the
// service's change listeners.
type autostartWant struct {
	open, admin bool
}

var autostart struct {
	sync.Mutex
	want      *autostartWant
	busy      bool
	applied   bool
	lastOpen  bool
	lastAdmin bool
}

func queueAutostart(exe string, open, admin bool) {
	autostart.Lock()
	autostart.want = &autostartWant{open: open, admin: admin}
	if autostart.busy {
		autostart.Unlock()
		return
	}
	autostart.busy = true
	autostart.Unlock()
	go func() {
		for {
			autostart.Lock()
			want := autostart.want
			autostart.want = nil
			if want == nil {
				autostart.busy = false
				autostart.Unlock()
				return
			}
			skip := autostart.applied && want.open == autostart.lastOpen && want.admin == autostart.lastAdmin
			autostart.Unlock()
			if skip {
				continue
			}
			if applyAutostart(exe, want.open, want.admin) {
				autostart.Lock()
				autostart.applied = true
				autostart.lastOpen = want.open
				autostart.lastAdmin = want.admin
				autostart.Unlock()
			}
		}
	}()
}

// applyAutostart (through queueAutostart) makes the login start the service process
// (internal/autostart: a launch agent on macOS, a Run entry or Scheduled Task on Windows,
// an XDG autostart entry on Linux) when Settings.OpenAtLogin is on. The login item of the
// versions before the split (MyGo's, which opens the app, i.e. the window) is removed the
// first time: the service started at login opens the window itself
// unless Settings.SilentStart (docs/service-split.md S3).
func applyAutostart(exe string, open, admin bool) bool {
	migrateLogin.Do(func() {
		dropLegacyLoginItem()
		if mygo.App.OpenAtLogin() {
			if err := mygo.App.SetOpenAtLogin(false); err != nil {
				log.Printf("lanflow: remove the old login item: %v", err)
			} else {
				log.Printf("lanflow: login item moved to the service process")
			}
		}
	})
	if (runtime.GOOS == "darwin" || runtime.GOOS == "windows" || runtime.GOOS == "linux") && !mygo.App.IsPackaged() {
		return true // development builds: no stable path to start at login
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	changed, err := autostartpkg.ApplyElevated(exe, open, admin)
	switch {
	case err != nil:
		log.Printf("lanflow: open at login: %v", err)
		return false
	case changed:
		log.Printf("lanflow: open at login: %v admin=%v (%s)", open, admin, exe)
	}
	return true
}
