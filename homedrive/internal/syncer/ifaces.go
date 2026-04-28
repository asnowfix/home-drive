// Package syncer implements the push/pull sync engine with conflict
// resolution, exponential backoff retry, and bisync safety net.
//
// This file defines local interfaces for external dependencies so the
// syncer compiles and tests independently of rcloneclient, store, and mqtt.
package syncer

import (
	"context"
	"time"
)

// RemoteFS is the subset of rcloneclient.RemoteFS used by the puller.
// Tests supply a mock; production wires in the real rclone wrapper.
type RemoteFS interface {
	CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error)
	DeleteFile(ctx context.Context, path string) error
	MoveFile(ctx context.Context, src, dst string) error
	Stat(ctx context.Context, path string) (RemoteObject, error)
	ListChanges(ctx context.Context, pageToken string) (Changes, error)
	GetStartPageToken(ctx context.Context) (string, error)
	DownloadFile(ctx context.Context, remotePath, localPath string) error
}

// RemoteObject describes a file on the remote side.
type RemoteObject struct {
	Path    string
	Size    int64
	MD5     string
	ModTime time.Time
	ID      string
}

// Change represents a single change reported by the Drive Changes API.
type Change struct {
	Path    string
	Deleted bool
	Object  RemoteObject
}

// Changes is the result of a ListChanges call.
type Changes struct {
	Items         []Change
	NextPageToken string
}

// JournalEntry records the last-known sync state for a file path.
type JournalEntry struct {
	Path         string
	LocalMtime   time.Time
	RemoteMtime  time.Time
	RemoteMD5    string
	RemoteID     string
	LastSyncedAt time.Time
	LastOrigin   string // "local" | "remote"
}

// Store is the subset of store.Store used by the puller for journal
// operations and page token persistence.
type Store interface {
	GetPageToken(ctx context.Context) (string, error)
	SetPageToken(ctx context.Context, token string) error
	Get(ctx context.Context, path string) (JournalEntry, bool, error)
	Put(ctx context.Context, entry JournalEntry) error
	Delete(ctx context.Context, path string) error
	NextOldN(ctx context.Context, path string) (int, error)
}

// AuditLogger appends structured audit entries to the JSONL log.
type AuditLogger interface {
	Log(entry AuditEntry) error
}

// AuditEntry is a single line in the audit log.
type AuditEntry struct {
	Timestamp  time.Time `json:"ts"`
	Op         string    `json:"op"`
	Path       string    `json:"path"`
	Origin     string    `json:"origin,omitempty"`
	Bytes      int64     `json:"bytes,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Resolution string    `json:"resolution,omitempty"`
	OldPath    string    `json:"old_path,omitempty"`
	DryRun     bool      `json:"dry_run,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Publisher is the subset of mqtt.Publisher used by the puller to emit
// events. Tests supply a recording mock.
type Publisher interface {
	PublishJSON(topic string, payload any) error
	Topic(parts ...string) string
}
