package app

import (
	"testing"

	"lanflow/internal/model"
)

// Settings from before TraySpeed keep their choice; the old flag follows
// the new field.
func TestNormalizeTraySpeed(t *testing.T) {
	for _, c := range []struct {
		in   model.Settings
		want string
		hide bool
	}{
		{model.Settings{}, model.TraySpeedBoth, false},
		{model.Settings{HideTraySpeed: true}, model.TraySpeedOff, true},
		{model.Settings{TraySpeed: model.TraySpeedDown, HideTraySpeed: true}, model.TraySpeedDown, false},
		{model.Settings{TraySpeed: "bogus"}, model.TraySpeedBoth, false},
	} {
		got := normalizeSettings(c.in)
		if got.TraySpeed != c.want || got.HideTraySpeed != c.hide {
			t.Errorf("%+v → %q hide=%v", c.in.TraySpeed, got.TraySpeed, got.HideTraySpeed)
		}
	}
	if defaultSettings().TraySpeed != model.TraySpeedBoth {
		t.Error("default")
	}
}

// The core's stacks are kept, mips included; anything else falls back to
// the default.
func TestNormalizeTUNStack(t *testing.T) {
	for in, want := range map[string]string{"mixed": "mixed", "system": "system", "gvisor": "gvisor", "mips": "mips", "lwip": "mixed", "": "mixed"} {
		if got := normalizeSettings(model.Settings{TUNStack: in}).TUNStack; got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

// mips reaches the config only on a core that knows it.
func TestComposeTUNStack(t *testing.T) {
	for _, c := range []struct{ stack, core, want string }{
		{"mips", "", "mips"},
		{"mips", "v1.19.31", "mips"},
		{"mips", "v1.19.30", "mixed"},
		{"mips", "alpha-1a2b3c4", "mips"},
		{"gvisor", "v1.18.0", "gvisor"},
	} {
		if got := composeTUNStack(c.stack, c.core); got != c.want {
			t.Errorf("%s on %q → %q, want %q", c.stack, c.core, got, c.want)
		}
	}
}
