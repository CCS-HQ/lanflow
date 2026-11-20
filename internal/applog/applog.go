// Package applog is Lanflow's own run log (as opposed to the core's):
// lanflow.log in the log directory, rotated at 5 MB with one old file
// kept, plus the most recent entries in memory for the 日志 page.
//
// Every line is redacted before it is stored (Redact): URLs keep only
// their scheme and host (subscription links carry tokens in the path or
// query), proxy share links are masked whole, and credentials (token=,
// "password": "…", Authorization and Cookie headers) are masked.
package applog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"lanflow/internal/seqring"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"lanflow/internal/model"
)

// FileName is the log file under the log directory.
const FileName = "lanflow.log"

const (
	maxSize    = 5 << 20
	maxEntries = 1000
)

// Levels, matching the core's log levels.
const (
	Debug = "debug"
	Info  = "info"
	Warn  = "warning"
	Error = "error"
)

// Logger writes the app log. The zero value and a nil *Logger are usable
// and keep entries in memory only.
type Logger struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
	// entries is a ring of the newest maxEntries entries (allocated on the
	// first entry), numbered for incremental reads (Since).
	entries seqring.Ring[model.LogEntry]
	onWrite func()
	// retry is when to try the file again after a rotation that could
	// neither move nor empty it (zero: no such failure).
	retry time.Time
	now   func() time.Time
	errs  *ErrorLog // Error entries are also recorded here (errors.go)
}

// Open opens (creating) dir/lanflow.log for appending. When the file cannot
// be opened the logger still keeps entries in memory and err says why.
func Open(dir string) (*Logger, error) {
	l := &Logger{path: filepath.Join(dir, FileName), now: time.Now, entries: seqring.NewLazy[model.LogEntry](maxEntries)}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return l, err
	}
	return l, l.open()
}

func (l *Logger) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Path is the log file ("" for an in-memory logger).
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// OnWrite sets fn, called (outside the lock) after every entry.
func (l *Logger) OnWrite(fn func()) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.onWrite = fn
	l.mu.Unlock()
}

// SetErrors makes every Error entry also a record of errs (errors.jsonl).
func (l *Logger) SetErrors(errs *ErrorLog) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.errs = errs
	l.mu.Unlock()
}

// Logf records one entry at level.
func (l *Logger) Logf(level, format string, args ...any) {
	if l == nil {
		return
	}
	l.add(level, sprintf(format, args), true)
}

// Unrecorded is Logf for an error the caller records in errors.jsonl
// itself in a better form (a core exit with its code and log tail).
func (l *Logger) Unrecorded(level, format string, args ...any) {
	if l == nil {
		return
	}
	l.add(level, sprintf(format, args), false)
}

func sprintf(format string, args []any) string {
	if len(args) > 0 {
		return fmt.Sprintf(format, args...)
	}
	return format
}

func (l *Logger) Infof(format string, args ...any)  { l.Logf(Info, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.Logf(Warn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.Logf(Error, format, args...) }
func (l *Logger) Debugf(format string, args ...any) { l.Logf(Debug, format, args...) }

func (l *Logger) add(level, msg string, record bool) {
	msg = Redact(strings.TrimRight(msg, "\n"))
	// keep one entry per line in the file; the UI shows it on one line too
	oneLine := strings.ReplaceAll(msg, "\n", " ⏎ ")
	l.mu.Lock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	l.pushLocked(model.LogEntry{Time: now, Level: level, Payload: oneLine})
	if l.f == nil && !l.retry.IsZero() && !now.Before(l.retry) {
		l.rotateLocked()
	}
	if l.f != nil {
		line := fmt.Sprintf("%s %-7s %s\n", now.Format("2006-01-02 15:04:05.000"), strings.ToUpper(level), oneLine)
		if l.size > 0 && l.size+int64(len(line)) > maxSize {
			l.rotateLocked()
		}
		if l.f != nil {
			n, _ := l.f.WriteString(line)
			l.size += int64(n)
		}
	}
	fn, errs := l.onWrite, l.errs
	l.mu.Unlock()
	if record && level == Error && errs != nil {
		errs.Add(model.ErrorKindError, oneLine, "")
	}
	if fn != nil {
		fn()
	}
}

// pushLocked appends e to the ring, dropping the oldest entry when full.
func (l *Logger) pushLocked(e model.LogEntry) {
	if l.entries.Len() == 0 && l.entries.Seq() == 0 {
		l.entries = seqring.NewLazy[model.LogEntry](maxEntries) // a Logger made without Open
	}
	l.entries.Push(e)
}

// rotateRetry is how long the file is left alone after a failed rotation.
const rotateRetry = time.Minute

func (l *Logger) rotateLocked() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	l.retry = time.Time{}
	// A full file that can be neither moved nor emptied (held open by
	// another program on Windows) is not written to: the log must not
	// grow without bound. Entries stay in memory; the file is tried again
	// later.
	if err := Rotate(l.path); err != nil {
		now := time.Now()
		if l.now != nil {
			now = l.now()
		}
		l.retry = now.Add(rotateRetry)
		return
	}
	_ = l.open()
}

// Rotate makes room at path: the file becomes path+".1" (replacing the
// old one), or, when it cannot be renamed, is emptied. An error means the
// file is still there in full and must not be appended to.
func Rotate(path string) error {
	old := path + ".1"
	err := os.Rename(path, old)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	// The old file may be what is in the way (open elsewhere, a directory).
	_ = os.Remove(old)
	if os.Rename(path, old) == nil {
		return nil
	}
	return os.Truncate(path, 0)
}

// Entries returns the entries in memory, oldest first.
func (l *Logger) Entries() []model.LogEntry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries.Len() == 0 {
		return nil
	}
	return l.entries.OldestFirst()
}

// Since returns the entries after the cursor since (model.LogPage).
func (l *Logger) Since(since uint64, limit int) model.LogPage {
	if l == nil {
		return model.LogPage{Reset: true}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.entries.Since(since, limit)
	return model.LogPage{Entries: p.Items, Cursor: p.Cursor, Reset: p.Reset, More: p.More}
}

// Clear forgets the entries in memory (the file is kept).
func (l *Logger) Clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.entries.Reset()
	l.mu.Unlock()
}

// Close closes the file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.retry = time.Time{}
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Writer adapts the logger for the standard log package: each write is
// one info entry ("lanflow: " prefixes and timestamps are dropped).
func (l *Logger) Writer() io.Writer { return stdWriter{l} }

type stdWriter struct{ l *Logger }

var stdPrefix = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? )?(lanflow: )?`)

func (w stdWriter) Write(p []byte) (int, error) {
	msg := stdPrefix.ReplaceAllString(strings.TrimRight(string(p), "\n"), "")
	level := Info
	if lower := strings.ToLower(msg); strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(msg, "失败") {
		level = Warn
	}
	w.l.add(level, msg, true)
	return len(p), nil
}
