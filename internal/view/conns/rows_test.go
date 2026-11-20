package conns

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// TestIsIP: only addresses are IPs; names made of hex letters are names.
func TestIsIP(t *testing.T) {
	for _, c := range []struct {
		host string // as a connection has it
		only string // hostOnly
		ip   bool
	}{
		{"1.2.3.4:443", "1.2.3.4", true},
		{"1.2.3.4", "1.2.3.4", true},
		{"[2001:db8::1]:443", "2001:db8::1", true},
		{"2001:db8::1", "2001:db8::1", true},
		{"[::1]", "::1", true},
		{"db.de:443", "db.de", false},
		{"bbc.de", "bbc.de", false},
		{"fb.cc:80", "fb.cc", false},
		{"cafe.be:443", "cafe.be", false},
		{"www.google.com:443", "www.google.com", false},
		{"1.2.3:80", "1.2.3", false},
		{"1.2.3.4.5", "1.2.3.4.5", false},
		{"localhost:8080", "localhost", false},
		{"", "", false},
	} {
		h := hostOnly(c.host)
		if h != c.only {
			t.Errorf("hostOnly(%q) = %q, want %q", c.host, h, c.only)
		}
		if got := isIP(h); got != c.ip {
			t.Errorf("isIP(%q) = %v, want %v", h, got, c.ip)
		}
	}
}

// TestAddRuleForHexDomain: a domain of hex letters prefills a domain rule,
// not an IP-CIDR one with the name as its payload.
func TestAddRuleForHexDomain(t *testing.T) {
	e, _ := testEnv(fake.NewInstant())
	p := New(e)
	p.addRuleForHost(model.Connection{Host: "db.de:443"})
	if r := p.form.Rule(); r.Type != "DOMAIN-SUFFIX" || r.Payload != "db.de" {
		t.Fatalf("rule for db.de: %+v", r)
	}
	p.addRuleForHost(model.Connection{Host: "8.8.8.8:53"})
	if r := p.form.Rule(); r.Type != "IP-CIDR" || r.Payload != "8.8.8.8/32" {
		t.Fatalf("rule for 8.8.8.8: %+v", r)
	}
}

func ids(rows []model.Connection) string {
	s := ""
	for _, r := range rows {
		s += r.ID
	}
	return s
}

// TestSortRows: every column both ways, newest first among equals, and
// connections started together equal by duration.
func TestSortRows(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	rows := []model.Connection{
		{ID: "a", Host: "b.com", Process: "x", Rule: "DomainSuffix", Chains: []string{"n2", "g"}, DownSpeed: 5, UpSpeed: 1, Download: 10, Start: t0},
		{ID: "b", Host: "a.com", Process: "z", Rule: "Match", Chains: []string{"n1"}, DownSpeed: 5, UpSpeed: 3, Download: 30, Start: t0.Add(time.Minute)},
		{ID: "c", Host: "c.com", Process: "y", Rule: "GeoIP", Chains: nil, DownSpeed: 9, UpSpeed: 2, Download: 20, Start: t0.Add(time.Minute)},
		{ID: "d", Host: "d.com", Process: "w", Rule: "", DownSpeed: 0, UpSpeed: 0, Download: 0, Start: t0.Add(-time.Minute), ClosedAt: t0.Add(4 * time.Minute)},
	}
	for _, c := range []struct {
		col  string
		desc bool
		want string
	}{
		{"host", false, "bacd"}, {"host", true, "dcab"},
		{"process", false, "dacb"},
		{"rule", false, "acbd"},  // DOMAIN-SUFFIX, GEO-IP, MATCH, —
		{"chain", false, "bacd"}, // n1, n2 › g, then none: newest first
		{"down", true, "cbad"},   // a and b are equal: b is newer
		{"down", false, "dbac"},
		{"up", true, "bcad"},
		{"total", true, "bcad"},
		// d closed after 5 minutes; a runs for an hour, b and c a minute
		// less, equal to each other whichever is compared first.
		{"dur", false, "dbca"}, {"dur", true, "abcd"},
	} {
		if got := ids(sortRows(rows, ui.SortOrder{Column: c.col, Descending: c.desc})); got != c.want {
			t.Errorf("by %s desc=%v: %s, want %s", c.col, c.desc, got, c.want)
		}
	}
	if ids(rows) != "abcd" {
		t.Errorf("sortRows moved its argument: %s", ids(rows))
	}
}

// TestRowsKeptBetweenChanges: a build for anything but new connections
// reads, filters and sorts nothing again; a change of the search, the
// order or the data does.
func TestRowsKeptBetweenChanges(t *testing.T) {
	f := fake.NewInstant()
	api := &countConns{Fake: f}
	e, _ := testEnv(f)
	e.API = api
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	tt.Frame()
	reads := api.active
	rows := p.sorted.rows
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	tt.Frame()
	tt.Move(300, 300)
	tt.Frame()
	if api.active != reads || &p.sorted.rows[0] != &rows[0] {
		t.Fatalf("read %d more times, rows made again: %v", api.active-reads, &p.sorted.rows[0] != &rows[0])
	}
	p.sort = ui.SortOrder{Column: "host"}
	tt.Frame()
	if api.active != reads || p.sorted.sort.Column != "host" || p.sorted.rows[0].Host > p.sorted.rows[1].Host {
		t.Fatalf("new order: reads %d, rows %s", api.active-reads, ids(p.sorted.rows))
	}
	p.query = "telegram"
	tt.Frame()
	if len(p.sorted.rows) == 0 || len(p.sorted.rows) >= len(rows) {
		t.Fatalf("search kept %d of %d rows", len(p.sorted.rows), len(rows))
	}
	p.query = ""
	// The connections change: the lists are read again.
	if err := f.CloseConnection(rows[0].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if api.active == reads || len(p.sorted.rows) != len(rows)-1 {
		t.Fatalf("after a change: %d more reads, %d rows (were %d)", api.active-reads, len(p.sorted.rows), len(rows))
	}
}

// TestPausedDropsConnTopic: paused, the page does not redraw for the
// connection stream.
func TestPausedDropsConnTopic(t *testing.T) {
	e, _ := testEnv(fake.NewInstant())
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if !p.Wants(app.TopicConns) {
		t.Fatal("running, the page wants TopicConns")
	}
	p.paused = true
	tt.Frame()
	if p.Wants(app.TopicConns) || !p.Wants(app.TopicStatus) {
		t.Fatal("paused, only TopicConns is dropped")
	}
	p.paused = false
	tt.Frame()
	if !p.Wants(app.TopicConns) {
		t.Fatal("resumed, the page wants TopicConns again")
	}
}

// TestPluralActiveConnections: English says "1 active connection".
func TestPluralActiveConnections(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	i18n.SetLang(i18n.EN)
	if got := i18n.Tn(1, "%d 个活动连接", 1); got != "1 active connection" {
		t.Errorf("one: %q", got)
	}
	if got := i18n.Tn(2, "%d 个活动连接", 2); got != "2 active connections" {
		t.Errorf("two: %q", got)
	}
	if got := i18n.Tn(1, "断开全部 %d 个连接？", 1); got != "Close the 1 connection?" {
		t.Errorf("close one: %q", got)
	}
	if got := topoStats(1, 0, 0, 0); got[:12] != "1 connection" || got[12] == 's' {
		t.Errorf("topoStats: %q", got)
	}
	i18n.SetLang(i18n.ZH)
	if got := i18n.Tn(1, "%d 个活动连接", 1); got != "1 个活动连接" {
		t.Errorf("Chinese: %q", got)
	}
}
