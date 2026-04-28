// types.go defines the interfaces, types, and sentinel errors used by the
// bisync safety net and (in future phases) the push/pull syncer.
package syncer

import (
	"context"
	"errors"
	"io"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrBisyncCanceled is returned when bisync is canceled via context.
var ErrBisyncCanceled = errors.New("bisync canceled")

// ErrBisyncRunning is returned when a force trigger arrives while
// bisync is already executing.
var ErrBisyncRunning = errors.New("bisync already running")

// ---------------------------------------------------------------------------
// Local interfaces -- defined here so the package compiles independently
// of the other phase implementations. Production wiring injects the
// concrete types.
// ---------------------------------------------------------------------------

// RemoteObject represents metadata about a file on the remote side.
type RemoteObject struct {
	Path    string
	Size    int64
	MD5     string
	ModTime time.Time
}

// RemoteFS abstracts the remote filesystem (Google Drive via rclone).
// Tests inject MemFS; production injects RcloneFS.
type RemoteFS interface {
	CopyFile(ctx context.Context, src, dstDir string) (RemoteObject, error)
	DeleteFile(ctx context.Context, path string) error
	MoveFile(ctx context.Context, src, dst string) error
	Stat(ctx context.Context, path string) (RemoteObject, error)
	List(ctx context.Context, dir string) ([]RemoteObject, error)
}

// JournalEntry records the last-known sync state of a file.
type JournalEntry struct {
	Path         string    `json:"path"`
	LocalMtime   time.Time `json:"local_mtime"`
	RemoteMtime  time.Time `json:"remote_mtime"`
	RemoteMD5    string    `json:"remote_md5"`
	RemoteID     string    `json:"remote_id"`
	LastSyncedAt time.Time `json:"last_synced_at"`
	LastOrigin   string    `json:"last_origin"` // "local" | "remote"
}

// Journal abstracts the BoltDB store for sync state.
type Journal interface {
	Get(path string) (*JournalEntry, error)
	Put(entry JournalEntry) error
	Exists(path string) bool
}

// EventPublisher abstracts MQTT publishing for bisync events.
type EventPublisher interface {
	PublishJSON(topic string, payload any) error
	Topic(parts ...string) string
}

// AuditWriter abstracts the JSONL audit log.
type AuditWriter interface {
	io.Writer
}

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) (<-chan time.Time, func())
	After(d time.Duration) <-chan time.Time
}

// realClock implements Clock using the standard library.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

func (realClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// BisyncConfig holds configuration for the bisync safety net.
type BisyncConfig struct {
	Interval  time.Duration // default 1h
	LocalRoot string        // absolute path to the local sync root
	DryRun    bool          // if true, detect but do not sync
}

// ---------------------------------------------------------------------------
// Audit log entry
// ---------------------------------------------------------------------------

// AuditEntry represents a single JSONL line in the audit log.
type AuditEntry struct {
	Timestamp    string `json:"ts"`
	Op           string `json:"op"`
	Duration     string `json:"duration_ms,omitempty"`
	FilesChanged int    `json:"files_changed"`
	FilesPushed  int    `json:"files_pushed"`
	FilesPulled  int    `json:"files_pulled"`
	Conflicts    int    `json:"conflicts"`
	DryRun       bool   `json:"dry_run,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// MQTT event payloads
// ---------------------------------------------------------------------------

// BisyncEvent is published when bisync starts or completes.
type BisyncEvent struct {
	Timestamp    string `json:"ts"`
	Type         string `json:"type"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	FilesChanged int    `json:"files_changed,omitempty"`
	Conflicts    int    `json:"conflicts,omitempty"`
	DryRun       bool   `json:"dry_run,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Diff result
// ---------------------------------------------------------------------------

// DiffKind describes the type of drift detected.
type DiffKind int

const (
	// DiffLocalOnly means the file exists locally but not remotely.
	DiffLocalOnly DiffKind = iota
	// DiffRemoteOnly means the file exists remotely but not locally.
	DiffRemoteOnly
	// DiffConflict means both sides differ from journal expectations.
	DiffConflict
)

// FileDiff represents one file that differs between local and remote.
type FileDiff struct {
	Path       string
	Kind       DiffKind
	LocalInfo  *LocalFileInfo // nil if DiffRemoteOnly
	RemoteInfo *RemoteObject  // nil if DiffLocalOnly
}

// LocalFileInfo holds local file metadata.
type LocalFileInfo struct {
	Path    string
	Size    int64
	ModTime time.Time
}
