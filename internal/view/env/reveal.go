package env

import (
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// Reveal brings a section or row of a page into view after a jump from
// the global search (⌘K): the search asks for it by its label with
// RequestReveal and navigates; the shared Section and Row of the kit
// call Reveal for every label they draw, and the one asked for scrolls
// to the middle of its page and is highlighted for a moment.
//
// There is one window per process, so the request is kept here rather
// than threaded through every page.

// revealHighlight is how long a revealed row stays highlighted.
const revealHighlight = 1800 * time.Millisecond

var reveal struct {
	mu      sync.Mutex
	pending string    // label to bring into view on its next draw
	asked   time.Time // when pending was asked; a request nobody drew expires
	shown   string    // label highlighted
	at      time.Time // when shown came into view
}

// RequestReveal asks the next Reveal of label (a label as drawn, that
// is translated) to bring its element into view.
func RequestReveal(label string) {
	reveal.mu.Lock()
	reveal.pending, reveal.asked = strings.TrimSpace(label), time.Now()
	reveal.mu.Unlock()
}

// Reveal scrolls el, drawn under label, into view when label was asked
// for, and reports whether el should be drawn highlighted.
func Reveal(el ui.Element, label string) bool {
	label = strings.TrimSpace(label)
	if label == "" {
		return false
	}
	reveal.mu.Lock()
	defer reveal.mu.Unlock()
	now := time.Now()
	if reveal.pending != "" && now.Sub(reveal.asked) > 5*time.Second {
		reveal.pending = "" // the page never drew it (a row hidden in this state)
	}
	if reveal.pending == label {
		reveal.pending = ""
		reveal.shown, reveal.at = label, now
		el.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollCenter, Animation: &ui.Smooth})
	}
	return reveal.shown == label && now.Sub(reveal.at) < revealHighlight
}

// RevealPending returns the label waiting to be revealed ("" = none),
// for tests.
func RevealPending() string {
	reveal.mu.Lock()
	defer reveal.mu.Unlock()
	return reveal.pending
}

// QueryArg is the page argument that opens a page filtered by query, as
// the global search's "在…中搜索" items do; the pages with a search field
// read it with TakeQuery.
func QueryArg(query string) string { return "q:" + query }

// TakeQuery returns the query of a QueryArg argument.
func TakeQuery(arg string) (string, bool) { return strings.CutPrefix(arg, "q:") }
