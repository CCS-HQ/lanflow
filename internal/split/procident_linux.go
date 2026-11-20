package split

import (
	"os"
	"strconv"
)

// processStart returns the start time of process pid in clock ticks since
// boot: field 22 of /proc/<pid>/stat, counted after the command name in
// parentheses (which may itself hold spaces and parentheses).
func processStart(pid int) (int64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	return parseProcStat(string(b))
}
