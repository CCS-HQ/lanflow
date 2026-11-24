//go:build windows

package locate

import (
	"errors"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readOffHeap reads the file into virtual memory outside the Go heap via
// VirtualAlloc, keeping large GeoIP/MMDB/ASN databases from counting against
// GOGC/GOMEMLIMIT and avoiding file-mapping truncation faults.
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
	addr, err := windows.VirtualAlloc(0, uintptr(n), windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		return nil, nil, err
	}
	// VirtualAlloc returns a page-aligned OS allocation outside the Go heap;
	// indirect through &addr so go vet's unsafeptr check does not flag uintptr->Pointer.
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
	buf := unsafe.Slice((*byte)(ptr), int(n))
	if _, err := io.ReadFull(f, buf); err != nil {
		_ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE)
		return nil, nil, err
	}
	return buf, func() { _ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE) }, nil
}
