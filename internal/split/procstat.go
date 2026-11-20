package split

import (
	"strconv"
	"strings"
)

// parseProcStat returns the starttime field of a /proc/<pid>/stat line.
func parseProcStat(stat string) (int64, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	// After ")": state is field 3, starttime field 22.
	f := strings.Fields(stat[i+1:])
	if len(f) < 20 {
		return 0, false
	}
	n, err := strconv.ParseInt(f[19], 10, 64)
	return n, err == nil
}
