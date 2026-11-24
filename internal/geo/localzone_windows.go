//go:build windows

package geo

import (
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var procSHLoadIndirectString = windows.NewLazySystemDLL("shlwapi.dll").NewProc("SHLoadIndirectString")

// osLocalZone reads the Windows time zone from the registry and maps it to
// an IANA name. TimeZoneKeyName (Vista and later) is the English key name
// on every locale; when it is missing, the localized StandardName is
// matched against the Std value of each key under Time Zones, as Go's own
// runtime does.
func osLocalZone() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	if name, _, err := k.GetStringValue("TimeZoneKeyName"); err == nil {
		if z, ok := FromWindowsZone(name); ok {
			return z
		}
	}
	std, _, err := k.GetStringValue("StandardName")
	if err != nil || std == "" {
		return ""
	}
	if key := keyForStandardName(std); key != "" {
		if z, ok := FromWindowsZone(key); ok {
			return z
		}
	}
	return ""
}

// keyForStandardName finds the Time Zones key whose localized standard
// name (Std or resolved MUI_Std) is std; "" when none matches.
func keyForStandardName(std string) string {
	zones, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion\Time Zones`, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer zones.Close()
	names, err := zones.ReadSubKeyNames(-1)
	if err != nil {
		return ""
	}
	for _, name := range names {
		z, err := registry.OpenKey(zones, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		s, _, err := z.GetStringValue("Std")
		mui, _, merr := z.GetStringValue("MUI_Std")
		z.Close()
		if err == nil && strings.EqualFold(s, std) {
			return name
		}
		if merr == nil && mui != "" && strings.EqualFold(loadIndirectString(mui), std) {
			return name
		}
	}
	return ""
}

func loadIndirectString(s string) string {
	if s == "" || procSHLoadIndirectString.Find() != nil {
		return s
	}
	src, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return s
	}
	var out [256]uint16
	r, _, _ := procSHLoadIndirectString.Call(
		uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(&out[0])),
		uintptr(len(out)),
		0,
	)
	runtime.KeepAlive(src)
	runtime.KeepAlive(&out[0])
	if r != 0 {
		return ""
	}
	return windows.UTF16ToString(out[:])
}
