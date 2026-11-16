// bisync_retention.go holds the bisync side of the .old.<N> retention GC
// (PLAN.md section 11.5): the periodic sweep and the post-conflict prune.
package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// runRetention applies the retention GC once syncDiffs has drained: the
// post-conflict prunes, then the periodic sweep if it is due.
func (b *Bisync) runRetention(ctx context.Context) {
	for base := range b.pendingPrune {
		b.pruneAfterConflict(ctx, base)
	}
	b.pendingPrune = nil

	if b.shouldSweep() {
		removed := b.sweepOldFiles(ctx)
		b.lastSweep = b.clock.Now()
		if len(removed) > 0 {
			b.log.Info("retention sweep pruned conflict losers",
				"op", "conflict_gc", "count", len(removed))
		}
	}
}

// ---------------------------------------------------------------------------
// Retention GC (PLAN.md §11.5)
// ---------------------------------------------------------------------------

// shouldSweep reports whether the periodic full-journal retention sweep is
// due. A zero SweepInterval disables the periodic sweep entirely (inline
// eviction in resolveLocalWins/resolveRemoteWins still runs).
func (b *Bisync) shouldSweep() bool {
	if b.cfg.SweepInterval <= 0 {
		return false
	}
	return b.clock.Now().Sub(b.lastSweep) >= b.cfg.SweepInterval
}

// pruneDeps builds the store.PruneDeps closures for the retention GC,
// wiring them to this Bisync's journal/remote/LocalRoot. Shared by the
// inline eviction in resolveLocalWins/resolveRemoteWins and the periodic
// sweep, so the deletion ordering (file first, then journal entry -- see
// store.PruneDeps.DeleteEntry) is enforced once, in store.PruneOldSiblings/
// evict, not duplicated here.
func (b *Bisync) pruneDeps() store.PruneDeps {
	return store.PruneDeps{
		Matcher: b.cfg.Matcher,
		ListByPrefix: func(prefix string) ([]JournalEntry, error) {
			return b.journal.ListByPrefix(prefix)
		},
		ForEach: func(fn func(JournalEntry) error) error {
			return b.journal.ForEach(fn)
		},
		DeleteEntry: func(p string) error {
			return b.journal.Delete(p)
		},
		RemoveLocal: func(relPath string) error {
			err := os.Remove(filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(relPath)))
			if err != nil && os.IsNotExist(err) {
				return nil
			}
			return err
		},
		RemoveRemote: func(ctx context.Context, p string) error {
			err := b.remote.DeleteFile(ctx, p)
			if err != nil && errors.Is(err, ErrRemoteNotFound) {
				return nil
			}
			return err
		},
		Auditor:   b.auditor,
		Publisher: b.mqtt,
		Clock:     b.clock.Now,
		DryRun:    b.cfg.DryRun,
		Log:       b.log,
	}
}

// pruneAfterConflict runs the inline retention GC for base right after a
// new conflict loser was recorded by resolveLocalWins/resolveRemoteWins.
// Errors are logged, never propagated -- a GC failure must not fail the
// conflict resolution that triggered it.
func (b *Bisync) pruneAfterConflict(ctx context.Context, base string) {
	if _, err := store.PruneOldSiblings(ctx, b.pruneDeps(), b.cfg.Retention, base); err != nil {
		b.log.Error("conflict retention prune failed", "op", "conflict_gc", "base", base, "error", err)
	}
}

// sweepOldFiles runs the periodic full-journal retention sweep. Errors are
// logged, never propagated -- see pruneAfterConflict.
func (b *Bisync) sweepOldFiles(ctx context.Context) []string {
	removed, err := store.SweepOldFiles(ctx, b.pruneDeps(), b.cfg.Retention)
	if err != nil {
		b.log.Error("retention sweep failed", "op", "conflict_gc", "error", err)
	}
	return removed
}
