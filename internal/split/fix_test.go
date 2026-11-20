package split

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
)

// A service that refuses the handshake for its protocol version is ended
// and replaced once. When the one started in its place refuses as well
// (the executable on disk is not this window's version), Connect returns
// the handshake error: it used to end and start services for ever, and the
// window never appeared.
func TestProtocolMismatchReplacesOnce(t *testing.T) {
	p := &fakeProc{alive: true, onTerm: true}
	useFakeProc(t, p)
	// Every started service comes up alive again and still mismatches.
	mismatch := &rpc.Error{Code: rpc.CodeHandshake, Message: "protocol", Data: &rpc.ErrorData{Kind: "protocol", PID: fakePID}}
	var dials, starts, cleanups atomic.Int32
	oldDial := dial
	dial = func(context.Context, remote.Options) (*remote.Client, error) {
		dials.Add(1)
		return nil, mismatch
	}
	t.Cleanup(func() { dial = oldDial })

	done := make(chan error, 1)
	go func() {
		_, _, err := Connect(context.Background(), ConnectOptions{
			DataDir: t.TempDir(), Version: "1.0.0",
			Start: func() error {
				starts.Add(1)
				p.mu.Lock()
				p.alive = true
				p.mu.Unlock()
				return nil
			},
			Cleanup: func() { cleanups.Add(1) },
			Stop:    StopOptions{Graceful: 50 * time.Millisecond, Term: 50 * time.Millisecond, Kill: 50 * time.Millisecond, Logf: func(string, ...any) {}},
		})
		done <- err
	}()
	select {
	case err := <-done:
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeHandshake {
			t.Fatalf("err = %v, want the handshake error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Connect did not return: %d dials, %d services ended", dials.Load(), cleanups.Load())
	}
	if got := p.steps(); !slices.Equal(got, []string{"term"}) {
		t.Fatalf("steps = %v, want one terminate", got)
	}
	if cleanups.Load() != 1 || dials.Load() != 2 {
		t.Fatalf("cleanups = %d dials = %d, want 1 and 2", cleanups.Load(), dials.Load())
	}
}

// StopService works on a pid the service reported and may run for close to
// a minute. When the service ends on its own and the pid goes to another
// process meanwhile, that process must be neither terminated nor killed.
func TestStopServiceLeavesAReusedPID(t *testing.T) {
	for _, tc := range []struct {
		name string
		// reusedAfter: the pid changes hands after this step ("quit" or "term").
		reusedAfter string
		want        []string
	}{
		{"reused while waiting for the quit", "quit", []string{"quit"}},
		{"reused while waiting for the terminate", "term", []string{"quit", "term"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProc{alive: true} // "alive" throughout: the pid is in use
			useFakeProc(t, p)
			released := false
			procOps.pin = func(pid int) (func() bool, func()) {
				if pid != fakePID {
					t.Errorf("pinned %d", pid)
				}
				return func() bool { return !slices.Contains(p.steps(), tc.reusedAfter) }, func() { released = true }
			}
			if err := StopService(fakePID, fastStop(p)); err != nil {
				t.Fatal(err)
			}
			if got := p.steps(); !slices.Equal(got, tc.want) {
				t.Fatalf("steps = %v, want %v", got, tc.want)
			}
			if !released {
				t.Fatal("the pin was not released")
			}
		})
	}
}

// The same process keeps its identity; another pid has another one.
func TestPinProcessOwnPID(t *testing.T) {
	same, release := pinProcess(os.Getpid())
	defer release()
	if !same() {
		t.Fatal("this process is no longer itself")
	}
}

func TestParseProcStat(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1234 (lanflow) S 1 1234 1234 0 -1 4194304 100 0 0 0 5 3 0 0 20 0 9 0 987654 1000 200 18446744073709551615", 987654, true},
		// A command name with spaces and parentheses.
		{"77 (a b) c) R 1 77 77 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 42 0 0", 42, true},
		{"77 (short) R 1 2", 0, false},
		{"garbage", 0, false},
		{"", 0, false},
	} {
		got, ok := parseProcStat(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseProcStat(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
