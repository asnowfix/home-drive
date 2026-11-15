// Package stats tracks windowed (1h/24h/7d) activity counters and the
// "last ..." timestamps shown in the status document (status-v2.md §5).
//
// Counters live in 5-minute buckets kept in memory and persisted in Bolt
// via a Persister (internal/store.Journal), so they survive restarts. The
// Tracker implements syncer.Recorder.
package stats

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/pkg/homedrive"
)

// MaxErrorMessage is the maximum byte length of last_error.message.
const MaxErrorMessage = 256

// Persister stores buckets and meta values. *store.Journal implements it.
type Persister interface {
	SaveStats(puts map[string][]byte, deleteBefore string, meta map[string]string) error
	LoadStats() (buckets map[string][]byte, meta map[string]string, err error)
}

// Config configures a Tracker.
type Config struct {
	// Store persists the counters; nil keeps them in memory only.
	Store Persister
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
	// Log receives flush failures; defaults to slog.Default().
	Log *slog.Logger
}

// Snapshot is a point-in-time view of the tracker.
type Snapshot struct {
	Counts       map[string]homedrive.Counts
	LastPush     time.Time
	LastPull     time.Time
	LastPoll     time.Time
	LastActivity time.Time // max(LastPush, LastPull)
	LastError    *homedrive.LastError
	Bisync       *homedrive.BisyncInfo
	Local        *homedrive.LocalInfo
}

// Tracker accumulates outcomes. All methods are safe for concurrent use,
// and the Record* methods are no-ops on a nil *Tracker (so a nil tracker
// stored in a syncer.Recorder interface is harmless).
type Tracker struct {
	cfg Config

	flushMu   sync.Mutex // serialises Flush; never held with mu across I/O
	mu        sync.Mutex
	buckets   map[time.Time]*bucket
	dirty     map[time.Time]struct{}
	metaDirty bool

	lastPush, lastPull, lastPoll time.Time
	lastErr                      *homedrive.LastError
	bisync                       *homedrive.BisyncInfo
	local                        *homedrive.LocalInfo
}

var _ syncer.Recorder = (*Tracker)(nil)

// New creates an empty Tracker. Call Load to restore persisted state.
func New(cfg Config) *Tracker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Tracker{
		cfg:     cfg,
		buckets: make(map[time.Time]*bucket),
		dirty:   make(map[time.Time]struct{}),
	}
}

// at returns t, or the current time when t is zero.
func (t *Tracker) at(ts time.Time) time.Time {
	if ts.IsZero() {
		return t.cfg.Now()
	}
	return ts
}

// bucketFor returns (creating if needed) the bucket containing ts and
// marks it dirty. Callers hold t.mu.
func (t *Tracker) bucketFor(ts time.Time) *bucket {
	start := bucketStart(ts)
	b := t.buckets[start]
	if b == nil {
		b = &bucket{}
		t.buckets[start] = b
	}
	t.dirty[start] = struct{}{}
	return b
}

// RecordStart counts one process start in the current bucket.
func (t *Tracker) RecordStart() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.bucketFor(t.cfg.Now()).Starts++
}

// Record implements syncer.Recorder.
func (t *Tracker) Record(o syncer.Outcome) {
	if t == nil {
		return
	}
	if errors.Is(o.Err, context.Canceled) || errors.Is(o.Err, syncer.ErrDanglingShortcut) {
		return
	}
	at := t.at(o.At)

	t.mu.Lock()
	defer t.mu.Unlock()

	if o.Op == syncer.OutBisync {
		t.recordBisync(o, at)
	}
	if o.Err != nil {
		b := t.bucketFor(at)
		b.Errors++
		switch errDirection(o) {
		case syncer.DirUp:
			b.ErrorsUp++
		case syncer.DirDown:
			b.ErrorsDn++
		}
		if o.Op == syncer.OutConflict {
			t.bucketFor(at).Conflicts++
		}
		t.setLastError(o, at)
		return
	}
	t.recordSuccess(o, at)
}

