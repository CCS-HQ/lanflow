package applog

import (
	"strings"
	"testing"
	"time"
)

// TestRedactLongLine: a long text (a core log tail, a dumped
// configuration) is redacted in time that grows with its length, not
// faster (the limit is generous: the race detector slows it a lot).
func TestRedactLongLine(t *testing.T) {
	for name, in := range map[string]string{
		"one word": strings.Repeat("x", 1<<17),
		"words":    strings.Repeat("proxy-name_01 key value token:", 1<<12),
		"urls":     strings.Repeat("https://a.example/b?c=d ", 1<<12),
	} {
		start := time.Now()
		_ = Redact(in)
		if d := time.Since(start); d > 20*time.Second {
			t.Errorf("%s: Redact took %s", name, d)
		} else {
			t.Logf("%s: %s", name, d)
		}
	}
}
