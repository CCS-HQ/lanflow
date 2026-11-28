//go:build linux && !android

package sysproxy

import "context"

func set(host string, port int, bypass []string) error {
	return gnomeSet(context.Background(), runCommand, host, port, bypass)
}

func clear() error { return gnomeClear(context.Background(), runCommand) }

func setPAC(url string) error { return gnomeSetPAC(context.Background(), runCommand, url) }

func get() (State, error) { return gnomeGet(context.Background(), runCommand) }
