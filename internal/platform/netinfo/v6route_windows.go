//go:build windows

package netinfo

import (
	"context"

	"golang.org/x/sys/windows"
)

func ipv6Route(context.Context) V6Route {
	adapters, err := listAdapters()
	if err != nil {
		return V6Route{}
	}
	routes, err := listRoutes(windows.AF_INET6)
	if err != nil {
		return V6Route{}
	}
	return analyzeWindowsV6Routes(adapters, routes)
}
