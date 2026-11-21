// bisync_gc.go holds the nested .old.<N> chain repair pass (PLAN.md §11.5);
// the retention GC itself lives in bisync_retention.go. Split out of
// bisync.go to keep that file under the 500-line limit.
package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

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
	if err := b.gateErr(); err != nil {
		return Report{}, err
	}
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
