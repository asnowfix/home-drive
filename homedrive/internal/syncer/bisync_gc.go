// bisync_gc.go holds the bisync retention GC and the nested .old.<N>
// chain repair pass (PLAN.md §11.5), split out of bisync.go to keep that
// file under the 500-line limit.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

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

// ---------------------------------------------------------------------------
// Chain repair (PLAN.md §11.5, issue #65 §3)
// ---------------------------------------------------------------------------

// keyChainRepair guards the one-time nested .old.<N> chain repair pass so
// it runs at most once automatically, on the first bisync pass after
// upgrade.
var keyChainRepair = []byte("old_chain_repair_v1")

// shouldRepairChains reports whether the automatic one-time chain repair
// pass is due: enabled in config and not yet marked done.
func (b *Bisync) shouldRepairChains() bool {
	if !b.cfg.RepairChains {
		return false
	}
	val, err := b.journal.GetMeta(keyChainRepair)
	if err != nil {
		b.log.Error("chain repair: failed to read completion marker, skipping this pass", "error", err)
		return false
	}
	return val != "done"
}

// RunChainRepair runs the nested .old.<N> chain repair pass on demand
// (POST /conflict/repair, `homedrive ctl conflict repair`), independent of
// whether the automatic first-pass-after-upgrade repair already ran. It
// takes the bisync lock like a normal pass, so it cannot run concurrently
// with one.
func (b *Bisync) RunChainRepair(ctx context.Context, dryRun bool) (Report, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runChainRepair(ctx, dryRun, !dryRun)
}

// runChainRepair walks the local root and remote listing, runs
// RepairChains, and -- if markDone is true and the pass did not error --
// records completion so the automatic pass (execute, via
// shouldRepairChains) does not repeat it. Callers must already hold b.mu.
func (b *Bisync) runChainRepair(ctx context.Context, dryRun, markDone bool) (Report, error) {
	locals, err := b.walkLocal(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("chain repair: walk local: %w", err)
	}
	remotes, err := b.listRemote(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("chain repair: list remote: %w", err)
	}

	deps := RepairDeps{
		Matcher: b.cfg.Matcher,
		RenameLocal: func(oldPath, newPath string) error {
			return os.Rename(
				filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(oldPath)),
				filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(newPath)),
			)
		},
		RenameRemote: func(ctx context.Context, oldPath, newPath string) error {
			return b.remote.MoveFile(ctx, oldPath, newPath)
		},
		JournalGet:    b.journal.Get,
		JournalDelete: b.journal.Delete,
		JournalPut:    b.journal.Put,
		Auditor:       b.auditor,
		Log:           b.log,
		DryRun:        dryRun,
	}

	report, err := RepairChains(ctx, deps, locals, remotes)
	if err != nil {
		return report, fmt.Errorf("chain repair: %w", err)
	}

	b.log.Info("chain repair pass complete",
		"op", "conflict_repair", "scanned", report.Scanned, "repaired", len(report.Links), "dry_run", dryRun)

	if dryRun || !markDone {
		return report, nil
	}
	if err := b.journal.SetMeta(keyChainRepair, "done"); err != nil {
		b.log.Error("chain repair: failed to persist completion marker, will retry next pass", "error", err)
	}
	return report, nil
}
