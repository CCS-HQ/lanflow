//go:build windows

package netinfo

import (
	"context"

	"golang.org/x/sys/windows"
)

func otherTUN(context.Context) (string, bool) {
	adapters, err := listAdapters()
	if err != nil {
		return "", false
	}
	routes, err := listRoutes(windows.AF_INET)
	if err != nil {
		return "", false
	}
	return detectWindowsTUN(adapters, routes)
}
