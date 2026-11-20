// bisync_guard.go is the bisync mass-deletion guard (issue #156). Since #152
// bisync propagates deletions in both directions from the journal, so a bad
// listing, a stale journal or a bug could plan many deletions at once. The
// guard vetoes the deletions of a run when there are too many, or when one
// side looks wholly empty against a populated journal. Pushes, pulls and
// conflict handling of that run still go ahead, and the journal is not
// touched for the vetoed paths, so the next run plans the same deletions
// again until the operator allows them once (AllowNextDeletes).
package syncer

import (
	"time"
)

const (
	// DefaultMaxDeleteCount and DefaultMaxDeletePct are the bisync
	// max_delete_count / max_delete_pct defaults.
	DefaultMaxDeleteCount = 50
	DefaultMaxDeletePct   = 10

	// deleteGuardFloor: a run with this many deletions or fewer is never
	// blocked by the threshold rule.
	deleteGuardFloor = 10

	// blockedSampleSize bounds the paths named in the audit line, log and
	// MQTT event of a blocked run.
	blockedSampleSize = 20
)

// Reasons a run's deletions are blocked.
const (
	BlockThreshold   = "threshold"
	BlockEmptyLocal  = "empty_local"
	BlockEmptyRemote = "empty_remote"
)

// DeleteBlock describes a run whose deletions were vetoed by the guard.
type DeleteBlock struct {
	Planned   int       // deletions planned by the vetoed run (both directions)
	Threshold int       // largest count that would have passed (0 for an empty side)
	Reason    string    // BlockThreshold, BlockEmptyLocal or BlockEmptyRemote
	Since     time.Time // first of the consecutive blocked runs
}

