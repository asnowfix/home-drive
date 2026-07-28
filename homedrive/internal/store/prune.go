// prune.go implements retention/GC for .old.<N> conflict-loser artifacts
// (PLAN.md §11.5): a count cap (max_per_file) and an optional age cap
// (max_age), applied both inline right after a new loser is recorded and
// periodically via a full-journal sweep piggybacked on the bisync tick.
package store

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// RetentionPolicy bounds how many .old.<N> conflict-loser siblings are
// kept per base file, and how old they may get. See PLAN.md §11.5.
type RetentionPolicy struct {
	// MaxPerFile caps how many siblings are kept; the oldest beyond this
	// count are evicted. MaxPerFile <= 0 means unlimited (no count-based
	// eviction) at this function's level -- config.Load's applyDefaults
	// never actually lets a config-sourced value reach here as <= 0 (see
	// that method's doc comment), but a caller invoking PruneOldSiblings
	// directly (e.g. with a zero-value RetentionPolicy) gets a safe no-op
	// rather than an unexpected mass deletion.
	MaxPerFile int

	// MaxAge expires siblings older than this regardless of MaxPerFile.
	// Zero means never expire by age.
	MaxAge time.Duration
}

// Publisher is the minimal MQTT-publish surface PruneOldSiblings and
// SweepOldFiles need to emit events/conflict.pruned. Its method set is
// structurally identical to syncer.Publisher, so any concrete Publisher
// value from that package satisfies this interface with no adapter.
type Publisher interface {
	PublishJSON(topic string, payload any) error
	Topic(parts ...string) string
}

// PruneDeps groups the callbacks PruneOldSiblings/SweepOldFiles need.
// It is expressed as functions rather than a Journal/Store interface so
// that both the pull path (store.Store, context-aware) and the bisync
// path (syncer.Journal, context-free) can supply the same call without
// an adapter type in between.
type PruneDeps struct {
	// Matcher parses/formats the .old.<N> suffix. Required.
	Matcher *oldsuffix.Matcher

	// ListByPrefix returns every journal entry whose path starts with
	// prefix (a base+Matcher.Pre() scan). Required.
	ListByPrefix func(prefix string) ([]JournalEntry, error)

	// ForEach calls fn for every journal entry in the journal. Only
	// required by SweepOldFiles.
	ForEach func(fn func(JournalEntry) error) error

	// DeleteEntry removes a journal entry. Called only after the
	// corresponding file has already been deleted (see evict). Required.
	DeleteEntry func(path string) error

	// RemoveLocal deletes the local file at relPath. Must treat "already
	// gone" as success (idempotent). Required.
	RemoveLocal func(relPath string) error

	// RemoveRemote deletes the remote object at path. Must treat
	// "already gone" as success (idempotent). Required.
	RemoveRemote func(ctx context.Context, path string) error

	// Auditor, if non-nil, receives a "conflict_gc" JSONL line for every
	// eviction.
	Auditor *Auditor

	// Publisher, if non-nil, receives an events/conflict.pruned message
	// for every eviction (and dry-run preview).
	Publisher Publisher

	// Clock defaults to time.Now if nil.
	Clock func() time.Time

	// Log defaults to slog.Default() if nil.
	Log *slog.Logger

	// DryRun, if true, logs and publishes what would be pruned without
	// deleting anything.
	DryRun bool
}

func (deps PruneDeps) clockNow() time.Time {
	if deps.Clock != nil {
		return deps.Clock()
	}
	return time.Now()
}

func (deps PruneDeps) logger() *slog.Logger {
	if deps.Log != nil {
		return deps.Log
	}
	return slog.Default()
}

// eviction pairs a journal entry with the retention rule that selected
// it, for audit/MQTT labeling ("max_per_file" | "max_age").
type eviction struct {
	entry  JournalEntry
	reason string
}

// PruneOldSiblings deletes base's .old.<N> siblings beyond policy's
// limits, on whichever side each loser lives (JournalEntry.LastOrigin).
// It returns the paths it removed. Errors deleting an individual loser
// are logged and skipped, never propagated -- a GC failure must not fail
// the conflict resolution that triggered it (PLAN.md §11.5).
func PruneOldSiblings(ctx context.Context, deps PruneDeps, policy RetentionPolicy, base string) ([]string, error) {
	candidates, err := deps.ListByPrefix(base + deps.Matcher.Pre())
	if err != nil {
		return nil, fmt.Errorf("store: list siblings of %q: %w", base, err)
	}

	evictions := selectEvictions(deps.Matcher, base, candidates, policy, deps.clockNow())

	var removed []string
	for _, ev := range evictions {
		if evict(ctx, deps, base, ev) {
			removed = append(removed, ev.entry.Path)
		}
	}
	return removed, nil
}

