package settings

import (
	"reflect"
	"testing"

	"lanflow/internal/model"
)

// TestRevertChangeDropsOnlyTheFailedChange: a save that failed (a port in
// use) must not take the save queued behind it down with it. The later
// snapshot was built on the draft and so carries the failed change; that
// change, and only that, goes back to what is stored.
func TestRevertChangeDropsOnlyTheFailedChange(t *testing.T) {
	stored := model.Settings{MixedPort: 7890, LogLevel: "info"}
	stored.Advanced.PACPort = 1000

	// The failed request: the port, and one option of a section.
	failedBase := stored
	failed := stored
	failed.MixedPort = 80
	failed.Advanced.PACPort = 2000

	// Queued behind it: its own changes on top of the failed snapshot.
	later := failed
	later.LogLevel = "debug"
	later.Advanced.PACMode = true

	revertChange(reflect.ValueOf(&later).Elem(), reflect.ValueOf(failed), reflect.ValueOf(failedBase), reflect.ValueOf(stored))

	if later.MixedPort != 7890 {
		t.Fatalf("the failed port change is still queued: %d", later.MixedPort)
	}
	if later.Advanced.PACPort != 1000 {
		t.Fatalf("the failed option of the section is still queued: %d", later.Advanced.PACPort)
	}
	if later.LogLevel != "debug" || !later.Advanced.PACMode {
		t.Fatalf("the later request lost its own change: %q %v", later.LogLevel, later.Advanced.PACMode)
	}

	// A value the later request set itself, over the failed one, stays.
	again := failed
	again.MixedPort = 9000
	revertChange(reflect.ValueOf(&again).Elem(), reflect.ValueOf(failed), reflect.ValueOf(failedBase), reflect.ValueOf(stored))
	if again.MixedPort != 9000 {
		t.Fatalf("a value set after the failed one was taken back: %d", again.MixedPort)
	}
}
