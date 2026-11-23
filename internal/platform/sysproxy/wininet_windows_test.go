//go:build windows

package sysproxy

import (
	"testing"
	"unsafe"
)

// The structs handed to WinINet and RAS must have the C layout. These are
// pure size and offset checks: nothing here calls into the system.
func TestWinStructLayout(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0))
	check := func(name string, got, want uintptr) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}

	// INTERNET_PER_CONN_OPTIONW: DWORD dwOption, then an 8-byte union
	// (DWORD / LPWSTR / FILETIME) aligned like a pointer.
	var opt perConnOption
	check("sizeof(perConnOption)", unsafe.Sizeof(opt), ptr+8)
	check("offsetof(perConnOption.option)", unsafe.Offsetof(opt.option), 0)
	check("offsetof(perConnOption.value)", unsafe.Offsetof(opt.value), ptr)
	check("sizeof(perConnOption.value)", unsafe.Sizeof(opt.value), 8)

	// INTERNET_PER_CONN_OPTION_LISTW: DWORD, LPWSTR, DWORD, DWORD, pointer.
	var list perConnOptionList
	check("sizeof(perConnOptionList)", unsafe.Sizeof(list), 3*ptr+8)
	check("offsetof(perConnOptionList.size)", unsafe.Offsetof(list.size), 0)
	check("offsetof(perConnOptionList.connection)", unsafe.Offsetof(list.connection), ptr)
	check("offsetof(perConnOptionList.optionCount)", unsafe.Offsetof(list.optionCount), 2*ptr)
	check("offsetof(perConnOptionList.optionError)", unsafe.Offsetof(list.optionError), 2*ptr+4)
	check("offsetof(perConnOptionList.options)", unsafe.Offsetof(list.options), 2*ptr+8)

	// RASENTRYNAMEW has no pointers, so it is the same everywhere:
	// DWORD, WCHAR[257], (2 bytes padding), DWORD, WCHAR[261], (2 bytes).
	var ras rasEntryName
	check("sizeof(rasEntryName)", unsafe.Sizeof(ras), 1048)
	check("offsetof(rasEntryName.entryName)", unsafe.Offsetof(ras.entryName), 4)
	check("offsetof(rasEntryName.flags)", unsafe.Offsetof(ras.flags), 520)
	check("offsetof(rasEntryName.phonebookPath)", unsafe.Offsetof(ras.phonebookPath), 524)

	if ptr == 8 {
		check("sizeof(perConnOption) on 64-bit", unsafe.Sizeof(opt), 16)
		check("sizeof(perConnOptionList) on 64-bit", unsafe.Sizeof(list), 32)
	}
}

// The union is read as a DWORD through its low half and as a pointer
// through its full width; both must see what was stored.
func TestPerConnOptionUnion(t *testing.T) {
	o := perConnOption{value: 0xAAAAAAAA_00000003}
	if o.dword() != 3 {
		t.Fatalf("dword() = %#x, want 3", o.dword())
	}
	var w uint16 = 'x'
	o.value = uint64(uintptr(unsafe.Pointer(&w)))
	if p := *(**uint16)(unsafe.Pointer(&o.value)); p != &w {
		t.Fatalf("pointer read back as %p, want %p", p, &w)
	}
}
