package sysproxy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeGSettings keeps the GVariant text of every key, as dconf would, and
// answers gsettings get/set; unset keys read as GNOME's defaults.
type fakeGSettings struct {
	keys  map[string]string
	sets  []string
	fail  error
	extra string // printed before every value (GLib warnings)
}

func newFakeGSettings() *fakeGSettings {
	f := &fakeGSettings{keys: map[string]string{
		gnomeSchema + " mode":           "'none'",
		gnomeSchema + " autoconfig-url": "''",
		gnomeSchema + " ignore-hosts":   "['localhost', '127.0.0.0/8', '::1']",
	}}
	for _, p := range gnomeProxies {
		f.keys[gnomeSchema+"."+p+" host"] = "''"
		f.keys[gnomeSchema+"."+p+" port"] = "0"
	}
	return f
}

func (f *fakeGSettings) run(_ context.Context, name string, args ...string) (string, error) {
	if name != gsettings || len(args) < 3 {
		return "", errors.New("unexpected command " + name)
	}
	if f.fail != nil {
		return "", f.fail
	}
	key := args[1] + " " + args[2]
	switch args[0] {
	case "get":
		v, ok := f.keys[key]
		if !ok {
			return "", errors.New("No such key")
		}
		return f.extra + v + "\n", nil
	case "set":
		f.keys[key] = args[3]
		f.sets = append(f.sets, key+" "+args[3])
		return f.extra, nil
	}
	return "", errors.New("unexpected gsettings " + args[0])
}

func TestGnomeSetGetClear(t *testing.T) {
	ctx := context.Background()
	f := newFakeGSettings()
	if err := gnomeSet(ctx, f.run, "127.0.0.1", 7890, DefaultBypass); err != nil {
		t.Fatal(err)
	}
	if last := f.sets[len(f.sets)-1]; last != gnomeSchema+" mode 'manual'" {
		t.Fatalf("mode not written last: %q", last)
	}
	if got := f.keys[gnomeSchema+" ignore-hosts"]; strings.Contains(got, "<local>") || !strings.Contains(got, "'192.168.0.0/16'") {
		t.Fatalf("ignore-hosts = %s", got)
	}
	st, err := gnomeGet(ctx, f.run)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Host != "127.0.0.1" || st.Port != 7890 || st.PACURL != "" || len(st.Bypass) == 0 {
		t.Fatalf("Get = %+v", st)
	}
	if err := gnomeClear(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	if st, _ := gnomeGet(ctx, f.run); st.Enabled {
		t.Fatalf("still enabled after Clear: %+v", st)
	}
}

func TestGnomeClearLeavesPAC(t *testing.T) {
	ctx := context.Background()
	f := newFakeGSettings()
	if err := gnomeSetPAC(ctx, f.run, "http://127.0.0.1:11233/proxy.pac"); err != nil {
		t.Fatal(err)
	}
	if err := gnomeClear(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	st, _ := gnomeGet(ctx, f.run)
	if st.PACURL != "http://127.0.0.1:11233/proxy.pac" || st.Enabled {
		t.Fatalf("Clear touched PAC: %+v", st)
	}
	if err := gnomeSetPAC(ctx, f.run, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := gnomeGet(ctx, f.run); st.PACURL != "" {
		t.Fatalf("PAC still on: %+v", st)
	}
	// SetPAC("") leaves a manual proxy alone.
	if err := gnomeSet(ctx, f.run, "127.0.0.1", 7890, nil); err != nil {
		t.Fatal(err)
	}
	if err := gnomeSetPAC(ctx, f.run, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := gnomeGet(ctx, f.run); !st.Enabled {
		t.Fatalf("SetPAC(\"\") turned off the manual proxy: %+v", st)
	}
}

func TestGnomeSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	f := newFakeGSettings()
	// Another app's proxy, with a quote in a bypass entry.
	f.keys[gnomeSchema+" mode"] = "'manual'"
	f.keys[gnomeSchema+".http host"] = "'proxy.corp'"
	f.keys[gnomeSchema+".http port"] = "3128"
	f.keys[gnomeSchema+" ignore-hosts"] = `['localhost', "it's.corp"]`
	saved, err := gnomeSnapshot(ctx, f.run)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for k, v := range f.keys {
		before[k] = v
	}
	if err := gnomeSet(ctx, f.run, "127.0.0.1", 7890, DefaultBypass); err != nil {
		t.Fatal(err)
	}
	if err := gnomeRestore(ctx, f.run, saved); err != nil {
		t.Fatal(err)
	}
	st, _ := gnomeGet(ctx, f.run)
	if st.Host != "proxy.corp" || st.Port != 3128 || !reflect.DeepEqual(st.Bypass, []string{"localhost", "it's.corp"}) {
		t.Fatalf("after Restore: %+v", st)
	}
	if got := f.keys[gnomeSchema+".https host"]; got != before[gnomeSchema+".https host"] {
		t.Fatalf("https host = %s, want %s", got, before[gnomeSchema+".https host"])
	}
	if err := gnomeRestore(ctx, f.run, Saved(`{"os":"darwin"}`)); err == nil {
		t.Fatal("restored a snapshot of another system")
	}
}

func TestGnomeErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeGSettings()
	f.fail = errors.New(`exec: "gsettings": executable file not found in $PATH`)
	if err := gnomeSet(ctx, f.run, "127.0.0.1", 7890, nil); err == nil || !strings.Contains(err.Error(), "没有找到 gsettings") {
		t.Fatalf("no gsettings: %v", err)
	}
	f.fail = errors.New("No such schema “org.gnome.system.proxy”")
	if _, err := gnomeGet(ctx, f.run); err == nil || !strings.Contains(err.Error(), "org.gnome.system.proxy") {
		t.Fatalf("no schema: %v", err)
	}
	f.fail = nil
	f.extra = "(gsettings:42): GLib-GIO-WARNING **: Using the 'memory' GSettings backend.  Your settings will not be saved or shared with other applications.\n"
	if err := gnomeSet(ctx, f.run, "127.0.0.1", 7890, nil); err == nil || !strings.Contains(err.Error(), "dconf") {
		t.Fatalf("memory backend: %v", err)
	}
	if err := gnomeSet(ctx, f.run, "", 7890, nil); err == nil {
		t.Fatal("accepted an empty host")
	}
}

func TestParseGVStrings(t *testing.T) {
	for in, want := range map[string][]string{
		"'manual'":             {"manual"},
		"@as []":               nil,
		`['a', "b'c", 'd\'e']`: {"a", "b'c", "d'e"},
		`['back\\slash']`:      {`back\slash`},
		"['localhost', '::1']": {"localhost", "::1"},
	} {
		if got := parseGVStrings(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseGVStrings(%s) = %q, want %q", in, got, want)
		}
	}
	if got := gvStrings([]string{"it's", `a\b`}); got != `['it\'s', 'a\\b']` {
		t.Errorf("gvStrings = %s", got)
	}
	if got := parseGVStrings(gvStrings([]string{"it's", `a\b`})); !reflect.DeepEqual(got, []string{"it's", `a\b`}) {
		t.Errorf("round trip = %q", got)
	}
}
