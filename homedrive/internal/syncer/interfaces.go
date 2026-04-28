// Package syncer defines local copies of dependency interfaces so that
// this package compiles and tests independently from Phases 1-4.
package syncer

import (
	"context"
	"time"
)

// Op represents a filesystem operation type from the watcher.
type Op int

const (
	// OpCreate indicates a new file was created.
	OpCreate Op = iota + 1
	// OpWrite indicates an existing file was modified.
	OpWrite
	// OpRemove indicates a file was deleted.
	OpRemove
	// OpRename indicates a file was renamed (handled by the pairer upstream).
	OpRename
)

// String returns the human-readable operation name.
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
	default:
		return "unknown"
	}
}

// Event represents a single filesystem event from the watcher.
type Event struct {
	Path string
	Op   Op
	At   time.Time
}

// DirRename represents a paired directory rename event from the watcher.
type DirRename struct {
	From string
	To   string
	At   time.Time
}

// RemoteObject holds metadata about a remote file.
type RemoteObject struct {
	Path     string
	Size     int64
	MD5      string
	ModTime  time.Time
	RemoteID string
}

// RemoteFS abstracts the remote filesystem operations. In production this
// is backed by rclone; in tests by MemFS or FlakyFS.
type RemoteFS interface {
	CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error)
	DeleteFile(ctx context.Context, path string) error
	MoveFile(ctx context.Context, src, dst string) error
	Stat(ctx context.Context, path string) (RemoteObject, error)
}

// SyncRecord is the journal entry stored after each successful sync.
type SyncRecord struct {
	Path         string    `json:"path"`
	LocalMtime   time.Time `json:"local_mtime"`
	RemoteMtime  time.Time `json:"remote_mtime"`
	RemoteMD5    string    `json:"remote_md5"`
	RemoteID     string    `json:"remote_id"`
	LastSyncedAt time.Time `json:"last_synced_at"`
	LastOrigin   string    `json:"last_origin"` // "local" or "remote"
}

// Store abstracts the BoltDB journal for sync state tracking.
type Store interface {
	Get(path string) (*SyncRecord, error)
	Put(record SyncRecord) error
	Delete(path string) error
	RewritePrefix(oldPrefix, newPrefix string) (int, error)
}

// AuditEntry represents a single entry in the JSONL audit log.
type AuditEntry struct {
	Timestamp  time.Time `json:"ts"`
	Op         string    `json:"op"`
	Path       string    `json:"path,omitempty"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	FilesCount int       `json:"files_count,omitempty"`
	DryRun     bool      `json:"dry_run,omitempty"`
	Error      string    `json:"error,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
}

// AuditLog abstracts the JSONL audit appender.
type AuditLog interface {
	Append(entry AuditEntry) error
}

// Publisher abstracts MQTT publishing for push events.
type Publisher interface {
	PublishJSON(topic string, payload any) error
	Topic(parts ...string) string
}