// SweepOldFiles walks every journal entry via deps.ForEach, groups
// direct .old.<N> siblings by their immediate base, and evicts each
// group beyond policy. Unlike PruneOldSiblings (bounded to one base,
// called inline right after a new loser is recorded), this is
// O(journal size) and is meant to run periodically (see the Bisync
// sweep integration), not on every conflict.
//
// A bbolt read transaction (which backs ForEach) forbids mutation from
// inside the callback, so this collects candidates in one pass and
// evicts them in a second pass, outside the transaction.
func SweepOldFiles(ctx context.Context, deps PruneDeps, policy RetentionPolicy) ([]string, error) {
	groups := make(map[string][]JournalEntry)
	if err := deps.ForEach(func(e JournalEntry) error {
		base, _, ok := deps.Matcher.TrimOne(e.Path)
		if !ok {
			return nil
		}
		groups[base] = append(groups[base], e)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: sweep: walk journal: %w", err)
	}

	now := deps.clockNow()
	var removed []string
	for base, siblings := range groups {
		evictions := selectEvictions(deps.Matcher, base, siblings, policy, now)
		for _, ev := range evictions {
			if evict(ctx, deps, base, ev) {
				removed = append(removed, ev.entry.Path)
			}
		}
	}
	return removed, nil
}

// selectEvictions decides which of candidates (a raw prefix-scan result
// that may include entries belonging to a different base, or to a
// deeper nested chain) should be evicted under policy, given the
// current time now.
//
// Only entries that TrimOne to exactly base are direct siblings; a
// deeper nested chain link (e.g. base.old.1.old.2, which TrimOne parses
// as base "base.old.1") is not a sibling of base and is left untouched
// -- collapsing those is the repair pass's job (PLAN.md §11.5), not
// retention's.
//
// Survivors are ordered by LastSyncedAt descending, tie-broken by N
// descending, so the newest max_per_file siblings are kept; anything
// beyond that count, or beyond max_age regardless of count, is evicted.
func selectEvictions(
	m *oldsuffix.Matcher, base string, candidates []JournalEntry, policy RetentionPolicy, now time.Time,
) []eviction {
	type indexed struct {
		entry JournalEntry
		n     int
	}
	var siblings []indexed
	for _, e := range candidates {
		trimmedBase, n, ok := m.TrimOne(e.Path)
		if !ok || trimmedBase != base {
			continue
		}
		siblings = append(siblings, indexed{entry: e, n: n})
	}

	sort.Slice(siblings, func(i, j int) bool {
		if !siblings[i].entry.LastSyncedAt.Equal(siblings[j].entry.LastSyncedAt) {
			return siblings[i].entry.LastSyncedAt.After(siblings[j].entry.LastSyncedAt)
		}
		return siblings[i].n > siblings[j].n
	})

	var evictions []eviction
	for i, s := range siblings {
		switch {
		case policy.MaxAge > 0 && now.Sub(s.entry.LastSyncedAt) > policy.MaxAge:
			evictions = append(evictions, eviction{entry: s.entry, reason: "max_age"})
		case policy.MaxPerFile > 0 && i >= policy.MaxPerFile:
			evictions = append(evictions, eviction{entry: s.entry, reason: "max_per_file"})
		}
	}
	return evictions
}

// evict performs the normative deletion ordering from PLAN.md §11.5:
// remove the file/remote object first, then the journal entry, then the
// audit line, then the MQTT event. Doing the file delete first is what
// makes .old.<N> "smallest free N" reuse safe -- a crash between the
// file delete and the journal delete only leaves an orphan journal entry
// (which merely reserves an N a little longer than necessary), never a
// file on disk with no journal record that a later conflict's N-reuse
// could silently overwrite.
//
// evict returns true if entry.Path was actually removed (false for a
// dry-run preview or a failed removal).
func evict(ctx context.Context, deps PruneDeps, base string, ev eviction) bool {
	log := deps.logger()
	entry := ev.entry

	if deps.DryRun {
		log.Info("dry-run: would prune conflict loser",
			"op", "conflict_gc", "path", entry.Path, "base", base, "reason", ev.reason)
		publishPruned(deps, base, entry.Path, ev.reason, true)
		return false
	}

	if err := removeEvicted(ctx, deps, entry); err != nil {
		log.Error("failed to prune conflict loser",
			"op", "conflict_gc", "path", entry.Path, "reason", ev.reason, "error", err)
		return false
	}

	if err := deps.DeleteEntry(entry.Path); err != nil {
		// The file is already gone; a stale journal entry only means the
		// N it reserves stays reserved a little longer, and a future
		// sweep will retry the delete. Not returning an error here is
		// intentional: the eviction itself (the part that reclaims disk
		// space) already succeeded.
		log.Error("pruned conflict loser but failed to delete journal entry",
			"op", "conflict_gc", "path", entry.Path, "error", err)
	}

	if deps.Auditor != nil {
		deps.Auditor.Log(AuditEntry{
			Timestamp: deps.clockNow(),
			Op:        "conflict_gc",
			Path:      entry.Path,
			Reason:    ev.reason,
		})
	}
	publishPruned(deps, base, entry.Path, ev.reason, false)

	log.Info("pruned conflict loser",
		"op", "conflict_gc", "path", entry.Path, "base", base, "reason", ev.reason)
	return true
}

// removeEvicted deletes entry's underlying file/remote object, on
// whichever side it lives per entry.LastOrigin.
func removeEvicted(ctx context.Context, deps PruneDeps, entry JournalEntry) error {
	if entry.LastOrigin == "remote" {
		return deps.RemoveRemote(ctx, entry.Path)
	}
	return deps.RemoveLocal(entry.Path)
}

// publishPruned emits events/conflict.pruned. No-op if deps.Publisher is
// nil.
func publishPruned(deps PruneDeps, base, path, reason string, dryRun bool) {
	if deps.Publisher == nil {
		return
	}
	_ = deps.Publisher.PublishJSON(deps.Publisher.Topic("events", "conflict.pruned"), map[string]any{
		"ts":      deps.clockNow().UTC().Format(time.RFC3339),
		"type":    "conflict.pruned",
		"path":    path,
		"base":    base,
		"reason":  reason,
		"dry_run": dryRun,
	})
}
