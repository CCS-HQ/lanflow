//go:build windows

package fsx

import (
	"golang.org/x/sys/windows"
)

func hideTempFile(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil || attrs == windows.INVALID_FILE_ATTRIBUTES {
		return
	}
	_ = windows.SetFileAttributes(p, attrs|windows.FILE_ATTRIBUTE_HIDDEN)
}

func unhideFile(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil || attrs == windows.INVALID_FILE_ATTRIBUTES || attrs&windows.FILE_ATTRIBUTE_HIDDEN == 0 {
		return
	}
	_ = windows.SetFileAttributes(p, attrs&^windows.FILE_ATTRIBUTE_HIDDEN)
}

func isReadOnlyDest(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	return err == nil && attrs != windows.INVALID_FILE_ATTRIBUTES && attrs&windows.FILE_ATTRIBUTE_READONLY != 0
}
