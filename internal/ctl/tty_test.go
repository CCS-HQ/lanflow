package ctl

import (
	"os"
	"testing"
)

func newPipe() (*os.File, *os.File, error) { return os.Pipe() }

func devNull(t *testing.T) *os.File {
	f, err := os.Open(os.DevNull)
	if err != nil {
		return nil
	}
	t.Cleanup(func() { f.Close() })
	return f
}
