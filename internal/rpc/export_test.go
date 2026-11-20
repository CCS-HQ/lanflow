package rpc

import "time"

// SetConsentTimeout shortens the wait for the user in tests.
func SetConsentTimeout(d time.Duration) (restore func()) {
	old := consentTimeout
	consentTimeout = d
	return func() { consentTimeout = old }
}

// SetSelfExe makes the server take path for its own executable: peers,
// the test binary among them, are then some other program.
func SetSelfExe(path string) (restore func()) {
	old := selfExeOverride.Swap(&path)
	return func() { selfExeOverride.Store(old) }
}

// SetLimits changes the handshake timeout and the connection limit of
// the servers made from now on (0 keeps a value).
func SetLimits(handshake time.Duration, conns int) (restore func()) {
	oh, oc := handshakeTimeout, maxConns
	if handshake > 0 {
		handshakeTimeout = handshake
	}
	if conns > 0 {
		maxConns = conns
	}
	return func() { handshakeTimeout, maxConns = oh, oc }
}

// RawDial connects to the endpoint without a handshake.
var RawDial = dialEndpoint