// recordSuccess applies a successful outcome. Callers hold t.mu.
func (t *Tracker) recordSuccess(o syncer.Outcome, at time.Time) {
	switch o.Dir {
	case syncer.DirUp:
		b := t.bucketFor(at)
		b.FilesUp++
		b.BytesUp += o.Bytes
	case syncer.DirDown:
		b := t.bucketFor(at)
		b.FilesDown++
		b.BytesDown += o.Bytes
	}
	switch o.Op {
	case syncer.OutPush, syncer.OutPushDelete, syncer.OutDirRename, syncer.OutBisyncPush:
		t.lastPush = maxTime(t.lastPush, at)
	case syncer.OutPull, syncer.OutPullDelete, syncer.OutBisyncPull:
		t.lastPull = maxTime(t.lastPull, at)
	case syncer.OutPoll:
		t.lastPoll = maxTime(t.lastPoll, at)
	case syncer.OutConflict:
		t.bucketFor(at).Conflicts++
	}
	t.metaDirty = true
}

// recordBisync stores the last pass summary. Callers hold t.mu.
func (t *Tracker) recordBisync(o syncer.Outcome, at time.Time) {
	info := &homedrive.BisyncInfo{
		At:           homedrive.Timestamp{Time: at.UTC()},
		Result:       "ok",
		DurationMs:   o.Duration.Milliseconds(),
		FilesChanged: o.Files,
	}
	if o.Err != nil {
		info.Result = "error"
		info.Error = truncate(o.Err.Error())
	}
	t.bisync = info
	t.metaDirty = true
}

// setLastError remembers a final failure. Callers hold t.mu.
func (t *Tracker) setLastError(o syncer.Outcome, at time.Time) {
	t.lastErr = &homedrive.LastError{
		TS:      homedrive.Timestamp{Time: at.UTC()},
		Op:      o.Op,
		Message: truncate(o.Err.Error()),
	}
	t.metaDirty = true
}

// RecordLocalTree implements syncer.Recorder.
func (t *Tracker) RecordLocalTree(files int, bytes int64, at time.Time) {
	if t == nil {
		return
	}
	at = t.at(at)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.local = &homedrive.LocalInfo{
		Files: files, Bytes: bytes, ScannedAt: homedrive.Timestamp{Time: at.UTC()},
	}
	t.metaDirty = true
}

// Snapshot returns the windowed counts and last-* values as of now.
func (t *Tracker) Snapshot(now time.Time) Snapshot {
	if t == nil {
		return Snapshot{Counts: sumWindows(nil, now)}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Snapshot{
		Counts:       sumWindows(t.buckets, now),
		LastPush:     t.lastPush,
		LastPull:     t.lastPull,
		LastPoll:     t.lastPoll,
		LastActivity: maxTime(t.lastPush, t.lastPull),
	}
	if t.lastErr != nil {
		e := *t.lastErr
		s.LastError = &e
	}
	if t.bisync != nil {
		b := *t.bisync
		s.Bisync = &b
	}
	if t.local != nil {
		l := *t.local
		s.Local = &l
	}
	return s
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// truncate cuts s to MaxErrorMessage bytes on a rune boundary.
func truncate(s string) string {
	if len(s) <= MaxErrorMessage {
		return s
	}
	s = s[:MaxErrorMessage]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// errDirection classifies a failed outcome as push side (DirUp), pull
// side (DirDown) or neither ("").
func errDirection(o syncer.Outcome) string {
	switch o.Op {
	case syncer.OutPush, syncer.OutPushDelete, syncer.OutDirRename, syncer.OutBisyncPush:
		return syncer.DirUp
	case syncer.OutPull, syncer.OutPullDelete, syncer.OutBisyncPull, syncer.OutPoll:
		return syncer.DirDown
	}
	return ""
}
