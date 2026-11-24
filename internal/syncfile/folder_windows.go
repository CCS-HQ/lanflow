//go:build windows

package syncfile

import "golang.org/x/sys/windows"

const (
	fileAttributeOffline            = 0x00001000
	fileAttributeRecallOnDataAccess = 0x00400000
)

func isCloudPlaceholder(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil || attrs == windows.INVALID_FILE_ATTRIBUTES {
		return false
	}
	return attrs&(fileAttributeOffline|fileAttributeRecallOnDataAccess) != 0
}
