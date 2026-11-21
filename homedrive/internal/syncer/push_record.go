// push_record.go holds the push syncer's result recording: directory
// renames, per-file outcomes (store, audit, MQTT, stats) and dry-run
// logging. Split out of syncer.go to keep that file under 500 lines.
package syncer

import (
	"context"
	"log/slog"
	"time"
)

// handleDirRename processes a directory rename with a single MoveDir
// call and a bulk store prefix rewrite.
func (s *Syncer) handleDirRename(ctx context.Context, dr DirRename) {
	start := s.nowFn()
	logger := s.logger.With("op", "dir_rename", "from", dr.From, "to", dr.To)

	if s.cfg.DryRun {
		s.logDryRunDirRename(logger, dr)
		return
	}

	// Acquire read lock for push/bisync coordination.
	s.bisyncMu.RLock()
	defer s.bisyncMu.RUnlock()

	blocked, err := s.retryGated(ctx, logger, "dir_rename", func(ctx context.Context) error {
		return s.remote.MoveDir(ctx, dr.From, dr.To)
	})
	if blocked {
		return
	}

	elapsed := s.nowFn().Sub(start)

	if err == nil {
		count, rewriteErr := s.store.RewritePrefix(ctx, dr.From, dr.To)
		if rewriteErr != nil {
			logger.Error("store prefix rewrite failed",
				"error", rewriteErr.Error(),
				"duration_ms", elapsed.Milliseconds(),
			)
		} else {
			logger.Info("directory rename synced",
				"files_count", count,
				"duration_ms", elapsed.Milliseconds(),
			)
		}
		s.appendAudit(AuditEntry{
			Timestamp:  s.nowFn(),
			Op:         "dir_rename",
			From:       dr.From,
			To:         dr.To,
			FilesCount: count,
			DurationMs: elapsed.Milliseconds(),
		})
		emitOutcome(s.recorder, Outcome{Op: OutDirRename, Path: dr.To, At: s.nowFn()})
		s.publishEvent("push.success", map[string]any{
			"ts":          s.eventTS(),
			"type":        "dir_rename",
			"from":        dr.From,
			"to":          dr.To,
			"files_count": count,
			"duration_ms": elapsed.Milliseconds(),
		})
		return
	}

	logger.Error("directory rename failed",
		"error", err.Error(),
		"duration_ms", elapsed.Milliseconds(),
	)
	s.appendAudit(AuditEntry{
		Timestamp:  s.nowFn(),
		Op:         "dir_rename",
		From:       dr.From,
		To:         dr.To,
		Error:      err.Error(),
		DurationMs: elapsed.Milliseconds(),
	})
	emitOutcome(s.recorder, Outcome{Op: OutDirRename, Path: dr.To, Err: err, At: s.nowFn()})
	s.publishEvent("push.failure", map[string]any{
		"ts":          s.eventTS(),
		"type":        "dir_rename",
		"from":        dr.From,
		"to":          dr.To,
		"error":       err.Error(),
		"duration_ms": elapsed.Milliseconds(),
	})
}

// recordResult records the outcome of a push operation in the store,
// audit log, and MQTT.
func (s *Syncer) recordResult(
	ctx context.Context,
	path, op string,
	obj *RemoteObject,
	err error,
	elapsed time.Duration,
) {
	if err != nil {
		s.logger.Error("push failed",
			"path", path,
			"op", op,
			"error", err.Error(),
			"duration_ms", elapsed.Milliseconds(),
		)
		s.appendAudit(AuditEntry{
			Timestamp:  s.nowFn(),
			Op:         op,
			Path:       path,
			Error:      err.Error(),
			DurationMs: elapsed.Milliseconds(),
		})
		s.publishEvent("push.failure", map[string]any{
			"ts":          s.eventTS(),
			"type":        op,
			"path":        path,
			"error":       err.Error(),
			"duration_ms": elapsed.Milliseconds(),
		})
		emitOutcome(s.recorder, Outcome{Op: pushOutcomeOp(op), Dir: pushOutcomeDir(op), Path: path, Err: err, At: s.nowFn()})
		return
	}

	s.logger.Info("push succeeded",
		"path", path,
		"op", op,
		"duration_ms", elapsed.Milliseconds(),
	)

	// Record sync in journal (ignore store errors, they are logged).
	record := s.pushEntry(path, obj)
	if putErr := s.store.Put(ctx, record); putErr != nil {
		s.logger.Error("store put failed",
			"path", path,
			"error", putErr.Error(),
		)
	}

	s.appendAudit(AuditEntry{
		Timestamp:  s.nowFn(),
		Op:         op,
		Path:       path,
		DurationMs: elapsed.Milliseconds(),
	})
	out := Outcome{Op: pushOutcomeOp(op), Dir: pushOutcomeDir(op), Path: path, At: s.nowFn()}
	if obj != nil && out.Dir == DirUp {
		out.Bytes = obj.Size
	}
	emitOutcome(s.recorder, out)
	s.publishEvent("push.success", map[string]any{
		"ts":          s.eventTS(),
		"type":        op,
		"path":        path,
		"duration_ms": elapsed.Milliseconds(),
	})
}

// logDryRun logs what would happen for a file event in dry-run mode.
func (s *Syncer) logDryRun(logger *slog.Logger, ev Event) {
	logger.Info("dry-run: would execute push",
		"path", ev.Path,
		"op", ev.Op.String(),
	)
	s.appendAudit(AuditEntry{
		Timestamp: s.nowFn(),
		Op:        ev.Op.String(),
		Path:      ev.Path,
		DryRun:    true,
	})
}

// logDryRunDirRename logs what would happen for a dir rename in dry-run mode.
func (s *Syncer) logDryRunDirRename(logger *slog.Logger, dr DirRename) {
	logger.Info("dry-run: would execute dir rename",
		"from", dr.From,
		"to", dr.To,
	)
	s.appendAudit(AuditEntry{
		Timestamp: s.nowFn(),
		Op:        "dir_rename",
		From:      dr.From,
		To:        dr.To,
		DryRun:    true,
	})
}

// eventTS is the "ts" value of push events: RFC3339, UTC, second
// precision, matching every other event payload (issue #141).
func (s *Syncer) eventTS() string {
	return s.nowFn().UTC().Format(time.RFC3339)
}

// pushOutcomeOp maps the watcher op name (create/write/remove) to the
// Outcome Op recorded for it.
func pushOutcomeOp(op string) string {
	if op == OpRemove.String() {
		return OutPushDelete
	}
	return OutPush
}

// pushOutcomeDir is DirUp for content uploads and "" for deletes.
func pushOutcomeDir(op string) string {
	if op == OpRemove.String() {
		return ""
	}
	return DirUp
}
