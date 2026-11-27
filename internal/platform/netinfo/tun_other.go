//go:build (!darwin || ios) && !windows

package netinfo

import "context"

func otherTUN(context.Context) (string, bool) { return "", false }
