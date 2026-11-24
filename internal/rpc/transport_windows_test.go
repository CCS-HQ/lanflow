//go:build windows

package rpc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"lanflow/internal/app/fake"
)

func TestWindowsPipeRebindAndPeerProc(t *testing.T) {
	pipe := fmt.Sprintf(`\\.\pipe\lanflow-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	t.Setenv("LANFLOW_SOCKET", pipe)

	l1, err := Listen("")
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	if _, err := Listen(""); !errors.Is(err, ErrInUse) {
		l1.Close()
		t.Fatalf("duplicate Listen = %v, want ErrInUse", err)
	}

	srv := NewServer(Options{API: fake.NewInstant(), Version: "1.0"})
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(l1) }()

	dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dcancel()
	c, err := Dial(dctx, DialOptions{Client: KindCtl})
	if err != nil {
		_ = srv.Close()
		t.Fatalf("Dial: %v", err)
	}
	if got := c.Welcome().PID; got != os.Getpid() {
		_ = srv.Close()
		_ = c.Close()
		t.Fatalf("Welcome().PID = %d, want %d", got, os.Getpid())
	}

	// Verify peerProc on a separate direct pipe connection.
	nc, err := dialEndpoint(dctx, "")
	if err != nil {
		_ = srv.Close()
		_ = c.Close()
		t.Fatalf("dialPipe: %v", err)
	}
	pid, exe, perr := peerProc(nc)
	_ = nc.Close()
	if perr != nil || pid != os.Getpid() || exe == "" {
		_ = srv.Close()
		_ = c.Close()
		t.Fatalf("peerProc = (%d, %q, %v), want pid %d", pid, exe, perr, os.Getpid())
	}

	// Shut down server without explicitly calling c.Close(): Conn.read()
	// must close the client handle so a new listener can bind immediately.
	_ = srv.Close()
	<-serveDone
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("client did not observe close")
	}

	var l2Err error
	for i := 0; i < 20; i++ {
		l2, err := Listen("")
		if err == nil {
			_ = l2.Close()
			return
		}
		l2Err = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("rebind after server close failed: %v (client err: %v)", l2Err, strings.TrimSpace(fmt.Sprint(c.Err())))
}
