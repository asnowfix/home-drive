// bisync.go implements the periodic bisync safety net described in
// PLAN.md sections 7.2 and 14 (Phase 7). It performs a full directory
// diff between the local filesystem and the remote, syncing any drift
// found. It acquires a global write lock to block push/pull workers
// during execution.
package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// Bisync is the periodic safety-net syncer that performs a full directory
// diff between local and remote, resolving any drift found.
type Bisync struct {
	cfg     BisyncConfig
	remote  RemoteFS
	journal Journal
	mqtt    Publisher   // may be nil if MQTT is disabled
	audit   AuditWriter // may be nil if audit is disabled
	clock   Clock
	log     *slog.Logger

	// mu is the global RWMutex shared with push/pull workers.
	// Bisync takes Lock(); push workers take RLock().
	mu *sync.RWMutex

	// forceCh receives signals from the /resync endpoint.
	forceCh chan struct{}

	// running tracks whether bisync is currently executing, protected
	// by runMu.
	runMu   sync.Mutex
	running bool

	// auditor receives "conflict_gc" JSONL lines from the retention GC
	// (PLAN.md §11.5). May be nil to disable.
	auditor *Auditor

	// lastSweep is when the periodic retention sweep last ran. Only
	// read/written from execute(), which already holds b.mu exclusively,
	// so it needs no separate lock.
	lastSweep time.Time

	// pendingPrune collects the base paths of conflicts resolved during
	// the current execute() pass. Pruning runs once, after syncDiffs has
	// fully drained the diffs computed at the top of this pass -- not
	// inline from resolveLocalWins/resolveRemoteWins -- because diffs is
	// a single snapshot taken before any conflict resolution: an inline
	// eviction can delete a sibling that a later, already-queued
	// DiffRemoteOnly entry in that same snapshot then resurrects by
	// pulling it back down. Only touched within execute(), which holds
	// b.mu exclusively, so it needs no separate lock.
	pendingPrune map[string]struct{}
}

// markForPrune records base as needing a retention-GC pass once the
// current execute() call's syncDiffs loop has fully drained. See
// pendingPrune's doc comment for why this can't run inline.
func (b *Bisync) markForPrune(base string) {
	if b.pendingPrune == nil {
		b.pendingPrune = make(map[string]struct{})
	}
	b.pendingPrune[base] = struct{}{}
}

// BisyncOpts are constructor options for Bisync.
type BisyncOpts struct {
	Config  BisyncConfig
	Remote  RemoteFS
	Journal Journal
	MQTT    Publisher     // optional
	Audit   AuditWriter   // optional
	Clock   Clock         // defaults to realClock
	Logger  *slog.Logger  // defaults to slog.Default()
	Mu      *sync.RWMutex // shared push/bisync mutex

	// Auditor, if non-nil, receives "conflict_gc" JSONL lines from the
	// retention GC (PLAN.md §11.5). Distinct from Audit (the
	// BisyncAuditEntry writer for bisync-pass summaries): this is the
	// same *store.Auditor instance the push/pull paths use for
	// per-file AuditEntry lines.
	Auditor *Auditor
}

