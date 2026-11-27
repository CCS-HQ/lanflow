package env

import "time"

// Level is how much a transient notification asks of the user, the same on
// the desktop and on phones (docs/notifications.md). It decides how long
// the notification shows and how it looks; where it shows is the shell's
// business (top center of the page on the desktop, under the status bar on
// iOS, above the tab bar on Android).
//
// What lasts (the core stopped, a subscription about to expire, a read-only
// setting) is no notification: the page shows it as a Banner for as long
// as it holds. What needs a decision is a dialog (Confirm).
type Level uint8

const (
	// Done answers what the user just did, when nothing on screen shows
	// it already: "已复制", "规则已保存". Brief, no action.
	Done Level = iota
	// News is something the service tells that the user did not ask for:
	// an automatic update went well, a setting was adjusted for them.
	News
	// Attention is a failure, or an outcome the user must know about: an
	// update failed, the core exited. It stays longer and offers what to
	// do next (查看详情, 查看日志, 重试).
	Attention
)

// Timeout is how long a notification of the level shows. The time stops
// while the pointer rests on the notifications or the keyboard focus is in
// them, and the user can close one at any time.
func (l Level) Timeout() time.Duration {
	switch l {
	case News:
		return 4 * time.Second
	case Attention:
		return 6 * time.Second
	}
	return 2 * time.Second
}

// Note is a transient notification.
type Note struct {
	Level Level
	// Title is the one line the notification says.
	Title string
	// Detail is a second, smaller line for Done and News. For Attention it
	// is the full text behind 「查看详情」 when Action is empty.
	Detail string
	// Action labels a button that runs OnAction and closes the note.
	Action   string
	OnAction func()
	// Key merges notes: a note whose key is showing replaces it and counts
	// how many times it came (×3). Empty means Level and Title together, so
	// the same message never stacks up.
	Key string
}

// MergeKey is the key that merges n with the notes like it.
func (n Note) MergeKey() string {
	if n.Key != "" {
		return n.Key
	}
	return string(rune('0'+n.Level)) + ":" + n.Title
}

// Note shows n through Notify, or, in an Env without one (tests that build
// a page alone), through Toast.
func (e *Env) Note(n Note) {
	switch {
	case e.Notify != nil:
		e.Notify(n)
	case e.Toast != nil:
		e.Toast(n.Title, n.Level == Attention)
	}
}
