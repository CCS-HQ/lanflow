package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"time"
)

var errPortListening = errors.New("proxy port has a listener")

const (
	// portWaitShort bounds the wait when no root core went away lately:
	// the port is unbindable for another reason, which waiting rarely fixes.
	portWaitShort = 4 * time.Second
	// rootTimeWait is how long TIME_WAIT sockets of a root core can keep
	// the port from the user's core: two MSLs (macOS net.inet.tcp.msl is
	// 15 s, measured 30 s in the VM) plus a margin.
	rootTimeWait = 35 * time.Second
)

// portWaitTimeout is how long to wait for the proxy port: the rest of
// rootTimeWait after a root core went away at rootStoppedAt (zero = never),
// and never less than portWaitShort.
func portWaitTimeout(now, rootStoppedAt time.Time) time.Duration {
	if rootStoppedAt.IsZero() {
		return portWaitShort
	}
	if left := rootTimeWait - now.Sub(rootStoppedAt); left > portWaitShort {
		return left
	}
	return portWaitShort
}

// waitTCPPortFree handles a macOS privilege transition: sockets left in
// TIME_WAIT by the root-run TUN core cannot be rebound by the user's core,
// even with SO_REUSEADDR. A successful Stop has already reaped the process;
// killing more processes or merely skipping the bind check cannot help.
// The caller picks timeout (portWaitTimeout). A real listener is still a
// conflict and is reported immediately.
func waitTCPPortFree(ctx context.Context, port int, lan bool, timeout time.Duration) error {
	if runtime.GOOS != "darwin" {
		return errPortListening
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return waitPortRelease(ctx, 50*time.Millisecond,
		func() bool { return tcpPortInUse(port, lan) },
		func() bool { return tcpPortListening(port) })
}

// tcpPortListening reports whether something accepts connections on
// 127.0.0.1:port.
func tcpPortListening(port int) bool {
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	c, err := d.Dial("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func waitPortRelease(ctx context.Context, poll time.Duration, busy, listening func() bool) error {
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !busy() {
			return nil
		}
		if listening() {
			return errPortListening
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
