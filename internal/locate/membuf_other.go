//go:build !unix

package locate

import (
	"errors"
	"os"
)

// readOffHeap reads the file; on this system into the Go heap (see the
// Unix version for why elsewhere it is not).
func readOffHeap(path string) (data []byte, free func(), err error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if st.Size() <= 0 || st.Size() > maxDatabaseSize {
		return nil, nil, errors.New("locate: not a database file")
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return data, func() {}, nil
}
