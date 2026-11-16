package syncer

import (
	"log/slog"
	"time"
)

// deleteConflictResolution is the audit/event resolution for a
// delete-vs-modify conflict (issue #152), used by both the puller and
// bisync: one side deleted a file the other side changed, and the modified
// copy is kept. No loser is renamed, so no conflict.resolved event exists
// for it (see the homedrive-conflict-resolution skill).
const deleteConflictResolution = "delete_vs_modify:keep_modified"

// publishDeleteConflict emits conflict.detected for a delete-vs-modify
// conflict. A publish failure is logged, never fatal. dryRun is carried in
// the payload so consumers can tell a rehearsal from a real conflict.
func publishDeleteConflict(
	pub Publisher, log *slog.Logger, now time.Time, path string, dryRun bool,
) {
	if pub == nil {
		return
	}
	err := pub.PublishJSON(pub.Topic("events", "conflict.detected"), map[string]any{
		"ts":         now.UTC().Format(time.RFC3339),
		"type":       "conflict.detected",
		"path":       path,
		"resolution": deleteConflictResolution,
		"dry_run":    dryRun,
	})
	if err != nil {
		log.Warn("failed to publish conflict event",
			"path", path, "op", "conflict", "error", err)
	}
}
