package locate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// realDB returns one of the databases another component downloads into
// bin/geo; the test is skipped without it.
func realDB(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bin", "geo", name))
	if err != nil {
		t.Skipf("bin/geo/%s not present", name)
	}
	return b
}

// The databases are the files the core updates: a file that appears late,
// one that is half written and one that is replaced are all picked up at
// the next check, and a failed open is never remembered as "opened".
func TestDatabaseReload(t *testing.T) {
	geoDB, asnDB := realDB(t, "geoip.metadb"), realDB(t, "GeoLite2-ASN.mmdb")
	path := filepath.Join(t.TempDir(), "geoip.metadb")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l := New("", path, "")
	l.l.now = func() time.Time { return now }
	country := func() string {
		if ep := l.Lookup("1.1.1.1"); ep.Place != nil {
			return ep.Place.Country
		}
		return ""
	}
	write := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		// a different time even on file systems with coarse timestamps
		mt := now.Add(time.Duration(len(data)) * time.Second)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	steps := []struct {
		name string
		prep func()
		wait time.Duration
		want string
	}{
		{"missing file", func() {}, 0, ""},
		{"half written file", func() { write(geoDB[:len(geoDB)/2]) }, dbCheckEvery, ""},
		{"complete file, before the next check", func() { write(geoDB) }, dbCheckEvery / 2, ""},
		{"complete file", func() {}, dbCheckEvery, "AU"},
		{"replaced by another database", func() { write(asnDB) }, dbCheckEvery, ""},
		{"file removed: the last good one stays", func() { os.Remove(path) }, dbCheckEvery, ""},
		{"back again", func() { write(geoDB) }, dbCheckEvery, "AU"},
		{"removed again", func() { os.Remove(path) }, dbCheckEvery, "AU"},
	}
	for _, st := range steps {
		st.prep()
		now = now.Add(st.wait)
		if got := country(); got != st.want {
			t.Fatalf("%s: country %q, want %q", st.name, got, st.want)
		}
	}
}

// Results stored in quick succession are written once, when the interval
// is over; the first one is written at once so its error is reported.
func TestCacheWritesAreCoalesced(t *testing.T) {
	ts := newTraceServer(t, traceBody("203.0.113.9", "JP", "NRT"))
	cache := filepath.Join(t.TempDir(), "exits.json")
	l, now, _ := newTestLocator(t, ts, cache)
	l.l.flushEvery = time.Hour // the timer never fires in this test
	sel := func(context.Context, string) error { return nil }
	onDisk := func() int {
		data, _ := os.ReadFile(cache)
		var m map[string]cacheEntry
		json.Unmarshal(data, &m)
		return len(m)
	}
	for i, node := range []string{"HK 01", "HK 02", "HK 03"} {
		if _, err := l.Probe(context.Background(), sel, 1, node); err != nil {
			t.Fatal(err)
		}
		if n := onDisk(); n != 1 {
			t.Fatalf("after probe %d: %d entries on disk, want the first only", i+1, n)
		}
		if g := l.Guess(node); g.Colo != "NRT" {
			t.Fatalf("%s not answered from memory: %+v", node, g)
		}
	}
	l.l.mu.Lock()
	pending := l.l.flushTimer != nil
	l.l.mu.Unlock()
	if !pending {
		t.Fatal("no write scheduled for the results held back")
	}
	if err := l.l.flush(); err != nil { // what the timer does
		t.Fatal(err)
	}
	if n := onDisk(); n != 3 {
		t.Fatalf("after the flush: %d entries on disk", n)
	}
	// past the interval a result is written at once again
	*now = now.Add(2 * time.Hour)
	if _, err := l.Probe(context.Background(), sel, 1, "HK 04"); err != nil {
		t.Fatal(err)
	}
	if n := onDisk(); n != 4 {
		t.Fatalf("after the interval: %d entries on disk", n)
	}

	// Reset forgets them, in memory and on disk.
	if err := l.Reset(); err != nil {
		t.Fatal(err)
	}
	if n := onDisk(); n != 0 {
		t.Fatalf("after Reset: %d entries on disk", n)
	}
	if g := l.Guess("HK 01"); g.Colo != "" {
		t.Fatalf("after Reset: %+v", g)
	}
}
