package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// ConflictPolicy defines how conflicts are resolved.
type ConflictPolicy string

const (
	PolicyNewerWins  ConflictPolicy = "newer_wins"
	PolicyLocalWins  ConflictPolicy = "local_wins"
	PolicyRemoteWins ConflictPolicy = "remote_wins"
)

// ConflictResult captures the outcome of a conflict resolution.
type ConflictResult struct {
	Winner     string // "local" or "remote"
	OldPath    string // path where the loser was preserved
	Resolution string // e.g. "newer_wins:local"
}

// conflictDeps groups the per-Puller dependencies resolveConflict needs
// (as opposed to the per-call arguments change/journal/localMtime), so the
// parameter list does not keep growing every time a new cross-cutting
// dependency (like Matcher) is threaded through. See homedrive-conventions
// (functions < 80 lines) and homedrive-conflict-resolution.
type conflictDeps struct {
	log       *slog.Logger
	store     Store
	remote    RemoteFS
	pub       Publisher
	localRoot string
	matcher   *oldsuffix.Matcher
	policy    ConflictPolicy
	dryRun    bool
	clock     func() time.Time
}

// resolveConflict handles a conflict between local and remote versions
// of a file during a pull operation. It follows the algorithm in
// PLAN.md section 11.2.
//
// Returns the resolution result, or an error if the conflict could not
// be resolved (e.g. rename of loser failed).
func resolveConflict(
	ctx context.Context,
	deps conflictDeps,
	change Change,
	journal JournalEntry,
	localMtime time.Time,
) (ConflictResult, error) {
	log := deps.log
	remoteMtime := change.Object.ModTime
	path := change.Path

	// Emit conflict.detected event.
	if deps.pub != nil {
		_ = deps.pub.PublishJSON(deps.pub.Topic("events", "conflict.detected"), map[string]any{
			"ts":           deps.clock().UTC().Format(time.RFC3339),
			"type":         "conflict.detected",
			"path":         path,
			"local_mtime":  localMtime.UTC().Format(time.RFC3339),
			"remote_mtime": remoteMtime.UTC().Format(time.RFC3339),
		})
	}

	winner := determineWinner(localMtime, remoteMtime, deps.policy)

	log.Info("conflict detected",
		"path", path,
		"op", "conflict",
		"local_mtime", localMtime,
		"remote_mtime", remoteMtime,
		"winner", winner,
		"origin", "remote",
	)

	if localMtime.Equal(remoteMtime) {
		log.Warn("equal mtime conflict, checksums differ",
			"path", path,
			"op", "conflict",
		)
	}

	base, n, err := deps.store.NextOldN(ctx, path)
	if err != nil {
		return ConflictResult{}, fmt.Errorf("computing next .old.N for %s: %w", path, err)
	}
	oldPath := deps.matcher.Format(base, n)

	result := ConflictResult{
		Winner:     winner,
		OldPath:    oldPath,
		Resolution: fmt.Sprintf("%s:%s", deps.policy, winner),
	}

	if deps.dryRun {
		log.Info("dry-run: would resolve conflict",
			"path", path,
			"op", "conflict",
			"resolution", result.Resolution,
			"old_path", oldPath,
		)
		return result, nil
	}

	if winner == "remote" {
		// Remote wins: rename local file to .old.<N>, then download remote.
		localFile := fmt.Sprintf("%s/%s", deps.localRoot, path)
		localOld := fmt.Sprintf("%s/%s", deps.localRoot, oldPath)
		if err := os.Rename(localFile, localOld); err != nil {
			log.Error("failed to rename local loser",
				"path", path,
				"old_path", oldPath,
				"error", err,
			)
			return ConflictResult{}, fmt.Errorf("renaming local loser %s: %w", path, err)
		}
		// Record the .old.<N> in the journal so NextOldN skips it next time.
		if err := deps.store.Put(ctx, JournalEntry{
			Path:         oldPath,
			LocalMtime:   localMtime,
			LastSyncedAt: deps.clock(),
			LastOrigin:   "local",
		}); err != nil {
			return ConflictResult{}, fmt.Errorf("recording old entry %s: %w", oldPath, err)
		}
		// Download the remote winner.
		if err := deps.remote.DownloadFile(ctx, path, localFile); err != nil {
			return ConflictResult{}, fmt.Errorf("downloading remote winner %s: %w", path, err)
		}
	} else {
		// Local wins: rename remote file to .old.<N> on remote side.
		if err := deps.remote.MoveFile(ctx, path, oldPath); err != nil {
			log.Error("failed to rename remote loser",
				"path", path,
				"old_path", oldPath,
				"error", err,
			)
			return ConflictResult{}, fmt.Errorf("renaming remote loser %s: %w", path, err)
		}
		// Record the .old.<N> in the journal.
		if err := deps.store.Put(ctx, JournalEntry{
			Path:         oldPath,
			RemoteMtime:  change.Object.ModTime,
			RemoteMD5:    change.Object.MD5,
			RemoteID:     change.Object.RemoteID,
			LastSyncedAt: deps.clock(),
			LastOrigin:   "remote",
		}); err != nil {
			return ConflictResult{}, fmt.Errorf("recording old entry %s: %w", oldPath, err)
		}
		// Upload the local winner.
		localFile := fmt.Sprintf("%s/%s", deps.localRoot, path)
		if _, err := deps.remote.CopyFile(ctx, localFile, path); err != nil {
			return ConflictResult{}, fmt.Errorf("uploading local winner %s: %w", path, err)
		}
	}

	// Emit conflict.resolved event.
	if deps.pub != nil {
		_ = deps.pub.PublishJSON(deps.pub.Topic("events", "conflict.resolved"), map[string]any{
			"ts":          deps.clock().UTC().Format(time.RFC3339),
			"type":        "conflict.resolved",
			"path":        path,
			"resolution":  result.Resolution,
			"kept_old_as": oldPath,
		})
	}

	return result, nil
}

// determineWinner returns "local" or "remote" based on mtime comparison
// and the configured conflict policy.
func determineWinner(localMtime, remoteMtime time.Time, policy ConflictPolicy) string {
	switch policy {
	case PolicyLocalWins:
		return "local"
	case PolicyRemoteWins:
		return "remote"
	default: // PolicyNewerWins
		if localMtime.After(remoteMtime) {
			return "local"
		}
		if remoteMtime.After(localMtime) {
			return "remote"
		}
		// Equal mtime, checksums differ: default to local wins.
		return "local"
	}
}
