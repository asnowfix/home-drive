// Package syncer implements the push/pull sync engine with conflict
// resolution, exponential backoff retry, and bisync safety net.
//
// This file re-exports the canonical RemoteFS (internal/rcloneclient) and
// Store (internal/store) interfaces and their associated data types as
// local aliases, so the rest of this package can keep using the short,
// unqualified names (RemoteObject, JournalEntry, ...) while there is
// exactly one real definition of each -- see the homedrive-test-mocks
// skill and issue #51. Publisher stays a small, syncer-local interface:
// it is a strict two-method subset of mqtt.Publisher using only
// primitive types, so there is no struct-shape drift risk in keeping it
// narrow (Go idiom: consumer-defined interfaces).
package syncer

import (
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
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

// RemoteFS is the canonical remote-filesystem interface, defined in
// internal/rcloneclient. Aliased here so the rest of this package can
// keep referring to the short name "RemoteFS".
type RemoteFS = rcloneclient.RemoteFS

// RemoteObject describes a file on the remote side. Alias of
// rcloneclient.RemoteObject (see package doc above).
type RemoteObject = rcloneclient.RemoteObject

// Change represents a single change reported by the Drive Changes API.
// Alias of rcloneclient.Change.
type Change = rcloneclient.Change

// Changes is the result of a ListChanges call. Alias of
// rcloneclient.Changes.
type Changes = rcloneclient.Changes

// JournalEntry records the last-known sync state for a file path. Alias
// of store.JournalEntry.
type JournalEntry = store.JournalEntry

// Quota holds remote storage usage information. Alias of
// rcloneclient.Quota.
type Quota = rcloneclient.Quota

// Store is the canonical sync-state persistence interface, defined in
// internal/store. Aliased here so the rest of this package can keep
// referring to the short name "Store".
type Store = store.Store

// RetentionPolicy bounds .old.<N> conflict-loser retention (PLAN.md
// §11.5). Alias of store.RetentionPolicy.
type RetentionPolicy = store.RetentionPolicy

// Auditor is the JSONL audit-log writer used by the retention GC to
// record "conflict_gc" entries. Alias of store.Auditor.
type Auditor = store.Auditor

// AuditLogger appends structured audit entries to the JSONL log.
type AuditLogger interface {
	Log(entry AuditEntry) error
}

// AuditEntry is a single line in the audit log.
type AuditEntry struct {
	Timestamp  time.Time `json:"ts"`
	Op         string    `json:"op"`
	Path       string    `json:"path,omitempty"`
	Origin     string    `json:"origin,omitempty"`
	Bytes      int64     `json:"bytes,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Resolution string    `json:"resolution,omitempty"`
	OldPath    string    `json:"old_path,omitempty"`
	DryRun     bool      `json:"dry_run,omitempty"`
	Error      string    `json:"error,omitempty"`
	// Push-syncer fields for directory rename audit entries.
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	FilesCount int    `json:"files_count,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
}

// Publisher is the subset of mqtt.Publisher used by the puller to emit
// events. Tests supply a recording mock.
type Publisher interface {
	PublishJSON(topic string, payload any) error
	Topic(parts ...string) string
}
