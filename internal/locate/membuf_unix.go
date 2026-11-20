//go:build unix

package locate

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// readOffHeap reads the file into memory that is not part of the Go heap
// (an anonymous mapping) and returns it with the function that gives it
// back. A database is several megabytes held for as long as the process
// runs; on the heap it would count against the process's memory limit and
// make the collector work harder for the whole session. Unlike a mapping
// of the file itself, replacing or truncating the file cannot fault a
// lookup.
func readOffHeap(path string) (data []byte, free func(), err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	n := st.Size()
	if n <= 0 || n > maxDatabaseSize {
		return nil, nil, errors.New("locate: not a database file")
	}
	buf, err := unix.Mmap(-1, 0, int(n), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return nil, nil, err
	}
	if _, err := io.ReadFull(f, buf); err != nil {
		_ = unix.Munmap(buf)
		return nil, nil, err
	}
	return buf, func() { _ = unix.Munmap(buf) }, nil
}
