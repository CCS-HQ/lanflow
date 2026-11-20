package autostart

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// The Run value of the user; Task Manager's Startup page turns it off in
// StartupApproved without removing it.
const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

func command(exe string) string { return `"` + exe + `" ` + strings.Join(Args, " ") }

// registryRun is runStore over HKCU.
type registryRun struct{ k registry.Key }

func (r registryRun) Get() (string, bool) {
	v, _, err := r.k.GetStringValue(Label)
	return v, err == nil
}

func (r registryRun) Set(cmd string) error { return r.k.SetStringValue(Label, cmd) }
func (r registryRun) Delete() error        { return r.k.DeleteValue(Label) }

func (r registryRun) ClearApproval() bool {
	a, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE)
	if err != nil {
		return false
	}
	defer a.Close()
	return a.DeleteValue(Label) == nil
}

func apply(exe string, on bool) (bool, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	return applyRun(registryRun{k}, command(exe), on)
}

func enabled(exe string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	have, _, err := k.GetStringValue(Label)
	if err != nil || !strings.EqualFold(have, command(exe)) {
		return false
	}
	a, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer a.Close()
	state, _, err := a.GetBinaryValue(Label)
	return err != nil || len(state) == 0 || state[0]&1 == 0
}
