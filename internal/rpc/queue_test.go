package rpc

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func changedMsg(topic string) message {
	b, _ := json.Marshal(Changed{Topic: topic})
	return message{Method: "changed", Params: b}
}

// TestChangedCoalesced: one "changed" per topic waits for a slow client,
// and it moves behind the answers queued since: an answer computed before
// the newer change must not be the last word on that topic.
func TestChangedCoalesced(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ss := newSession(a)
	defer ss.cancel()
	ss.enqueue(changedMsg("proxies"), "proxies", false)
	ss.enqueue(message{ID: json.RawMessage("1"), Result: json.RawMessage(`["old"]`)}, "", false)
	for i := 0; i < 100; i++ {
		ss.enqueue(changedMsg("proxies"), "proxies", false)
		ss.enqueue(changedMsg("status"), "status", false)
	}
	ss.qmu.Lock()
	n := len(ss.queue)
	ss.qmu.Unlock()
	if n != 3 {
		t.Fatalf("%d messages queued, want 3 (the answer and one per topic)", n)
	}
	go ss.writeLoop()
	sc := bufio.NewScanner(b)
	var got []string
	for len(got) < 3 && sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if m.Method == "changed" {
			var c Changed
			_ = json.Unmarshal(m.Params, &c)
			got = append(got, c.Topic)
		} else {
			got = append(got, "answer")
		}
	}
	if len(got) != 3 || got[0] != "answer" || got[1] != "proxies" || got[2] != "status" {
		t.Fatalf("order = %v, want [answer proxies status]", got)
	}
}

// TestSlowClientDropped: a client that does not read is disconnected at
// the write timeout, and nothing is queued for it afterwards.
func TestSlowClientDropped(t *testing.T) {
	old := writeTimeout
	writeTimeout = 100 * time.Millisecond
	defer func() { writeTimeout = old }()
	a, b := net.Pipe() // unbuffered: a write waits for the reader
	defer b.Close()
	ss := newSession(a)
	defer ss.cancel()
	go ss.writeLoop()
	ss.send(message{ID: json.RawMessage("1"), Result: json.RawMessage(`[]`)})
	select {
	case <-ss.wdone:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer still waits for a client that does not read")
	}
	if _, err := a.Write([]byte("x")); err == nil {
		t.Fatal("the connection was not closed")
	}
	if om := ss.enqueue(changedMsg("status"), "status", false); om != nil {
		t.Fatal("a message was queued for a dead connection")
	}
	ss.qmu.Lock()
	n := len(ss.queue)
	ss.qmu.Unlock()
	if n != 0 {
		t.Fatalf("%d messages left queued", n)
	}
}

// TestQueueBound: a client with too many unread messages is disconnected.
func TestQueueBound(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ss := newSession(a) // no writer: nothing leaves the queue
	defer ss.cancel()
	for i := 0; i <= maxQueued; i++ {
		ss.send(message{ID: json.RawMessage("1"), Result: json.RawMessage(`[]`)})
	}
	if _, err := a.Write([]byte("x")); err == nil {
		t.Fatal("the connection was not closed at the queue limit")
	}
}

func TestSameExe(t *testing.T) {
	self := selfExe()
	if self == "" {
		t.Fatal("own executable unknown")
	}
	pid, exe, err := pidExeOfSelf()
	if err != nil {
		t.Fatalf("own process %d: %v", pid, err)
	}
	if !sameExe(exe, self) {
		t.Fatalf("the system reports %q for this process, os.Executable %q", exe, self)
	}
	if sameExe(self+".other", self) || sameExe("", self) || sameExe(self, "") {
		t.Fatal("different paths compare equal")
	}
}
