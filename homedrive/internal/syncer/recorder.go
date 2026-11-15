// recorder.go defines the optional Recorder hook through which the push,
// pull and bisync paths report one Outcome per *final* result (after
// retries) to a statistics tracker (internal/stats). It is a
// consumer-defined interface so this package never imports the tracker.
package syncer

import (
	"context"
	"errors"
	"time"
)

// Outcome Op values.
const (
	OutPush       = "push"
	OutPushDelete = "push_delete"
	OutDirRename  = "dir_rename"
	OutPull       = "pull"
	OutPullDelete = "pull_delete"
	OutConflict   = "conflict"
	OutBisyncPush = "bisync_push"
	OutBisyncPull = "bisync_pull"
	OutPoll       = "poll"
	OutBisync     = "bisync"
)

// Outcome Dir values. Only content transfers carry a direction; they are
// the only outcomes counted as files_up / files_down.
const (
	DirUp   = "up"
	DirDown = "down"
)

// Outcome is the final result of one sync operation.
type Outcome struct {
	// Op is one of the Out* constants above.
	Op string
	// Dir is DirUp, DirDown or "" (not a content transfer).
	Dir string
	// Path is the root-relative file path, when there is one.
	Path string
	// Bytes transferred (content transfers only).
	Bytes int64
	// Files is the number of files changed (OutBisync only).
	Files int
	// Duration of the operation (OutBisync only).
	Duration time.Duration
	// Err is the final failure after retries; nil means success.
	Err error
	// At is when the operation finished.
	At time.Time
}

// Recorder receives outcomes. A nil Recorder is a no-op everywhere it is
// accepted.
type Recorder interface {
	Record(Outcome)
	// RecordLocalTree reports the size of the local tree seen by the last
	// bisync walk (excludes applied).
	RecordLocalTree(files int, bytes int64, at time.Time)
}

// countableErr reports whether err is a real failure to count: a
// cancelled context and a dangling Drive shortcut skip are not.
func countableErr(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, ErrDanglingShortcut)
}

// emitOutcome forwards o to rec, dropping non-countable errors' failure
// status (they are reported as nothing at all). Safe on a nil rec.
func emitOutcome(rec Recorder, o Outcome) {
	if rec == nil {
		return
	}
	if o.Err != nil && !countableErr(o.Err) {
		return
	}
	rec.Record(o)
}