// NewBisync creates a new bisync runner. The returned ForceCh can be
// used to trigger an immediate bisync run (e.g., from /resync).
func NewBisync(opts BisyncOpts) (*Bisync, chan<- struct{}) {
	clk := opts.Clock
	if clk == nil {
		clk = realClock{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	mu := opts.Mu
	if mu == nil {
		mu = &sync.RWMutex{}
	}
	cfg := opts.Config
	if cfg.Matcher == nil {
		cfg.Matcher, _ = oldsuffix.New("") // never errors for the empty/default format
	}

	forceCh := make(chan struct{}, 1)

	b := &Bisync{
		cfg:     cfg,
		remote:  opts.Remote,
		journal: opts.Journal,
		mqtt:    opts.MQTT,
		audit:   opts.Audit,
		clock:   clk,
		log:     logger,
		mu:      mu,
		forceCh: forceCh,
		auditor: opts.Auditor,
	}
	return b, forceCh
}

// Mu returns the shared RWMutex so push workers can take RLock.
func (b *Bisync) Mu() *sync.RWMutex {
	return b.mu
}

// Run starts the bisync ticker loop. It blocks until ctx is canceled.
func (b *Bisync) Run(ctx context.Context) error {
	interval := b.cfg.Interval
	if interval <= 0 {
		interval = time.Hour
	}

	tickCh, stopTicker := b.clock.NewTicker(interval)
	defer stopTicker()

	b.log.Info("bisync started",
		"interval", interval.String(),
		"local_root", b.cfg.LocalRoot,
		"dry_run", b.cfg.DryRun,
	)

	for {
		select {
		case <-ctx.Done():
			return ErrBisyncCanceled
		case <-tickCh:
			b.execute(ctx)
		case <-b.forceCh:
			b.log.Info("bisync force triggered")
			b.execute(ctx)
		}
	}
}

// ForceRun triggers an immediate bisync execution. Returns
// ErrBisyncRunning if a run is already in progress.
func (b *Bisync) ForceRun(_ context.Context) error {
	b.runMu.Lock()
	if b.running {
		b.runMu.Unlock()
		return ErrBisyncRunning
	}
	b.runMu.Unlock()

	select {
	case b.forceCh <- struct{}{}:
		return nil
	default:
		return ErrBisyncRunning
	}
}

// execute performs a single bisync pass.
func (b *Bisync) execute(ctx context.Context) {
	b.runMu.Lock()
	if b.running {
		b.runMu.Unlock()
		b.log.Warn("bisync skipped, already running")
		return
	}
	b.running = true
	b.runMu.Unlock()
	defer func() {
		b.runMu.Lock()
		b.running = false
		b.runMu.Unlock()
	}()

	start := b.clock.Now()

	b.publishEvent(BisyncEvent{
		Timestamp: start.UTC().Format(time.RFC3339),
		Type:      "bisync.started",
		DryRun:    b.cfg.DryRun,
	})

	// Acquire exclusive lock, blocking push/pull workers.
	b.log.Debug("bisync acquiring global lock")
	b.mu.Lock()
	defer b.mu.Unlock()
	b.log.Debug("bisync global lock acquired")

	if b.shouldRepairChains() {
		// Run before diff(): repair renames the very paths a
		// pre-computed diffs snapshot would reference, and mutating
		// files mid-way through processing a stale diffs list is the
		// same resurrection hazard the inline retention GC has (see
		// pendingPrune's doc comment). Running it first and letting
		// diff() see fresh post-repair state sidesteps that entirely.
		if _, err := b.runChainRepair(ctx, b.cfg.DryRun, true); err != nil {
			b.log.Error("chain repair pass failed", "op", "conflict_repair", "error", err)
		}
	}

	// Perform the diff.
	diffs, err := b.diff(ctx)
	if err != nil {
		b.log.Error("bisync diff failed", "error", err)
		b.writeAudit(start, 0, 0, 0, err)
		b.publishEvent(BisyncEvent{
			Timestamp: b.clock.Now().UTC().Format(time.RFC3339),
			Type:      "bisync.completed",
			Error:     err.Error(),
			DryRun:    b.cfg.DryRun,
		})
		return
	}

	pushed, pulled, conflicts := b.syncDiffs(ctx, diffs)

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

	elapsed := b.clock.Now().Sub(start)

	b.log.Info("bisync completed",
		"duration_ms", elapsed.Milliseconds(),
		"files_pushed", pushed,
		"files_pulled", pulled,
		"conflicts", conflicts,
		"dry_run", b.cfg.DryRun,
	)

	b.writeAudit(start, pushed, pulled, conflicts, nil)
	b.publishEvent(BisyncEvent{
		Timestamp:    b.clock.Now().UTC().Format(time.RFC3339),
		Type:         "bisync.completed",
		DurationMs:   elapsed.Milliseconds(),
		FilesChanged: pushed + pulled,
		Conflicts:    conflicts,
		DryRun:       b.cfg.DryRun,
	})
}

// syncDiffs iterates over diffs and syncs each one.
func (b *Bisync) syncDiffs(
	ctx context.Context, diffs []FileDiff,
) (pushed, pulled, conflicts int) {
	for _, d := range diffs {
		if ctx.Err() != nil {
			break
		}
		switch d.Kind {
		case DiffLocalOnly:
			if err := b.syncLocalToRemote(ctx, d); err != nil {
				b.log.Error("bisync push failed",
					"path", d.Path, "error", err)
				continue
			}
			pushed++
		case DiffRemoteOnly:
			if err := b.syncRemoteToLocal(ctx, d); err != nil {
				b.log.Error("bisync pull failed",
					"path", d.Path, "error", err)
				continue
			}
			pulled++
		case DiffConflict:
			if err := b.resolveConflict(ctx, d); err != nil {
				b.log.Error("bisync conflict resolution failed",
					"path", d.Path, "error", err)
			}
			conflicts++
		}
	}
	return pushed, pulled, conflicts
}

// ---------------------------------------------------------------------------
// Audit and MQTT helpers
// ---------------------------------------------------------------------------

// writeAudit appends a JSONL line to the audit log.
func (b *Bisync) writeAudit(
	start time.Time,
	pushed, pulled, conflicts int,
	syncErr error,
) {
	if b.audit == nil {
		return
	}

	elapsed := b.clock.Now().Sub(start)
	entry := BisyncAuditEntry{
		Timestamp:    start.UTC().Format(time.RFC3339),
		Op:           "bisync",
		Duration:     fmt.Sprintf("%d", elapsed.Milliseconds()),
		FilesChanged: pushed + pulled,
		FilesPushed:  pushed,
		FilesPulled:  pulled,
		Conflicts:    conflicts,
		DryRun:       b.cfg.DryRun,
	}
	if syncErr != nil {
		entry.Error = syncErr.Error()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		b.log.Error("failed to marshal audit entry", "error", err)
		return
	}

	line := string(data) + "\n"
	if _, err := b.audit.Write([]byte(line)); err != nil {
		b.log.Error("failed to write audit entry", "error", err)
	}
}

// publishEvent publishes a bisync MQTT event. No-op if MQTT is nil.
func (b *Bisync) publishEvent(event BisyncEvent) {
	if b.mqtt == nil {
		return
	}

	parts := strings.Split(event.Type, ".")
	topic := b.mqtt.Topic(append([]string{"events"}, parts...)...)

	if err := b.mqtt.PublishJSON(topic, event); err != nil {
		b.log.Error("failed to publish bisync event",
			"type", event.Type,
			"error", err,
		)
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