// DeleteBlockedEvent is the MQTT payload on events/bisync/delete_blocked.
type DeleteBlockedEvent struct {
	Timestamp string   `json:"ts"`
	Type      string   `json:"type"`
	Planned   int      `json:"planned"`
	Threshold int      `json:"threshold"`
	Reason    string   `json:"reason"`
	Paths     []string `json:"paths,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
}

// AllowNextDeletes arms the one-shot override: the deletions of the next
// bisync run that reaches the guard go through, whatever their number, then
// the override resets. A dry-run pass honours but does not consume it.
func (b *Bisync) AllowNextDeletes() {
	b.guardMu.Lock()
	b.allowOnce = true
	b.guardMu.Unlock()
	b.log.Warn("bisync: next run's deletions allowed once", "op", "allow_deletes")
}

// DeleteBlock returns the current block, or nil when the last run that
// planned deletions was not vetoed.
func (b *Bisync) DeleteBlock() *DeleteBlock {
	b.guardMu.Lock()
	defer b.guardMu.Unlock()
	if b.block == nil {
		return nil
	}
	cp := *b.block
	return &cp
}

// takeOverride reports whether the one-shot override is armed, consuming it
// unless this is a dry run.
func (b *Bisync) takeOverride() bool {
	b.guardMu.Lock()
	defer b.guardMu.Unlock()
	armed := b.allowOnce
	if !b.cfg.DryRun {
		b.allowOnce = false
	}
	return armed
}

// maxDelete returns the effective bounds, defaulting unset (zero) values.
func (b *Bisync) maxDelete() (count, pct int) {
	count, pct = b.cfg.MaxDeleteCount, b.cfg.MaxDeletePct
	if count < 1 {
		count = DefaultMaxDeleteCount
	}
	if pct < 1 {
		pct = DefaultMaxDeletePct
	}
	return count, pct
}

// evaluateGuard applies the rule to n planned deletions. The threshold
// rule is n > floor AND (n > count OR n > pct% of journaled entries); an
// empty local walk or remote listing blocks any n > 0 on its own.
func (b *Bisync) evaluateGuard(n int) (blocked bool, reason string, threshold int) {
	switch {
	case b.localCount == 0:
		return true, BlockEmptyLocal, 0
	case b.remoteCount == 0:
		return true, BlockEmptyRemote, 0
	}
	maxCount, maxPct := b.maxDelete()
	journaled := b.journalSize()
	threshold = max(deleteGuardFloor, min(maxCount, journaled*maxPct/100))
	return n > deleteGuardFloor && (n > maxCount || n*100 > maxPct*journaled), BlockThreshold, threshold
}

// journalSize counts journal entries. A read failure yields 0, which makes
// the percentage bound fail closed (deletions above the floor are blocked).
func (b *Bisync) journalSize() int {
	n := 0
	err := b.journal.ForEach(func(JournalEntry) error {
		n++
		return nil
	})
	if err != nil {
		b.log.Error("bisync delete guard: counting journal entries failed", "error", err)
		return 0
	}
	return n
}

// applyDeleteGuard returns the diffs to execute: all of them, or all but
// the planned deletions when the guard vetoes them.
func (b *Bisync) applyDeleteGuard(diffs []FileDiff) []FileDiff {
	b.vetoed = 0
	planned := plannedDeletions(diffs)
	// Consumed by every run that reaches the guard, even one with nothing
	// to delete, so a stale override cannot wave through a later bad run.
	override := b.takeOverride()
	if len(planned) == 0 {
		b.setBlock(nil)
		return diffs
	}
	blocked, reason, threshold := b.evaluateGuard(len(planned))
	if override || !blocked {
		if override {
			b.log.Warn("bisync: deletions allowed by one-shot override",
				"op", "allow_deletes", "planned", len(planned), "dry_run", b.cfg.DryRun)
		}
		b.setBlock(nil)
		return diffs
	}

	blk := b.setBlock(&DeleteBlock{Planned: len(planned), Threshold: threshold, Reason: reason})
	b.vetoed = len(planned)
	b.reportBlock(blk, planned)

	kept := make([]FileDiff, 0, len(diffs)-len(planned))
	for _, d := range diffs {
		if d.Kind != DiffRemoteDeleted && d.Kind != DiffLocalDeleted {
			kept = append(kept, d)
		}
	}
	return kept
}

// setBlock records the current block (nil clears it), keeping Since stable
// across consecutive blocked runs, and returns the stored value.
func (b *Bisync) setBlock(blk *DeleteBlock) *DeleteBlock {
	b.guardMu.Lock()
	defer b.guardMu.Unlock()
	if blk != nil {
		blk.Since = b.clock.Now()
		if b.block != nil {
			blk.Since = b.block.Since
		}
	}
	b.block = blk
	return blk
}

// reportBlock emits the log, audit line and MQTT event of a blocked run.
func (b *Bisync) reportBlock(blk *DeleteBlock, planned []FileDiff) {
	paths := make([]string, 0, min(len(planned), blockedSampleSize))
	for _, d := range planned[:min(len(planned), blockedSampleSize)] {
		paths = append(paths, d.Path)
	}
	b.log.Warn("bisync deletions blocked by the mass-deletion guard",
		"op", "delete_blocked", "planned", blk.Planned, "threshold", blk.Threshold,
		"reason", blk.Reason, "paths", paths, "dry_run", b.cfg.DryRun)
	b.auditBlocked(blk, paths)
	if b.mqtt == nil {
		return
	}
	ev := DeleteBlockedEvent{
		Timestamp: b.clock.Now().UTC().Format(time.RFC3339),
		Type:      "bisync.delete_blocked",
		Planned:   blk.Planned,
		Threshold: blk.Threshold,
		Reason:    blk.Reason,
		Paths:     paths,
		DryRun:    b.cfg.DryRun,
	}
	if err := b.mqtt.PublishJSON(b.mqtt.Topic("events", "bisync", "delete_blocked"), ev); err != nil {
		b.log.Error("failed to publish bisync event", "type", ev.Type, "error", err)
	}
}
