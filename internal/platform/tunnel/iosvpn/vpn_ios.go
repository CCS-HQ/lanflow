//go:build ios

package iosvpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"lanflow/internal/core"
	"lanflow/internal/i18n"
)

// stopTimeout bounds how long Stop waits for the system to report the VPN
// down.
const stopTimeout = 5 * time.Second

// VPN is the packet tunnel as a core.Runner: Start hands the config over
// and starts the VPN, whose extension runs the core. It is also a
// core.ConfigStager (the extension stages every reload: the TUN's
// descriptor is only known there) and a core.Adopter (the VPN outlives the
// app, which may find it running when it starts again).
type VPN struct {
	plugin *mygo.NativePlugin

	wireOnce sync.Once
	mu       sync.Mutex
	status   string        // the last status the system reported
	changed  chan struct{} // closed and replaced on every status
	active   bool          // started or adopted, and not stopped since
	stopping bool          // Stop was called: the VPN going down is expected
	exited   chan error    // replaced on every Start
}

// New returns the VPN.
func New() *VPN {
	return &VPN{
		plugin:  mygo.Native("lanflowtunnel"),
		changed: make(chan struct{}),
		exited:  make(chan error, 1),
	}
}

var (
	_ core.Runner       = (*VPN)(nil)
	_ core.ConfigStager = (*VPN)(nil)
	_ core.Adopter      = (*VPN)(nil)
)

// Paths returns the core's paths in the App Group container.
func (v *VPN) Paths() (Paths, error) {
	g, err := groupContainer()
	if err != nil {
		return Paths{}, err
	}
	return PathsIn(g), nil
}

// ControllerAddress is the controller's socket in the container, or ""
// when the container is unavailable (the start then fails saying why).
func (v *VPN) ControllerAddress() string {
	p, err := v.Paths()
	if err != nil {
		return ""
	}
	return p.Controller
}

func (v *VPN) wire() {
	v.wireOnce.Do(func() {
		v.plugin.On("status", v.onStatus)
	})
}

type statusEvent struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

func down(status string) bool { return status == "disconnected" || status == "invalid" || status == "" }

func (v *VPN) onStatus(data []byte) {
	var ev statusEvent
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.status = ev.Status
	close(v.changed)
	v.changed = make(chan struct{})
	if !down(ev.Status) || !v.active || v.stopping {
		return
	}
	v.active = false
	select {
	case v.exited <- v.exitError(ev.Error):
	default:
	}
}

// exitError explains a VPN that went down by itself; mu is held.
func (v *VPN) exitError(reason string) error {
	var log string
	if p, err := v.Paths(); err == nil {
		log = tail(filepath.Join(p.Home, logName), 20)
	}
	switch {
	case reason != "" && log != "":
		return errors.New(i18n.Tf("系统 VPN 已断开（%s），最近日志：\n%s", reason, log))
	case reason != "":
		return errors.New(i18n.Tf("系统 VPN 已断开（%s）", reason))
	case log != "":
		return errors.New(i18n.Tf("系统 VPN 已断开，最近日志：\n%s", log))
	}
	return i18n.Error("系统 VPN 已断开")
}

// queryStatus asks the system for the VPN's status.
func (v *VPN) queryStatus(ctx context.Context) (string, error) {
	var r struct {
		Status string `json:"status"`
	}
	if err := v.plugin.Call(ctx, "status", nil, &r); err != nil {
		return "", err
	}
	v.mu.Lock()
	v.status = r.Status
	v.mu.Unlock()
	return r.Status, nil
}

// waitDown waits until the system reports the VPN down.
func (v *VPN) waitDown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	for {
		v.mu.Lock()
		st, ch := v.status, v.changed
		v.mu.Unlock()
		if down(st) {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return fmt.Errorf(i18n.T("系统 VPN 未能停止：%w"), ctx.Err())
		}
	}
}

// Start hands the config over and starts the VPN. A VPN still up from an
// earlier run of the app is stopped first, so the core runs this config.
func (v *VPN) Start(ctx context.Context, spec core.Spec) error {
	v.wire()
	p, err := v.Paths()
	if err != nil {
		return err
	}
	if err := checkSocket(p.Controller); err != nil {
		return err
	}
	if err := handOver(p, spec.WorkDir, spec.Config); err != nil {
		return fmt.Errorf(i18n.T("无法把配置交给系统 VPN：%w"), err)
	}
	if st, err := v.queryStatus(ctx); err == nil && !down(st) {
		if err := v.Stop(ctx); err != nil {
			return err
		}
	}
	v.mu.Lock()
	v.active, v.stopping = true, false
	v.exited = make(chan error, 1)
	v.mu.Unlock()
	if err := v.plugin.Call(ctx, "start", nil, nil); err != nil {
		v.mu.Lock()
		v.active = false
		v.mu.Unlock()
		return fmt.Errorf(i18n.T("无法启动系统 VPN：%w"), err)
	}
	return nil
}

// Stop takes the VPN down, which ends the core.
func (v *VPN) Stop(ctx context.Context) error {
	v.wire()
	v.mu.Lock()
	v.stopping = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.active = false
		v.mu.Unlock()
	}()
	if err := v.plugin.Call(ctx, "stop", nil, nil); err != nil {
		return fmt.Errorf(i18n.T("无法停止系统 VPN：%w"), err)
	}
	if _, err := v.queryStatus(ctx); err != nil {
		return nil // the system knows no VPN of ours: nothing runs
	}
	return v.waitDown(ctx)
}

func (v *VPN) Running() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.active
}

func (v *VPN) Exited() <-chan error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.exited
}

// StageConfig hands the config over again and has the extension stage it
// with its TUN descriptor; the path it answers is the one to reload.
func (v *VPN) StageConfig(ctx context.Context, spec core.Spec) (string, error) {
	p, err := v.Paths()
	if err != nil {
		return "", err
	}
	if err := handOver(p, spec.WorkDir, spec.Config); err != nil {
		return "", fmt.Errorf(i18n.T("无法把配置交给系统 VPN：%w"), err)
	}
	// Before a start the tunnel is not up to stage it: the handed-over
	// copy is the config, and the extension stages it as it starts (Start
	// hands that same file over again, unchanged).
	if st, err := v.queryStatus(ctx); err != nil || (st != "connected" && st != "reasserting") {
		return filepath.Join(p.Home, sourceName), nil
	}
	var reply struct {
		Data string `json:"data"`
	}
	if err := v.plugin.Call(ctx, "message", map[string]string{"data": `{"op":"stage"}`}, &reply); err != nil {
		return "", fmt.Errorf(i18n.T("系统 VPN 未能接收配置：%w"), err)
	}
	var r struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(reply.Data), &r); err != nil {
		return "", fmt.Errorf(i18n.T("系统 VPN 的回复无法解析：%w"), err)
	}
	if r.Error != "" {
		return "", errors.New(r.Error)
	}
	return r.Path, nil
}

// Adopt takes over a VPN that is up (the app was ended and opened again
// while it ran); the manager then checks its controller answers.
func (v *VPN) Adopt(ctx context.Context, _ core.Spec) error {
	v.wire()
	st, err := v.queryStatus(ctx)
	if err != nil {
		return err
	}
	if st != "connected" && st != "reasserting" {
		return core.ErrNothingToAdopt
	}
	v.mu.Lock()
	v.active, v.stopping = true, false
	v.exited = make(chan error, 1)
	v.mu.Unlock()
	return nil
}

// Release forgets the VPN without stopping it.
func (v *VPN) Release() {
	v.mu.Lock()
	v.active = false
	v.mu.Unlock()
}
