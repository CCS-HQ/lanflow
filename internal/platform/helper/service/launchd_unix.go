//go:build !windows

package service

//lint:file-ignore U1000 used by the macOS helper (darwin.go); built on every unix so its tests run there too.

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// waitGone polls until pid has ended or d has passed.
func waitGone(ctx context.Context, p procs, pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for p.alive(pid) {
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(p.poll):
		}
	}
	return true
}

// launchdPID reads the "pid = N" line of launchctl print: the running
// process of the service, 0 when there is none.
func launchdPID(out string) int {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(k) != "pid" {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 1 {
			return n
		}
	}
	return 0
}

// logSize is the size of the helper log, where the new helper's lines
// start.
func logSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// logTail returns the last n non-empty lines written to path after
// offset start (at most the last 8 KiB), joined on one line: the error
// travels through osascript, which keeps only one line of it.
func logTail(path string, start int64, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	size := fi.Size()
	if size < start {
		start = 0 // rotated or truncated
	}
	const max = 8 << 10
	if size-start > max {
		start = size - max
	}
	b := make([]byte, size-start)
	if _, err := f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// removeOwnSocket removes path when it is still the file own describes.
func removeOwnSocket(path string, own os.FileInfo) {
	if own == nil {
		return
	}
	if fi, err := os.Lstat(path); err == nil && os.SameFile(fi, own) {
		_ = os.Remove(path)
	}
}
