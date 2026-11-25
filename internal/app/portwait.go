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

// waitTCPPortFree handles a macOS privilege transition: sockets left in
// TIME_WAIT by the root-run TUN core cannot be rebound by the user's core,
// even with SO_REUSEADDR. A successful Stop has already reaped the process;
// killing more processes or merely skipping the bind check cannot help.
// The default TCP MSL is 15 s, so allow two MSLs plus a small margin. A real
// listener is still a conflict and is reported immediately.
func waitTCPPortFree(ctx context.Context, port int, lan bool) error {
	if runtime.GOOS != "darwin" {
		return errPortListening
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return waitPortRelease(ctx, 50*time.Millisecond,
		func() bool { return tcpPortInUse(port, lan) },
		func() bool {
			d := net.Dialer{Timeout: 200 * time.Millisecond}
			c, err := d.DialContext(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return false
			}
			_ = c.Close()
			return true
		})
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
