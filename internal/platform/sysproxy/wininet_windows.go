//go:build windows

package sysproxy

// WinINet per-connection options and RAS phone-book enumeration: the
// system calls behind winSystem. The decisions live in winconn.go.

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	internetOptionPerConnectionOption = 75 // INTERNET_OPTION_PER_CONNECTION_OPTION

	internetPerConnFlags         = 1  // INTERNET_PER_CONN_FLAGS
	internetPerConnProxyServer   = 2  // INTERNET_PER_CONN_PROXY_SERVER
	internetPerConnProxyBypass   = 3  // INTERNET_PER_CONN_PROXY_BYPASS
	internetPerConnAutoconfigURL = 4  // INTERNET_PER_CONN_AUTOCONFIG_URL
	internetPerConnFlagsUI       = 10 // INTERNET_PER_CONN_FLAGS_UI

	errorBufferTooSmall = 603 // ERROR_BUFFER_TOO_SMALL

	rasMaxEntryName = 256 // RAS_MaxEntryName
	maxPath         = 260 // MAX_PATH
)

var (
	procInternetQueryOptionW = wininet.NewProc("InternetQueryOptionW")
	procGlobalFree           = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
	procRasEnumEntriesW      = windows.NewLazySystemDLL("rasapi32.dll").NewProc("RasEnumEntriesW")
)

// perConnOption is INTERNET_PER_CONN_OPTIONW. The C union (DWORD dwValue,
// LPWSTR pszValue, FILETIME ftValue) is 8 bytes wide and pointer aligned,
// which is exactly how Go lays out a uint64 on every Windows architecture
// (offset 8 on amd64/arm64, offset 4 on 386). It is not declared as a
// pointer because WinINet also stores plain DWORDs there, and the garbage
// collector must never see those as pointers.
type perConnOption struct {
	option uint32
	value  uint64
}

// dword returns the union read as dwValue (the machines are little endian).
func (o *perConnOption) dword() uint32 { return uint32(o.value) }

// perConnOptionList is INTERNET_PER_CONN_OPTION_LISTW.
type perConnOptionList struct {
	size        uint32
	connection  *uint16 // nil = the LAN
	optionCount uint32
	optionError uint32
	options     *perConnOption
}

// rasEntryName is RASENTRYNAMEW (WINVER >= 0x500, pack 4).
type rasEntryName struct {
	size          uint32
	entryName     [rasMaxEntryName + 1]uint16
	flags         uint32
	phonebookPath [maxPath + 1]uint16
}

// connectionName converts a winConn name to pszConnection.
func connectionName(name string) (*uint16, error) {
	if name == winLAN {
		return nil, nil
	}
	return windows.UTF16PtrFromString(name)
}

