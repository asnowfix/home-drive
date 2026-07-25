// types.go defines bisync-specific interfaces, types, and sentinel errors.
// Shared types (RemoteObject, RemoteFS, JournalEntry, AuditEntry, etc.) live
// in ifaces.go; this file contains only what is unique to the bisync path.
package syncer

import (
	"errors"
	"io"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
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
// Bisync-specific interfaces
// ---------------------------------------------------------------------------

// Journal abstracts the BoltDB store for bisync state.
// It is context-free and lighter than Store, which includes page-token and
// push-syncer methods.
type Journal interface {
	Get(path string) (*JournalEntry, error)
	Put(entry JournalEntry) error
	Exists(path string) bool
}

// AuditWriter abstracts the JSONL audit log writer used by bisync.
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

	// Matcher controls the .old.<N> suffix format used when naming
	// conflict losers. Defaults to the default ".old.%d" format if nil
	// (see oldsuffix.New). Set from config.ConflictCfg.OldSuffixFormat
	// during wiring (cmd/homedrive/agent.go).
	Matcher *oldsuffix.Matcher
}

// ---------------------------------------------------------------------------
// BisyncAuditEntry is a single JSONL line in the bisync audit log.
// Per-file push/pull audit entries use AuditEntry from ifaces.go.
// ---------------------------------------------------------------------------

type BisyncAuditEntry struct {
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
