//go:build windows

package helper

import (
	"errors"
	"os"

	"github.com/Microsoft/go-winio"
)

func isPlatformTransport(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, winio.ErrTimeout)
}
