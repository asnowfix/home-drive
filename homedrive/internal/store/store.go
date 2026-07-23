package store

import (
	"context"
	"errors"
	"log/slog"
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

	// NextOldN returns the smallest positive N such that "<path>.old.<N>"
	// has no journal entry, used when computing conflict loser filenames.
	NextOldN(ctx context.Context, path string) (int, error)

	// RewritePrefix renames all journal paths under oldPrefix to
	// newPrefix, used by the push syncer when a directory is renamed.
	RewritePrefix(ctx context.Context, oldPrefix, newPrefix string) (int, error)
}

// JournalStore adapts *Journal to the context-aware Store interface.
type JournalStore struct {
	J   *Journal
	Log *slog.Logger
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
func (s *JournalStore) NextOldN(_ context.Context, path string) (int, error) {
	return s.J.NextOldN(path), nil
}

// RewritePrefix implements Store. The audit trail for directory renames
// is emitted separately by the syncer via AuditLogger, so no *Auditor is
// threaded through here (matching the pre-existing adapter behavior).
func (s *JournalStore) RewritePrefix(_ context.Context, oldPrefix, newPrefix string) (int, error) {
	return RewritePrefix(s.J, oldPrefix, newPrefix, nil, s.Log)
}
