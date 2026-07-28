package store

import (
	"context"
	"errors"
	"log/slog"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// Store is the canonical interface for sync-state persistence consumed by
// the push/pull sync engine (internal/syncer). It wraps the context-free
// *Journal API (bbolt itself is synchronous) with context parameters, for
// consistency with the rest of the sync engine's call signatures.
// JournalStore is the sole production implementation; tests use an
// in-memory double.
//
// This is the single home for the "Store" contract: internal/syncer
// imports this interface directly rather than declaring its own local
// copy, so a mock that satisfies syncer's needs is guaranteed to also
// satisfy any other consumer's.
type Store interface {
	// GetPageToken retrieves the persisted Drive Changes API page token.
	GetPageToken(ctx context.Context) (string, error)

	// SetPageToken persists the Drive Changes API page token.
	SetPageToken(ctx context.Context, token string) error

	// Get retrieves the journal entry for path. found is false (with a
	// nil error) if no entry exists.
	Get(ctx context.Context, path string) (JournalEntry, bool, error)

	// Put writes or overwrites the journal entry for entry.Path.
	Put(ctx context.Context, entry JournalEntry) error

	// Delete removes the journal entry for path.
	Delete(ctx context.Context, path string) error

	// NextOldN returns the base path a new conflict loser should hang off,
	// and the smallest positive N such that "<base><suffix(N)>" has no
	// journal entry. See oldsuffix.NextOldN for the collapsing algorithm
	// that fixes issue #65.
	NextOldN(ctx context.Context, path string) (base string, n int, err error)

	// RewritePrefix renames all journal paths under oldPrefix to
	// newPrefix, used by the push syncer when a directory is renamed.
	RewritePrefix(ctx context.Context, oldPrefix, newPrefix string) (int, error)

	// ListOldSiblings returns every journal entry whose path starts with
	// prefix. Used by the retention GC (PLAN.md §11.5) to bound its work
	// to one prefix scan per base file instead of a full-journal walk.
	ListOldSiblings(ctx context.Context, prefix string) ([]JournalEntry, error)

	// ForEach calls fn for every journal entry. Used by the periodic
	// retention sweep (PLAN.md §11.5).
	ForEach(ctx context.Context, fn func(JournalEntry) error) error
}

// JournalStore adapts *Journal to the context-aware Store interface.
type JournalStore struct {
	J   *Journal
	Log *slog.Logger

	// Matcher controls the .old.<N> suffix format used by NextOldN. Nil
	// falls back to the default ".old.%d" format (see Journal.NextOldN).
	// Set this from the compiled config.ConflictCfg.OldSuffixFormat
	// matcher during wiring (cmd/homedrive/agent.go) -- keep it the same
	// *oldsuffix.Matcher instance used to build any other Matcher-typed
	// field (e.g. syncer.PullerConfig.Matcher, syncer.BisyncConfig.Matcher)
	// derived from the same config value, so collapsing decisions agree.
	Matcher *oldsuffix.Matcher
}

// NewJournalStore creates a JournalStore wrapping j.
func NewJournalStore(j *Journal, log *slog.Logger) *JournalStore {
	return &JournalStore{J: j, Log: log}
}

// GetPageToken implements Store.
func (s *JournalStore) GetPageToken(_ context.Context) (string, error) {
	return s.J.GetPageToken()
}

// SetPageToken implements Store.
func (s *JournalStore) SetPageToken(_ context.Context, token string) error {
	return s.J.SetPageToken(token)
}

// Get implements Store.
func (s *JournalStore) Get(_ context.Context, path string) (JournalEntry, bool, error) {
	e, err := s.J.Get(path)
	if errors.Is(err, ErrNotFound) {
		return JournalEntry{}, false, nil
	}
	if err != nil {
		return JournalEntry{}, false, err
	}
	return e, true, nil
}

// Put implements Store.
func (s *JournalStore) Put(_ context.Context, entry JournalEntry) error {
	return s.J.Put(entry)
}

// Delete implements Store.
func (s *JournalStore) Delete(_ context.Context, path string) error {
	return s.J.Delete(path)
}

// NextOldN implements Store.
func (s *JournalStore) NextOldN(_ context.Context, path string) (string, int, error) {
	base, n := s.J.NextOldN(s.Matcher, path)
	return base, n, nil
}

// RewritePrefix implements Store. The audit trail for directory renames
// is emitted separately by the syncer via AuditLogger, so no *Auditor is
// threaded through here (matching the pre-existing adapter behavior).
func (s *JournalStore) RewritePrefix(_ context.Context, oldPrefix, newPrefix string) (int, error) {
	return RewritePrefix(s.J, oldPrefix, newPrefix, nil, s.Log)
}

// ListOldSiblings implements Store.
func (s *JournalStore) ListOldSiblings(_ context.Context, prefix string) ([]JournalEntry, error) {
	return s.J.ListByPrefix(prefix)
}

// ForEach implements Store.
func (s *JournalStore) ForEach(_ context.Context, fn func(JournalEntry) error) error {
	return s.J.ForEach(fn)
}
