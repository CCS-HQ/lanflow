package service

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// A config Lanflow composes is what the helper sees on every TUN start:
// it must reach the core byte for byte.
func TestSanitizeConfigPassesComposedConfig(t *testing.T) {
	sub := `
external-controller: 0.0.0.0:9090
external-ui: ui
secret: hunter2
proxies:
  - {name: a, type: ss, server: example.com, port: 8388, cipher: aes-128-gcm, password: p}
proxy-providers:
  air:
    type: http
    url: https://example.com/sub
    path: ./providers/air.yaml
    interval: 3600
proxy-groups:
  - {name: PROXY, type: select, proxies: [a], use: [air]}
rule-providers:
  ads:
    type: http
    behavior: domain
    url: https://example.com/ads.yaml
    path: ruleset/ads.yaml
  nopath:
    type: http
    behavior: domain
    url: https://example.com/x.yaml
rules:
  - RULE-SET,ads,REJECT
  - MATCH,PROXY
`
	out, err := profile.Compose(profile.Input{
		Subscription: []byte(sub),
		Managed:      profile.Managed{MixedPort: 7890, Mode: model.ModeRule, TUN: true, TUNStack: "mixed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sanitizeConfig(out.YAML)
	if err != nil {
		t.Fatalf("a composed config was refused: %v\n%s", err, out.YAML)
	}
	if !bytes.Equal(got.YAML, out.YAML) || len(got.Removed) != 0 {
		t.Fatalf("a composed config was changed (removed %v)", got.Removed)
	}
	want := []string{filepath.FromSlash("providers/air.yaml"), filepath.FromSlash("ruleset/ads.yaml")}
	if strings.Join(got.ProviderPaths, ",") != strings.Join(want, ",") {
		t.Fatalf("provider paths = %v, want %v", got.ProviderPaths, want)
	}
}

func TestSanitizeConfigRemovesControllerAndUI(t *testing.T) {
	in := `
mixed-port: 7890
external-controller: 0.0.0.0:9090
External-Controller-TLS: 0.0.0.0:9443
external-controller-unix: /etc/cron.d/x
external-controller-pipe: \\.\pipe\x
external-ui: /etc
external-ui-url: https://example.com/ui.zip
external-ui-name: x
external-doh-server: /dns-query
secret: "0123"
mode: rule
rules:
  - MATCH,DIRECT
dns:
  nameserver-policy: {1: 2}
`
	got, err := sanitizeConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Removed) != 9 {
		t.Fatalf("removed = %v", got.Removed)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(got.YAML, &doc); err != nil {
		t.Fatalf("the cleaned config does not parse: %v\n%s", err, got.YAML)
	}
	for key := range doc {
		if strippedConfigKeys[strings.ToLower(key)] {
			t.Errorf("%s survived", key)
		}
	}
	if doc["mixed-port"] != 7890 || doc["mode"] != "rule" || doc["dns"] == nil {
		t.Fatalf("other keys were lost: %v", doc)
	}
	if rules, _ := doc["rules"].([]any); len(rules) != 1 || rules[0] != "MATCH,DIRECT" {
		t.Fatalf("rules = %v", doc["rules"])
	}
}

// A key that arrives through a top-level merge cannot be cut out of the
// document; it must not get past the check either.
func TestSanitizeConfigRefusesMergedKeys(t *testing.T) {
	for name, in := range map[string]string{
		"controller": "x-base: &base\n  external-controller: 0.0.0.0:9090\n<<: *base\nmixed-port: 7890\n",
		"in a list":  "x-a: &a {mode: rule}\nx-b: &b {external-ui: /etc}\n<<: [*a, *b]\n",
		"nested":     "x-a: &a {secret: x}\nx-b: &b {<<: *a}\n<<: *b\n",
		"ntp":        "x-n: &n {write-to-system: true}\nntp:\n  <<: *n\n  enable: true\n",
	} {
		if got, err := sanitizeConfig([]byte(in)); err == nil {
			t.Errorf("%s: accepted:\n%s", name, got.YAML)
		}
	}
	// A merge that brings nothing of the kind is fine and stays as written.
	in := "x-base: &base {mode: rule, proxy-providers: {p: {type: http, url: 'https://e/x', path: a/b.yaml}}}\n<<: *base\nmixed-port: 7890\n"
	got, err := sanitizeConfig([]byte(in))
	if err != nil || string(got.YAML) != in || len(got.ProviderPaths) != 1 {
		t.Fatalf("harmless merge: %v %v\n%s", err, got.ProviderPaths, got.YAML)
	}
}

// ntp.write-to-system lets the config set the system clock through a core
// that runs as root.
func TestSanitizeConfigRemovesNTPWriteToSystem(t *testing.T) {
	in := "mixed-port: 7890\nntp:\n  enable: true\n  write-to-system: true\n  server: time.example.com\n"
	got, err := sanitizeConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(got.YAML, &doc); err != nil {
		t.Fatal(err)
	}
	ntp, _ := doc["ntp"].(map[string]any)
	if _, ok := ntp["write-to-system"]; ok || ntp["enable"] != true || ntp["server"] != "time.example.com" {
		t.Fatalf("ntp = %v", ntp)
	}
	if strings.Join(got.Removed, ",") != "ntp.write-to-system" {
		t.Fatalf("removed = %v", got.Removed)
	}
	// Without the key the section passes untouched.
	in = "ntp:\n  enable: true\n  server: time.example.com\n"
	if got, err := sanitizeConfig([]byte(in)); err != nil || string(got.YAML) != in {
		t.Fatalf("plain ntp: %v\n%s", err, got.YAML)
	}
}

// A provider file must not be, or lie below, one of the helper's own
// entries of the work directory: as the config's name it would be copied
// over the cleaned config; below a database it would turn that into a
// directory only root could remove.
func TestSanitizeConfigRefusesReservedProviderPaths(t *testing.T) {
	for _, p := range []string{
		"runtime.yaml", "RUNTIME.YAML", "./x/../runtime.yaml", "runtime.yaml/x",
		"geoip.metadb", "geoip.metadb/x", "GeoIP.dat", "GeoSite.dat/sub/x", "Country.mmdb", "ASN.mmdb", "GeoLite2-ASN.mmdb",
		"cache.db", "Cache.DB/x", "proxies", "./rules", ".lanflow-0011", ".lanflow-x/y",
	} {
		for _, section := range []string{"proxy-providers", "rule-providers"} {
			in := section + ":\n  p:\n    type: http\n    url: https://example.com/x\n    path: '" + p + "'\n"
			if got, err := sanitizeConfig([]byte(in)); err == nil {
				t.Errorf("%s path %q accepted (%v)", section, p, got.ProviderPaths)
			}
		}
	}
	in := "proxy-providers:\n  a: {type: http, url: 'https://e/a', path: proxies/a.yaml}\n  b: {type: http, url: 'https://e/b', path: rules/b.yaml}\n  c: {type: http, url: 'https://e/c', path: my-runtime.yaml}\n"
	if got, err := sanitizeConfig([]byte(in)); err != nil || len(got.ProviderPaths) != 3 {
		t.Fatalf("ordinary paths: %v %v", err, got.ProviderPaths)
	}
}

func largeSanitizeConfig(keys, providers int) []byte {
	var b strings.Builder
	for i := 0; i < keys; i++ {
		fmt.Fprintf(&b, "k%d: %d\n", i, i)
	}
	b.WriteString("external-controller: 0.0.0.0:9090\nproxy-providers:\n")
	for i := 0; i < providers; i++ {
		fmt.Fprintf(&b, "  p%d: {type: http, url: 'https://e/%d', path: p/%d.yaml}\n", i, i, i)
	}
	return []byte(b.String())
}

func TestSanitizeConfigLargeMapping(t *testing.T) {
	in := largeSanitizeConfig(80000, 10000)
	if len(in) > maxConfigSize {
		t.Fatalf("test config is %d bytes", len(in))
	}
	// Keep the large input's correctness coverage under -race without
	// applying the production deadline to instrumented code on a shared
	// CI runner. BenchmarkSanitizeConfig measures its scaling separately.
	got, err := sanitizeNow(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ProviderPaths) != 10000 || len(got.Removed) != 1 || bytes.Contains(got.YAML, []byte("external-controller")) {
		t.Fatalf("paths %d, removed %v", len(got.ProviderPaths), got.Removed)
	}
}

func TestSanitizeConfigRefusesMergeBomb(t *testing.T) {
	// Merges nested so that following them all would never end.
	var b strings.Builder
	b.WriteString("a0: &a0 {path: x.yaml}\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "a%d: &a%d {<<: [*a%d, *a%d, *a%d, *a%d]}\n", i, i, i-1, i-1, i-1, i-1)
	}
	b.WriteString("proxy-providers:\n  p: {<<: *a30}\n")
	// Require the traversal budget to reject it, rather than accepting
	// any error (including the outer wall-clock deadline) as success.
	if _, err := sanitizeNow([]byte(b.String())); !errors.Is(err, errTooComplex) {
		t.Fatalf("merge bomb: got %v, want %v", err, errTooComplex)
	}
}

// Comparing sizes exposes quadratic YAML map decoding without making
// ordinary tests depend on the speed or load of the runner.
func BenchmarkSanitizeConfig(b *testing.B) {
	for _, providers := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("keys=%d", 9*providers), func(b *testing.B) {
			in := largeSanitizeConfig(8*providers, providers)
			b.SetBytes(int64(len(in)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := sanitizeNow(in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSanitizeConfigRefuses(t *testing.T) {
	provider := func(section, path string) string {
		return section + ":\n  p:\n    type: http\n    url: https://example.com/x\n    path: " + path + "\n"
	}
	cases := map[string]string{
		"absolute provider path":    provider("proxy-providers", "/etc/cron.d/x"),
		"absolute rule path":        provider("rule-providers", "/etc/cron.d/x"),
		"parent directory":          provider("rule-providers", "../../etc/x"),
		"parent inside":             provider("proxy-providers", "a/../../x"),
		"the work dir itself":       provider("proxy-providers", "./"),
		"path is not a string":      provider("proxy-providers", "[a, b]"),
		"path through a merge":      "x: &p {type: http, url: 'https://example.com/x', path: /etc/x}\nproxy-providers:\n  p:\n    <<: *p\n",
		"path of a numeric name":    "rule-providers:\n  1:\n    path: /etc/x\n",
		"not YAML":                  "a: [1, 2\n",
		"not a mapping":             "- a\n- b\n",
		"empty":                     "",
		"duplicate top-level key":   "mixed-port: 1\nmixed-port: 2\n",
		"duplicate key in provider": "proxy-providers:\n  p:\n    path: a\n    path: /etc/x\n",
	}
	for name, in := range cases {
		if got, err := sanitizeConfig([]byte(in)); err == nil {
			t.Errorf("%s: accepted (%q, paths %v)", name, got.YAML, got.ProviderPaths)
		}
	}
	if _, err := sanitizeConfig(bytes.Repeat([]byte("#"), maxConfigSize+1)); err == nil {
		t.Error("an oversized config was accepted")
	}
}

func TestSanitizeConfigAcceptsLocalProviderPaths(t *testing.T) {
	in := "proxy-providers:\n  a: {type: http, url: 'https://e/x', path: ./p/a.yaml}\n  b: {type: file, path: b.yaml}\n  c: {type: http, url: 'https://e/y'}\n  d: {type: http, url: 'https://e/z', path: ''}\n" +
		"rule-providers:\n  r: {type: http, behavior: domain, url: 'https://e/r', path: p//a.yaml}\n  i: {type: inline, behavior: domain, payload: [a.com]}\n"
	got, err := sanitizeConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got.YAML) != in {
		t.Fatal("config changed")
	}
	if want := "b.yaml," + filepath.FromSlash("p/a.yaml"); strings.Join(got.ProviderPaths, ",") != want {
		t.Fatalf("provider paths = %v, want %s", got.ProviderPaths, want)
	}
}
