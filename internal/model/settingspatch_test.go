package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestMergeSettings(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stored := Settings{MixedPort: 7890, Mode: ModeRule, LogLevel: "info", Bypass: []string{"localhost"}}
	stored.DNS.Nameserver = []string{"1.1.1.1"}
	stored.Geo.UpdatedAt = at

	// The writer's snapshot is older than what is stored now.
	base := stored
	cur := stored
	cur.Mode = ModeGlobal           // another writer, a plain option
	cur.DNS.TakeOver = true         // another option of a section the writer changes too
	cur.Sites = []Site{{Name: "a"}} // a list the writer leaves alone
	cur.Geo.UpdatedAt = at.Add(time.Hour)

	next := base
	next.LogLevel = "debug"
	next.DNS.Nameserver = []string{"8.8.8.8"}
	next.Bypass = nil

	got := MergeSettings(cur, base, next)
	want := cur
	want.LogLevel = "debug"
	want.DNS.Nameserver = []string{"8.8.8.8"}
	want.Bypass = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged\n%+v\nwant\n%+v", got, want)
	}
	if ch := SettingsChanged(base, next); !reflect.DeepEqual(ch, []string{"Bypass", "LogLevel", "DNS.Nameserver"}) {
		t.Fatalf("changed = %v", ch)
	}
	// The result shares no list with next.
	next.DNS.Nameserver[0] = "changed"
	if got.DNS.Nameserver[0] != "8.8.8.8" {
		t.Fatal("the merged settings share a list with next")
	}
	// Nothing changed: nothing merged, whatever is stored.
	if got := MergeSettings(cur, base, base); !reflect.DeepEqual(got, cur) {
		t.Fatalf("an empty change altered the settings: %+v", got)
	}
	// A nil list and an empty one are the same option value.
	empty := base
	empty.Sites = []Site{}
	if ch := SettingsChanged(base, empty); len(ch) != 0 {
		t.Fatalf("nil → empty list counts as a change: %v", ch)
	}
}

// TestMergeSettingsOverJSON: base and next reach the service as JSON
// (internal/rpc). What the writer did not touch must still compare equal
// after the trip, times included, or every save would carry them along.
func TestMergeSettingsOverJSON(t *testing.T) {
	loc := time.FixedZone("x", 8*3600)
	base := Settings{MixedPort: 7890, Sites: []Site{{Name: "a", URL: "https://a"}}}
	base.Geo.UpdatedAt = time.Date(2026, 1, 2, 3, 4, 5, 6, loc)
	base.AgentGrants = []AgentGrant{{Agent: "x", Allow: true, At: time.Now()}}
	base.WebDAV.LastBackupAt = time.Now()
	next := CloneSettings(base)
	next.TrayNode = true

	trip := func(s Settings) (out Settings) {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if ch := SettingsChanged(trip(base), trip(next)); !reflect.DeepEqual(ch, []string{"TrayNode"}) {
		t.Fatalf("changed after the JSON trip = %v, want TrayNode alone", ch)
	}
}

func TestCloneSettings(t *testing.T) {
	s := Settings{Bypass: []string{"a"}, Sites: []Site{{Name: "n"}}}
	s.DNS.Policy = []KV{{Key: "k", Value: "v"}}
	s.Geo.UpdatedAt = time.Now()
	c := CloneSettings(s)
	if !reflect.DeepEqual(c, s) {
		t.Fatalf("clone differs: %+v", c)
	}
	c.Bypass[0], c.Sites[0].Name, c.DNS.Policy[0].Value = "b", "m", "w"
	if s.Bypass[0] != "a" || s.Sites[0].Name != "n" || s.DNS.Policy[0].Value != "v" {
		t.Fatal("the clone shares a list with the original")
	}
}

func TestRevertSettings(t *testing.T) {
	old := Settings{Mode: ModeRule, LogLevel: "info", MixedPort: 7890, Bypass: []string{"a", "b"}}
	old.Advanced.HTTPPort = 8080
	wrote := CloneSettings(old)
	wrote.Mode, wrote.LogLevel, wrote.Bypass = ModeGlobal, "debug", []string{"x"}
	wrote.Advanced.HTTPPort = 0
	wrote.TrayNode = true // changed beside the write, by someone else

	cur := CloneSettings(wrote)
	cur.Mode = ModeDirect // the user changed what the write set
	cur.MixedPort = 7777  // and something the write left alone

	next, skipped := RevertSettings(cur, wrote, old, func(p string) bool { return p != "TrayNode" })
	if !reflect.DeepEqual(skipped, []string{"Mode"}) {
		t.Fatalf("skipped = %v, want Mode", skipped)
	}
	got := MergeSettings(cur, wrote, next)
	want := CloneSettings(cur)
	want.LogLevel, want.Bypass = "info", []string{"a", "b"}
	want.Advanced.HTTPPort = 8080
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reverted\n%+v\nwant\n%+v", got, want)
	}
	if len(SettingsPaths()) < 80 || SettingsPaths()[0] != "OpenAtLogin" {
		t.Fatalf("paths: %v", SettingsPaths())
	}
}
