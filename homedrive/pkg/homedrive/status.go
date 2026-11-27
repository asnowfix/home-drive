package homedrive

import (
	"encoding/json"
	"fmt"
	"time"
)

// StatusSchema is the major.minor version of the status document
// published on <base>/<host>/<user>/status. See
// homedrive/docs/design/status-v2.md §5.3 (the wire contract).
//
// 2.1 adds the "guards" object and the "root_blocked" state (additive).
// 2.2 adds guards.skipped_symlinks (additive).
// 2.3 adds copy_resolved_links (additive).
const StatusSchema = "2.3"

// Status document "state" values. Consumers must treat unknown values as
// "degraded: show raw", so new values may be added in a minor release.
// StateStarting and StateError are contract values only: nothing emits
// them until #142 (startup reorder).
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StatePaused   = "paused"
	StateError    = "error"
	// StateRootBlocked: local_root is missing or not the synced tree, so
	// pull, push and bisync are paused (issue #170). Added in 2.1.
	StateRootBlocked = "root_blocked"
)

// Window names used as keys of StatusV2.Counts.
const (
	Window1h  = "1h"
	Window24h = "24h"
	Window7d  = "7d"
)

// Timestamp is a time that always marshals as RFC3339 UTC with a "Z"
// suffix and second precision (status-v2.md §5.3). Use *Timestamp for
// nullable fields: nil marshals as JSON null.
type Timestamp struct{ time.Time }

// TS returns a *Timestamp for t, or nil when t is the zero time.
func TS(t time.Time) *Timestamp {
	if t.IsZero() {
		return nil
	}
	return &Timestamp{t}
}

// MarshalJSON implements json.Marshaler.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(time.RFC3339))
}

// UnmarshalJSON implements json.Unmarshaler. It rejects JSON null: use
// *Timestamp for nullable fields (encoding/json then leaves it nil).
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("parse timestamp %q: %w", s, err)
	}
	t.Time = parsed.UTC()
	return nil
}

// Counts is one window of activity counters (bytes in B). Conflict
// handling counts in Conflicts only, never in Files*/Bytes*.
type Counts struct {
	FilesUp   int64 `json:"files_up"`
	FilesDown int64 `json:"files_down"`
	BytesUp   int64 `json:"bytes_up"`
	BytesDown int64 `json:"bytes_down"`
	Errors    int64 `json:"errors"` // all final failures, including the two below
	// ErrorsUp / ErrorsDown split Errors by direction (push side vs pull
	// side, poll failures counting as down). Ops with no direction (a
	// failed bisync pass, conflict resolution) count in Errors only.
	ErrorsUp   int64 `json:"errors_up"`
	ErrorsDown int64 `json:"errors_down"`
	Conflicts  int64 `json:"conflicts"`
	Starts     int64 `json:"starts"`
}

// LastError is the last final failure (after retries), persisted across
// restarts. Message is truncated to 256 bytes.
type LastError struct {
	TS      Timestamp `json:"ts"`
	Op      string    `json:"op"`
	Message string    `json:"message"`
}

// BisyncInfo describes the last completed bisync pass, persisted. Error
// is omitted (not null) when Result is "ok": the one exception to the
// contract's "absent is null".
type BisyncInfo struct {
	At           Timestamp `json:"at"`
	Result       string    `json:"result"` // "ok" | "error"
	DurationMs   int64     `json:"duration_ms"`
	FilesChanged int       `json:"files_changed"`
	Error        string    `json:"error,omitempty"`
}

// LocalInfo is the local tree size seen by the last bisync walk. Not
// real-time.
type LocalInfo struct {
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
	ScannedAt Timestamp `json:"scanned_at"`
}

// RootGuard reports a blocked local_root (status-v2.md §5.3, "guards").
// Reason is a stable code: missing, marker_missing, identity_mismatch or
// unreadable. It never carries a path or an error message.
type RootGuard struct {
	Reason string    `json:"reason"`
	Since  Timestamp `json:"since"`
}

// DeleteGuard reports bisync deletions held back by the mass-deletion
// guard (issue #156). Reason is a stable code: threshold, empty_local or
// empty_remote. Planned and Threshold are counts, never file names.
type DeleteGuard struct {
	Reason    string    `json:"reason"`
	Planned   int       `json:"planned"`
	Threshold int       `json:"threshold"`
	Since     Timestamp `json:"since"`
}

// SymlinkGuard reports symlinks in local_root that are skipped because
// they are never synced (issue #191). Count is a number, never file names.
type SymlinkGuard struct {
	Count int       `json:"count"`
	Since Timestamp `json:"since"`
}

// Guards describes the safety guards currently holding work back. Each
// member is null when its guard is not blocking. Added in schema 2.1.
type Guards struct {
	LocalRoot     *RootGuard   `json:"local_root"`
	BisyncDeletes *DeleteGuard `json:"bisync_deletes"`
	// SkippedSymlinks is null when no symlink is skipped. Added in 2.2.
	SkippedSymlinks *SymlinkGuard `json:"skipped_symlinks"`
}

// StatusV2 is the retained JSON document published on the status topic.
// Absent values are JSON null, never "" or the zero time.
type StatusV2 struct {
	Schema           string    `json:"schema"`
	TS               Timestamp `json:"ts"`
	PublishIntervalS int       `json:"publish_interval_s"`
	Version          string    `json:"version"`
	State            string    `json:"state"`
	StartedAt        Timestamp `json:"started_at"`
	UptimeS          int64     `json:"uptime_s"`
	DryRun           bool      `json:"dry_run"`
	PendingUp        int       `json:"pending_up"`

	LastPush     *Timestamp `json:"last_push"`
	LastPull     *Timestamp `json:"last_pull"`
	LastActivity *Timestamp `json:"last_activity"`
	LastPoll     *Timestamp `json:"last_poll"`

	// Counts always holds all three windows (1h, 24h, 7d).
	Counts       map[string]Counts `json:"counts"`
	LastError    *LastError        `json:"last_error"`
	Bisync       *BisyncInfo       `json:"bisync"`
	Local        *LocalInfo        `json:"local"`
	QuotaUsedPct *float64          `json:"quota_used_pct"`

	// Guards is always present (members null when not blocking).
	Guards Guards `json:"guards"`

	// CopyResolvedLinks is the number of symlinks.copy_resolved_links
	// patterns this host applies (0 = the default: every link is skipped).
	// The patterns themselves are in GET /status, not in this retained
	// document. Added in 2.3.
	CopyResolvedLinks int `json:"copy_resolved_links"`
}
