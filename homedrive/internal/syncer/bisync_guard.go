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
	"errors"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// ErrNoDeleteBlock is returned by AllowNextDeletes when no run is blocked:
// the override is tied to the block it releases and is never armed blind.
var ErrNoDeleteBlock = errors.New("no bisync deletions are blocked")

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

// deleteAllowance is a one-shot override tied to the block it was armed
// against: it only releases a block of the same reason with no more planned
// deletions than that block had.
type deleteAllowance struct {
	reason  string
	planned int
}

// AllowNextDeletes arms the one-shot override for the current block. The
// next bisync run that reaches the guard is released from the veto only if
// its block has the same reason and no more planned deletions than the
// current one; otherwise it is blocked again with its own figures. Either
// way the override is then spent. It returns ErrNoDeleteBlock when nothing
// is blocked. A dry-run pass honours but does not consume it.
func (b *Bisync) AllowNextDeletes() error {
	b.guardMu.Lock()
	defer b.guardMu.Unlock()
	if b.block == nil {
		return ErrNoDeleteBlock
	}
	b.allow = &deleteAllowance{reason: b.block.Reason, planned: b.block.Planned}
	b.log.Warn("bisync: next run's deletions allowed once", "op", "allow_deletes",
		"reason", b.allow.reason, "up_to", b.allow.planned)
	return nil
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

// takeOverride returns the armed override (nil if none), consuming it
// unless this is a dry run.
func (b *Bisync) takeOverride() *deleteAllowance {
	b.guardMu.Lock()
	defer b.guardMu.Unlock()
	armed := b.allow
	if !b.cfg.DryRun {
		b.allow = nil
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

// journalSize counts the journal rows that could become a deletion: those
// with a sync baseline that watcher.exclude does not exclude. Conflict-loser
// rows and other baseline-less rows would only dilute the percentage bound. A
// read failure yields 0, which makes the bound fail closed (deletions above
// the floor are blocked).
func (b *Bisync) journalSize() int {
	n := 0
	err := b.journal.ForEach(func(e JournalEntry) error {
		if hasBaseline(&e) && !pathfilter.Excluded(b.cfg.Exclude, e.Path) {
			n++
		}
		return nil
	})
	if err != nil {
		b.log.Error("bisync delete guard: counting journal entries failed", "error", err)
		return 0
	}
	return n
}

// applyDeleteGuard returns the diffs to execute: all of them, or all but
// the planned deletions when the guard vetoes them. abort is true when an
// empty side blocked the run: the whole pass must then do nothing, since a
// transfer would repopulate the empty side (or push to a wrong remote) and
// let the next run's smaller plan slip under the floor.
func (b *Bisync) applyDeleteGuard(diffs []FileDiff) (kept []FileDiff, abort bool) {
	b.vetoed = 0
	planned := plannedDeletions(diffs)
	// Consumed by every run that reaches the guard, even one with nothing
	// to delete, so a stale override cannot wave through a later bad run.
	allow := b.takeOverride()
	if len(planned) == 0 {
		b.setBlock(nil)
		return diffs, false
	}
	blocked, reason, threshold := b.evaluateGuard(len(planned))
	if !blocked {
		b.setBlock(nil)
		return diffs, false
	}
	if allow != nil && allow.reason == reason && len(planned) <= allow.planned {
		b.log.Warn("bisync: deletions allowed by one-shot override",
			"op", "allow_deletes", "planned", len(planned), "reason", reason,
			"dry_run", b.cfg.DryRun)
		if !b.cfg.DryRun {
			b.setBlock(nil)
		}
		return diffs, false
	}

	blk := b.setBlock(&DeleteBlock{Planned: len(planned), Threshold: threshold, Reason: reason})
	b.vetoed = len(planned)
	b.reportBlock(blk, planned)
	if reason != BlockThreshold {
		return nil, true
	}

	kept = make([]FileDiff, 0, len(diffs)-len(planned))
	for _, d := range diffs {
		if d.Kind != DiffRemoteDeleted && d.Kind != DiffLocalDeleted {
			kept = append(kept, d)
		}
	}
	return kept, false
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
