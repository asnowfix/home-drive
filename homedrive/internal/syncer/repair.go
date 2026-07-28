// repair.go implements the one-time repair pass for pre-existing nested
// .old.<N> chains (PLAN.md §11.5, issue #65 §3): collapsing new conflicts
// (bisync_ops.go, conflict.go) stops chains from growing further, but a
// NAS that already accumulated a 13-deep chain before upgrading needs
// those existing links renumbered onto their base's flat namespace.
//
// The pass is driven by the filesystem and remote listing, not by the
// journal: some chain links may predate a journal entry entirely (e.g. a
// os.Rename that raced a failed store.Put), so a journal-only scan would
// miss them.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

// RepairedLink describes one nested chain link renumbered onto its base's
// flat namespace.
type RepairedLink struct {
	OldPath string
	NewPath string
	Side    string // "local" | "remote"
}

// Report summarizes one RepairChains pass.
type Report struct {
	// Scanned is how many local+remote paths were considered candidates
	// (depth >= 2 under Matcher, i.e. a nested chain link).
	Scanned int
	// Links is every link that was renumbered (or, in a dry run, would
	// have been).
	Links []RepairedLink
}

// RepairDeps groups the callbacks RepairChains needs. Expressed as
// functions, not a Journal/RemoteFS interface, so both the bisync path
// (context-free Journal) and any future caller can supply the same shape
// without an adapter type in between.
type RepairDeps struct {
	// Matcher parses/formats the .old.<N> suffix. Required.
	Matcher *oldsuffix.Matcher

	// RenameLocal renames a local file, relative to the sync root.
	RenameLocal func(oldPath, newPath string) error

	// RenameRemote renames a remote object.
	RenameRemote func(ctx context.Context, oldPath, newPath string) error

	// JournalGet returns the journal entry for path, or nil if none
	// exists (some chain links predate a journal entry, per this file's
	// package doc).
	JournalGet func(path string) (*JournalEntry, error)

	// JournalDelete removes a journal entry. No-op-safe if none exists.
	JournalDelete func(path string) error

	// JournalPut writes a journal entry.
	JournalPut func(entry JournalEntry) error

	// Auditor, if non-nil, receives a "conflict_repair" JSONL line for
	// every renumbered link.
	Auditor *store.Auditor

	// Log defaults to slog.Default() if nil.
	Log *slog.Logger

	// DryRun, if true, reports what would be renumbered without
	// renaming anything.
	DryRun bool
}

func (deps RepairDeps) logger() *slog.Logger {
	if deps.Log != nil {
		return deps.Log
	}
	return slog.Default()
}

// candidate is one path under consideration: a local or remote file whose
// Matcher-stripped depth is >= 2 (a nested chain link) and whose fully
// stripped base exists on the same side.
type candidate struct {
	path  string
	base  string
	depth int
	side  string // "local" | "remote"
}

// RepairChains renumbers every pre-existing nested .old.<N> chain link
// found in locals/remotes onto its base's flat namespace, per PLAN.md
// §11.5. A plain single-suffix path (depth 1) is left untouched -- it is
// already the correct output of the live collapsing algorithm
// (oldsuffix.NextOldN), not corruption. Content is never deleted, only
// renamed: every link survives, just renumbered.
//
// Processing order is deepest-first, so a 13-deep chain's deepest link is
// renumbered before its shallower links can collide with a target this
// same pass is about to create.
func RepairChains(
	ctx context.Context, deps RepairDeps, locals []LocalFileInfo, remotes []RemoteObject,
) (Report, error) {
	localSet := make(map[string]struct{}, len(locals))
	for _, l := range locals {
		localSet[l.Path] = struct{}{}
	}
	remoteSet := make(map[string]struct{}, len(remotes))
	for _, r := range remotes {
		remoteSet[r.Path] = struct{}{}
	}

	candidates := findCandidates(deps.Matcher, localSet, remoteSet)

	var report Report
	report.Scanned = len(candidates)
	for _, c := range candidates {
		exists := existsFn(c.side, localSet, remoteSet)
		_, n := oldsuffix.NextOldN(deps.Matcher, c.base, exists)
		target := deps.Matcher.Format(c.base, n)

		if deps.DryRun {
			report.Links = append(report.Links, RepairedLink{OldPath: c.path, NewPath: target, Side: c.side})
			continue
		}

		if err := repairOne(ctx, deps, c, target, localSet, remoteSet); err != nil {
			deps.logger().Error("chain repair failed",
				"op", "conflict_repair", "path", c.path, "target", target, "side", c.side, "error", err)
			continue
		}

		report.Links = append(report.Links, RepairedLink{OldPath: c.path, NewPath: target, Side: c.side})
	}

	return report, nil
}

// findCandidates scans localSet/remoteSet for nested chain links (depth
// >= 2 whose fully-stripped base exists on the same side), sorted
// deepest-first.
func findCandidates(m *oldsuffix.Matcher, localSet, remoteSet map[string]struct{}) []candidate {
	var candidates []candidate
	for p := range localSet {
		base, depth := m.Base(p)
		if depth < 2 {
			continue
		}
		if _, ok := localSet[base]; !ok {
			continue
		}
		candidates = append(candidates, candidate{path: p, base: base, depth: depth, side: "local"})
	}
	for p := range remoteSet {
		base, depth := m.Base(p)
		if depth < 2 {
			continue
		}
		if _, ok := remoteSet[base]; !ok {
			continue
		}
		candidates = append(candidates, candidate{path: p, base: base, depth: depth, side: "remote"})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].depth != candidates[j].depth {
			return candidates[i].depth > candidates[j].depth
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates
}

// existsFn returns an existence check over the live (mutated-as-we-go)
// local or remote set, so two nested chains that collapse onto the same
// base within a single pass never pick the same target N.
func existsFn(side string, localSet, remoteSet map[string]struct{}) func(string) bool {
	set := localSet
	if side == "remote" {
		set = remoteSet
	}
	return func(p string) bool {
		_, ok := set[p]
		return ok
	}
}

// repairOne renames c.path to target on c.side, carries its journal entry
// over (if any), and audits the rename. It mutates localSet/remoteSet so
// later candidates in the same pass see the up-to-date namespace.
func repairOne(
	ctx context.Context, deps RepairDeps, c candidate, target string, localSet, remoteSet map[string]struct{},
) error {
	switch c.side {
	case "local":
		if err := deps.RenameLocal(c.path, target); err != nil {
			return fmt.Errorf("rename local %s to %s: %w", c.path, target, err)
		}
		delete(localSet, c.path)
		localSet[target] = struct{}{}
	case "remote":
		if err := deps.RenameRemote(ctx, c.path, target); err != nil {
			return fmt.Errorf("rename remote %s to %s: %w", c.path, target, err)
		}
		delete(remoteSet, c.path)
		remoteSet[target] = struct{}{}
	}

	entry, _ := deps.JournalGet(c.path)
	_ = deps.JournalDelete(c.path)
	if entry != nil {
		carried := *entry
		carried.Path = target
		if err := deps.JournalPut(carried); err != nil {
			return fmt.Errorf("journal put %s: %w", target, err)
		}
	}

	if deps.Auditor != nil {
		deps.Auditor.Log(store.AuditEntry{
			Op:      "conflict_repair",
			Path:    c.path,
			NewPath: target,
		})
	}

	deps.logger().Info("chain repair: renumbered nested conflict loser",
		"op", "conflict_repair", "path", c.path, "new_path", target, "side", c.side)
	return nil
}
