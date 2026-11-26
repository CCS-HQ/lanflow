package settings

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/view/env"
)

// TestSearchIndexMatchesTabs keeps the global search's settings index in
// step with the tabs: every section and row it lists is drawn on its tab.
func TestSearchIndexMatchesTabs(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 4000)
	byTab := map[string][]SearchEntry{}
	for _, en := range SearchIndex() {
		byTab[en.Tab] = append(byTab[en.Tab], en)
	}
	for _, tab := range tabKeys {
		if len(byTab[tab]) == 0 {
			t.Errorf("tab %s has nothing in the search index", tab)
		}
		r.arg = tab
		tt.Frame()
		tt.Frame()
		for _, en := range byTab[tab] {
			// A row drawn only in some states reveals its section, which
			// must be drawn.
			if !tt.HasText(en.Reveal) {
				t.Errorf("%s: %q is in the search index but not drawn", tab, en.Label)
			}
		}
	}
}

func TestOpenEntryRevealsRow(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	r.arg = "advanced#代理认证"
	tt.Frame()
	if p.Tab() != "advanced" {
		t.Fatalf("tab %s", p.Tab())
	}
	tt.Frame()
	if pend := env.RevealPending(); pend != "" {
		t.Fatalf("row not revealed, %q pending", pend)
	}
}
