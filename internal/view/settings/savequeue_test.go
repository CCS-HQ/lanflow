package settings

import (
	"testing"
	"time"

	"lanflow/internal/model"
)

// TestFailedSaveLeavesTheQueuedOnes: a save that failed (a port in use)
// must not take the save queued behind it down with it. The later
// snapshot was built on the draft and so carries the failed value, but
// only its own change is saved; and once the failure is known the
// controls stop showing the failed value while the later save still
// waits.
func TestFailedSaveLeavesTheQueuedOnes(t *testing.T) {
	g := newGated()
	p, a, tt, _ := asyncPage(t, g)

	s := p.current()
	s.MixedPort = 7891 // the fake refuses 7891 as taken
	var portErr error
	p.save(s, "", func(err error) { portErr = err })
	g.waitStarted(t, "save", 1)

	s = p.current()
	if s.MixedPort != 7891 {
		t.Fatal("the draft does not show the port while it is being saved")
	}
	s.LogLevel = "debug"
	s.Advanced.PACMode = true
	p.save(s, "", nil)

	g.release("save", 1)
	// The outcome reaches the page in a frame after the job is done (the
	// page's first frame started jobs of its own, so counting does not
	// tell which one finished).
	for deadline := time.Now().Add(10 * time.Second); portErr == nil; tt.Frame() {
		if time.Now().After(deadline) {
			t.Fatal("the refused port reported no error")
		}
		time.Sleep(time.Millisecond)
	}
	if cur := p.current(); cur.MixedPort != 7890 || cur.LogLevel != "debug" || !cur.Advanced.PACMode {
		t.Fatalf("after the failure the page shows port %d, log level %q, PAC %v; want the stored port and the queued changes",
			cur.MixedPort, cur.LogLevel, cur.Advanced.PACMode)
	}

	g.release("save", 2)
	a.Settle(tt.Frame)
	got := g.Fake.Settings()
	if got.MixedPort != 7890 {
		t.Fatalf("the failed port was saved with the later change: %d", got.MixedPort)
	}
	if got.LogLevel != "debug" || !got.Advanced.PACMode {
		t.Fatalf("the later save lost its own change: %q %v", got.LogLevel, got.Advanced.PACMode)
	}
	if p.draft != nil || len(p.saveQ) != 0 {
		t.Fatal("the queue is not empty after the last save")
	}
}

// TestSaveKeepsWhatOthersStored: the page saves only what its control
// changed. A setting another writer (another page, the tray, an agent, the
// configuration sync) stored after the page drew its snapshot stays, even
// when it is stored while the page's save waits.
func TestSaveKeepsWhatOthersStored(t *testing.T) {
	g := newGated()
	p, a, tt, _ := asyncPage(t, g)

	s := p.current() // the snapshot the controls were drawn with

	// Another writer, after the snapshot.
	other := g.Fake.Settings()
	other.Mode = model.ModeGlobal
	other.Sites = append(other.Sites, model.Site{Name: "example", URL: "https://example.org"})
	if err := g.Fake.SaveSettings(other); err != nil {
		t.Fatal(err)
	}

	s.OpenAtLogin = true
	p.save(s, "", nil)
	g.waitStarted(t, "save", 1)

	// And one more while the page's save is on its way.
	if err := g.Fake.UpdateSettings(model.Settings{}, model.Settings{TrayNode: true}); err != nil {
		t.Fatal(err)
	}

	g.release("save", 1)
	a.Settle(tt.Frame)
	got := g.Fake.Settings()
	if !got.OpenAtLogin {
		t.Fatal("the page's change was not saved")
	}
	if got.Mode != model.ModeGlobal || len(got.Sites) != len(other.Sites) || !got.TrayNode {
		t.Fatalf("the page's save undid another writer: mode %s, %d sites (want %d), tray node %v",
			got.Mode, len(got.Sites), len(other.Sites), got.TrayNode)
	}
}

// TestChooseCoreKeepsWhatWasSavedBehindTheDialog: the file dialog stays
// open as long as the user likes. Settings saved meanwhile (here: by
// another writer, with a frame drawn behind the dialog) must not be taken
// back by the snapshot the 更改… button was drawn with.
func TestChooseCoreKeepsWhatWasSavedBehindTheDialog(t *testing.T) {
	g := newGated()
	g.release("save", 100)
	p, a, tt, _ := asyncPage(t, g)
	tt.Frame()

	old := pickCoreFile
	defer func() { pickCoreFile = old }()
	pickCoreFile = func() ([]string, error) {
		// While the dialog is open.
		other := g.Fake.Settings()
		other.Mode, other.LogLevel = model.ModeGlobal, "debug"
		if err := g.Fake.SaveSettings(other); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		return []string{"/opt/mihomo"}, nil
	}
	p.chooseCore()
	a.Settle(tt.Frame)

	got := g.Fake.Settings()
	if got.CorePath != "/opt/mihomo" || got.CoreVersion != "" {
		t.Fatalf("the chosen core was not saved: %q %q", got.CorePath, got.CoreVersion)
	}
	if got.Mode != model.ModeGlobal || got.LogLevel != "debug" {
		t.Fatalf("choosing a core undid what was saved behind the dialog: mode %s, log level %q", got.Mode, got.LogLevel)
	}
}