// queryConn reads the flags, proxy server, bypass list and PAC address of
// one connection with InternetQueryOptionW.
func (winReal) queryConn(name string) (winConn, error) {
	if err := procInternetQueryOptionW.Find(); err != nil {
		return winConn{}, err
	}
	if err := procGlobalFree.Find(); err != nil {
		return winConn{}, err
	}
	conn, err := connectionName(name)
	if err != nil {
		return winConn{}, err
	}
	// INTERNET_PER_CONN_FLAGS_UI reports what the settings dialog shows;
	// plain INTERNET_PER_CONN_FLAGS can hide the auto-detect bit after a
	// failed detection. Fall back to it where the UI option is unknown.
	var lastErr error
	for _, flagsOption := range []uint32{internetPerConnFlagsUI, internetPerConnFlags} {
		opts := [4]perConnOption{
			{option: flagsOption},
			{option: internetPerConnProxyServer},
			{option: internetPerConnProxyBypass},
			{option: internetPerConnAutoconfigURL},
		}
		list := perConnOptionList{
			size:        uint32(unsafe.Sizeof(perConnOptionList{})),
			connection:  conn,
			optionCount: uint32(len(opts)),
			options:     &opts[0],
		}
		size := list.size
		r, _, e := procInternetQueryOptionW.Call(0, internetOptionPerConnectionOption,
			uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
		if r == 0 {
			lastErr = e
			continue
		}
		return winConn{
			Name:       name,
			Flags:      opts[0].dword(),
			Server:     takeString(&opts[1]),
			Bypass:     takeString(&opts[2]),
			AutoConfig: takeString(&opts[3]),
		}, nil
	}
	return winConn{}, fmt.Errorf("InternetQueryOptionW: %w", lastErr)
}

// takeString copies the string WinINet returned in o and releases it:
// WinINet allocates these with GlobalAlloc and the caller frees them.
func takeString(o *perConnOption) string {
	// Read the union as pszValue. The memory is not Go's, so holding it
	// in a pointer for the copy is fine.
	p := *(**uint16)(unsafe.Pointer(&o.value))
	if p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	procGlobalFree.Call(uintptr(o.value))
	o.value = 0
	return s
}

// setConn writes the selected options of c with InternetSetOptionW.
func (winReal) setConn(c winConn, fields winField) error {
	if err := procInternetSetOptionW.Find(); err != nil {
		return err
	}
	conn, err := connectionName(c.Name)
	if err != nil {
		return err
	}
	// The strings are referenced from the union as integers, so they are
	// pinned: that keeps them alive and at a fixed address until the call
	// has returned.
	var pin runtime.Pinner
	defer pin.Unpin()
	opts := make([]perConnOption, 0, 4)
	if fields&winFieldFlags != 0 {
		opts = append(opts, perConnOption{option: internetPerConnFlags, value: uint64(c.Flags)})
	}
	for _, it := range []struct {
		field  winField
		option uint32
		value  string
	}{
		{winFieldServer, internetPerConnProxyServer, c.Server},
		{winFieldBypass, internetPerConnProxyBypass, c.Bypass},
		{winFieldAutoConfig, internetPerConnAutoconfigURL, c.AutoConfig},
	} {
		if fields&it.field == 0 {
			continue
		}
		p, err := windows.UTF16PtrFromString(it.value)
		if err != nil {
			return err
		}
		pin.Pin(p)
		opts = append(opts, perConnOption{option: it.option, value: uint64(uintptr(unsafe.Pointer(p)))})
	}
	if len(opts) == 0 {
		return nil
	}
	list := perConnOptionList{
		size:        uint32(unsafe.Sizeof(perConnOptionList{})),
		connection:  conn,
		optionCount: uint32(len(opts)),
		options:     &opts[0],
	}
	r, _, e := procInternetSetOptionW.Call(0, internetOptionPerConnectionOption,
		uintptr(unsafe.Pointer(&list)), uintptr(list.size))
	if r == 0 {
		return fmt.Errorf("InternetSetOptionW: %w", e)
	}
	return nil
}

// rasEntries lists the entry names of the user's and the all-users phone
// book with RasEnumEntriesW. The first call (no buffer) reports the size
// needed through ERROR_BUFFER_TOO_SMALL; with no entries it succeeds
// straight away. The loop covers an entry being added between the calls.
func (winReal) rasEntries() ([]string, error) {
	if err := procRasEnumEntriesW.Find(); err != nil {
		return nil, err
	}
	const entrySize = uint32(unsafe.Sizeof(rasEntryName{}))
	var cb, count uint32
	r, _, _ := procRasEnumEntriesW.Call(0, 0, 0, uintptr(unsafe.Pointer(&cb)), uintptr(unsafe.Pointer(&count)))
	for attempt := 0; attempt < 4 && r == errorBufferTooSmall; attempt++ {
		n := (cb + entrySize - 1) / entrySize
		if n == 0 {
			n = 1
		}
		buf := make([]rasEntryName, n)
		buf[0].size = entrySize
		cb = n * entrySize
		r, _, _ = procRasEnumEntriesW.Call(0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&cb)), uintptr(unsafe.Pointer(&count)))
		if r != 0 {
			continue
		}
		if count > n {
			count = n
		}
		names := make([]string, 0, count)
		for i := range buf[:count] {
			names = append(names, windows.UTF16ToString(buf[i].entryName[:]))
		}
		return names, nil
	}
	if r != 0 {
		return nil, fmt.Errorf("RasEnumEntriesW: %w", syscall.Errno(r))
	}
	return nil, nil
}
