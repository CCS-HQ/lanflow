package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"lanflow/internal/core"
	"lanflow/internal/model"
)

// stagingRunner stands in for the helper's runner: its core runs on the
// helper's own copy of the config (core.ConfigStager).
type stagingRunner struct {
	namedRunner
	specs *[]core.Spec
	path  string
	err   error
}

func (r stagingRunner) StageConfig(_ context.Context, spec core.Spec) (string, error) {
	*r.specs = append(*r.specs, spec)
	return r.path, r.err
}

// stagingCore is fakeCore with such a helper runner.
type stagingCore struct {
	*fakeCore
	runner stagingRunner
}

func (c stagingCore) HelperRunner() core.Runner { return c.runner }

// A core the helper runs loads nothing outside the helper's work
// directory: the reload names the copy the helper made, not runtime.yaml.
// A core Lanflow runs itself reloads runtime.yaml as before.
func TestReloadUsesHelperStagedConfig(t *testing.T) {
	h := newHarness(t, nil)
	var staged []core.Spec
	sc := stagingCore{fakeCore: h.core, runner: stagingRunner{namedRunner: namedRunner{"helper"}, specs: &staged, path: "/helper/work/runtime.yaml"}}
	h.s.d.core = sc
	var reloaded []string
	h.ctrl.mu.Lock()
	h.ctrl.reloadFn = func(path string) error { reloaded = append(reloaded, path); return nil }
	h.ctrl.mu.Unlock()
	setRunning := func(tun bool) {
		h.s.mu.Lock()
		h.s.state.st.Core = model.CoreRunning
		h.s.state.tunRunner = tun
		h.s.mu.Unlock()
	}
	reload := func() error {
		t.Helper()
		h.s.opMu.Lock()
		defer h.s.opMu.Unlock()
		return h.s.reloadLocked()
	}

	// Run by Lanflow itself.
	setRunning(false)
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 || len(reloaded) != 1 || reloaded[0] != h.s.runtime {
		t.Fatalf("exec runner: staged %v, reloaded %v", staged, reloaded)
	}

	// Run by the helper.
	setRunning(true)
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 || staged[0].Config != h.s.runtime || staged[0].WorkDir != h.s.workDir {
		t.Fatalf("staged %+v", staged)
	}
	if len(reloaded) != 2 || reloaded[1] != "/helper/work/runtime.yaml" {
		t.Fatalf("helper runner reloaded %v", reloaded)
	}

	// The helper refuses the config (or is an older one without the op):
	// the core is not asked to reload and the previous config stays.
	sc.runner.err = errors.New("权限组件拒绝了这份配置")
	h.s.d.core = sc
	err := reload()
	if err == nil || !strings.Contains(err.Error(), "权限组件拒绝了这份配置") {
		t.Fatalf("reload with a refusing helper: %v", err)
	}
	if len(reloaded) != 2 {
		t.Fatalf("the core was asked to reload a refused config: %v", reloaded)
	}

	// The core refuses the staged config: runtime.yaml is rolled back and
	// the helper is asked to stage that one again, so its copy is not left
	// as the refused config.
	sc.runner.err = nil
	h.s.d.core = sc
	staged = nil
	var atStage []string
	h.ctrl.mu.Lock()
	h.ctrl.reloadFn = func(path string) error {
		b, _ := os.ReadFile(h.s.runtime)
		atStage = append(atStage, string(b))
		reloaded = append(reloaded, path)
		return errors.New("reload refused")
	}
	h.ctrl.mu.Unlock()
	before, _ := os.ReadFile(h.s.runtime)
	if err := h.s.updateSettings(func(set *model.Settings) { set.MixedPort++ }); err != nil {
		t.Fatal(err)
	}
	if err := reload(); err == nil {
		t.Fatal("a refused reload succeeded")
	}
	after, _ := os.ReadFile(h.s.runtime)
	if len(staged) != 2 || string(after) != string(before) || len(atStage) != 1 || atStage[0] == string(before) {
		t.Fatalf("after a refused reload: %d stage requests, runtime.yaml restored = %v", len(staged), string(after) == string(before))
	}
	h.ctrl.mu.Lock()
	h.ctrl.reloadFn = func(path string) error { reloaded = append(reloaded, path); return nil }
	h.ctrl.mu.Unlock()
	reloaded = reloaded[:2]

	// A helper from before the stage op (0.1.3, whose core the app took
	// over after an upgrade) runs the core on runtime.yaml itself.
	sc.runner.err = fmt.Errorf("%w: 未知操作", core.ErrStageUnsupported)
	h.s.d.core = sc
	if err := reload(); err != nil {
		t.Fatalf("reload through an old helper: %v", err)
	}
	if len(reloaded) != 3 || reloaded[2] != h.s.runtime {
		t.Fatalf("old helper reloaded %v", reloaded)
	}
	reloaded = reloaded[:2]

	// A helper runner that does not stage (the tests' plain fake) keeps
	// the old behaviour.
	h.s.d.core = h.core
	if err := reload(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 3 || reloaded[2] != h.s.runtime {
		t.Fatalf("plain helper runner reloaded %v", reloaded)
	}
}
