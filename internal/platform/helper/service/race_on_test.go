//go:build race

package service

// raceEnabled reports whether the tests run under the race detector,
// which slows the code it instruments several times over.
const raceEnabled = true
