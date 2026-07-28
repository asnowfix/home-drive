package store

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// ConflictPolicy defines how conflicts are resolved.
type ConflictPolicy string

const (
	PolicyNewerWins  ConflictPolicy = "newer_wins"
	PolicyLocalWins  ConflictPolicy = "local_wins"
	PolicyRemoteWins ConflictPolicy = "remote_wins"
)

// ConflictSide identifies which side won or lost a conflict.
type ConflictSide string

const (
	SideLocal  ConflictSide = "local"
	SideRemote ConflictSide = "remote"
)

// Sentinel errors for conflict resolution.
var (
	ErrConflict      = errors.New("store: conflict detected")
	ErrRenameFailed  = errors.New("store: loser rename failed")
	ErrUnknownPolicy = errors.New("store: unknown conflict policy")
)

// ConflictResult describes the outcome of a conflict resolution.
type ConflictResult struct {
	Winner    ConflictSide
	LoserSide ConflictSide
	OldPath   string // path the loser was renamed to (e.g. "file.txt.old.3")
	Warning   string // non-empty if equal-mtime case triggered
}

// ConflictInput provides the data needed to resolve a conflict.
type ConflictInput struct {
	Path        string
	LocalMtime  time.Time
	RemoteMtime time.Time
	LocalMD5    string
	RemoteMD5   string
}

// ConflictResolver resolves file conflicts using the journal to track
// .old.<N> suffixes.
type ConflictResolver struct {
	journal *Journal
	auditor *Auditor
	logger  *slog.Logger
	policy  ConflictPolicy
	// OldSuffixFormat controls the suffix pattern. Default: ".old.%d"
	OldSuffixFormat string
}

// NewConflictResolver creates a resolver bound to the given journal.
func NewConflictResolver(journal *Journal, auditor *Auditor, logger *slog.Logger, policy ConflictPolicy) *ConflictResolver {
	return &ConflictResolver{
		journal:         journal,
		auditor:         auditor,
		logger:          logger,
		policy:          policy,
		OldSuffixFormat: ".old.%d",
	}
}

// Resolve determines the winner and computes the .old.<N> path for the
// loser. It does NOT perform the actual file rename or upload/download;
// the caller (syncer) is responsible for that. It DOES record the .old.<N>
// entry in the journal so that N increments correctly on subsequent conflicts.
func (r *ConflictResolver) Resolve(input ConflictInput) (ConflictResult, error) {
	winner, warning, err := r.determineWinner(input)
	if err != nil {
		return ConflictResult{}, err
	}

	loser := SideRemote
	if winner == SideRemote {
		loser = SideLocal
	}

	_, oldPath := r.nextOldPath(input.Path)

	// Record the .old.<N> path in the journal so future conflicts
	// see it when computing the next N.
	oldEntry := JournalEntry{
		Path:         oldPath,
		LastSyncedAt: time.Now(),
		LastOrigin:   string(loser),
	}
	if loser == SideLocal {
		oldEntry.LocalMtime = input.LocalMtime
	} else {
		oldEntry.RemoteMtime = input.RemoteMtime
		oldEntry.RemoteMD5 = input.RemoteMD5
	}
	if err := r.journal.Put(oldEntry); err != nil {
		return ConflictResult{}, fmt.Errorf("store: record old entry %q: %w", oldPath, err)
	}

	result := ConflictResult{
		Winner:    winner,
		LoserSide: loser,
		OldPath:   oldPath,
		Warning:   warning,
	}

	// Audit log
	resolution := fmt.Sprintf("newer_wins:%s", winner)
	if warning != "" {
		resolution = fmt.Sprintf("equal_mtime:%s", winner)
	}

	r.logger.Info("conflict resolved",
		"path", input.Path,
		"op", "conflict",
		"winner", string(winner),
		"loser_side", string(loser),
		"old_path", oldPath,
		"resolution", resolution,
	)

	if r.auditor != nil {
		r.auditor.Log(AuditEntry{
			Op:         "conflict",
			Path:       input.Path,
			Resolution: resolution,
			OldPath:    oldPath,
		})
	}

	return result, nil
}

// determineWinner applies the conflict policy to decide which side wins.
func (r *ConflictResolver) determineWinner(input ConflictInput) (ConflictSide, string, error) {
	switch r.policy {
	case PolicyLocalWins:
		return SideLocal, "", nil
	case PolicyRemoteWins:
		return SideRemote, "", nil
	case PolicyNewerWins:
		return r.newerWins(input)
	default:
		return "", "", fmt.Errorf("%w: %q", ErrUnknownPolicy, r.policy)
	}
}

// newerWins implements the three cases from PLAN.md 11.2.
func (r *ConflictResolver) newerWins(input ConflictInput) (ConflictSide, string, error) {
	switch {
	case input.LocalMtime.After(input.RemoteMtime):
		// Case 1: local is newer
		return SideLocal, "", nil

	case input.RemoteMtime.After(input.LocalMtime):
		// Case 2: remote is newer
		return SideRemote, "", nil

	default:
		// Case 3: equal mtime, checksums differ
		warning := fmt.Sprintf(
			"equal mtime (%s) but checksums differ: local=%s remote=%s; defaulting to local wins",
			input.LocalMtime.Format(time.RFC3339),
			input.LocalMD5,
			input.RemoteMD5,
		)
		r.logger.Warn("conflict with equal mtime",
			"path", input.Path,
			"op", "conflict",
			"local_mtime", input.LocalMtime,
			"remote_mtime", input.RemoteMtime,
			"local_md5", input.LocalMD5,
			"remote_md5", input.RemoteMD5,
		)
		return SideLocal, warning, nil
	}
}

// matcher compiles r.OldSuffixFormat into an oldsuffix.Matcher, falling
// back to the default ".old.%d" format if OldSuffixFormat is empty or
// invalid. OldSuffixFormat is normally validated once at config-load
// time (internal/config.Load); this fallback only matters for a caller
// that mutates the exported field directly after construction.
func (r *ConflictResolver) matcher() *oldsuffix.Matcher {
	m, err := oldsuffix.New(r.OldSuffixFormat)
	if err != nil {
		m, _ = oldsuffix.New("")
	}
	return m
}

// nextOldPath computes the base path a new conflict loser should hang
// off, and the .old.<N> path to actually use, delegating to
// oldsuffix.NextOldN so an already-suffixed path collapses onto its
// tracked base instead of nesting a new suffix on top (issue #65; see
// the homedrive-conflict-resolution skill and PLAN.md §11.2/§11.5).
func (r *ConflictResolver) nextOldPath(path string) (base, oldPath string) {
	m := r.matcher()
	base, n := oldsuffix.NextOldN(m, path, r.journal.Exists)
	return base, m.Format(base, n)
}

// NextOldPath computes the next .old.<N> path for a given base path
// without recording it. Useful for callers that need to preview.
func (r *ConflictResolver) NextOldPath(path string) string {
	_, oldPath := r.nextOldPath(path)
	return oldPath
}
