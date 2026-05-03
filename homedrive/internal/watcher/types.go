// Package watcher provides recursive fsnotify file watching with per-path
// debouncing and directory rename pairing via inotify cookies.
package watcher

import (
	"time"
)

// Op describes the type of file system operation observed.
type Op int

const (
	// OpCreate indicates a new file or directory was created.
	OpCreate Op = iota + 1
	// OpWrite indicates a file was modified.
	OpWrite
	// OpRemove indicates a file or directory was removed.
	OpRemove
	// OpRename indicates a file or directory was renamed (unpaired).
	OpRename
	// OpDirRename indicates a paired directory rename was detected.
	OpDirRename
)

// String returns the human-readable name of the operation.
func (o Op) String() string {
	switch o {
	case OpCreate:
		return "create"
	case OpWrite:
		return "write"
	case OpRemove:
		return "remove"
	case OpRename:
		return "rename"
	case OpDirRename:
		return "dir_rename"
	default:
		return "unknown"
	}
}

// Event represents a single file system event after debouncing and filtering.
type Event struct {
	Path string
	Op   Op
	At   time.Time
}

// DirRename represents a paired directory rename detected via inotify cookies.
// This collapses what would be thousands of child events into a single event,
// enabling one Drive API call instead of thousands.
type DirRename struct {
	From string
	To   string
	At   time.Time
}

// WatchEvent is a union type sent on the watcher's output channel.
// Exactly one of Event or DirRename is non-nil.
type WatchEvent struct {
	Event     *Event
	DirRename *DirRename
}

// SyncRecord holds the sync state for a file, used by the inode/mtime guard
// to suppress self-induced echoes from recent pulls. This is the minimal
// interface the watcher needs from the store -- the full record lives in
// internal/store/.
type SyncRecord struct {
	LocalMtime time.Time
	Size       int64
}

// SyncStore is the interface the watcher uses to query last sync state.
// The real implementation lives in internal/store/; tests provide a stub.
type SyncStore interface {
	// GetSyncRecord returns the last known sync record for a path.
	// Returns nil if no record exists.
	GetSyncRecord(path string) *SyncRecord
}

// Config holds watcher configuration.
type Config struct {
	// LocalRoot is the root directory to watch recursively.
	LocalRoot string

	// Debounce is the per-path debounce window. Default 2s.
	Debounce time.Duration

	// DirRenamePairWindow is the time window for matching rename cookies.
	// Default 500ms.
	DirRenamePairWindow time.Duration

	// Exclude is a list of doublestar glob patterns to exclude.
	Exclude []string
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() Config {
	return Config{
		Debounce:            2 * time.Second,
		DirRenamePairWindow: 500 * time.Millisecond,
	}
}
