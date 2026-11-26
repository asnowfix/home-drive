package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// Reasons a removed local directory is kept on Drive (issue #174). They are
// the audit "resolution" of a remove_dir_kept entry.
const (
	keptNotEmpty    = "not_empty"
	keptUnknown     = "not_known_synced"
	keptLocalExists = "exists_locally"
	keptRootOrBad   = "root_or_invalid_path"
)

// removeOutcome says what execRemove did, so handleEvent can log and audit
// a directory removal for what it was instead of "push succeeded".
type removeOutcome struct {
	dir     bool   // the path named a remote folder
	trashed bool   // the folder was moved to the Drive trash
	kept    string // non-empty: the folder was kept on Drive, and why
}

// execRemove deletes the remote object for the root-relative path rel.
// An already-absent object (ErrRemoteNotFound) is the desired end state, so
// it counts as success: returning nil stops retryFunc from retrying five
// times with backoff and keeps recordResult from emitting push.failure
// (issue #107). Typical cause: the pull side already removed the object.
// A remote folder goes through execRemoveDir (issues #115, #174).
func (s *Syncer) execRemove(ctx context.Context, rel string) (removeOutcome, error) {
	err := s.remote.DeleteFile(ctx, rel)
	switch {
	case err == nil:
		return removeOutcome{}, nil
	case errors.Is(err, ErrRemoteNotFound):
		s.logger.Debug("remote object already absent, delete is a no-op", "path", rel)
		return removeOutcome{}, nil
	case errors.Is(err, ErrRemoteIsDir):
		return s.execRemoveDir(ctx, rel)
	}
	return removeOutcome{}, fmt.Errorf("delete file %q: %w", rel, err)
}

// execRemoveDir trashes the Drive folder of a removed local directory, only
// when every guard agrees (issue #174): the path is not the root, the local
// directory is still gone, the folder is known to be synced, and Drive
// holds nothing under it, both by a List at call time and by
// RemoveEmptyDir's own check of every child outside the trash. A
// folder that still holds something returns ErrDirNotEmpty, so retryFunc
// gives in-flight child deletions (rm -r) time to land; a List error is
// retried the same way and never deletes. Anything else keeps the folder.
func (s *Syncer) execRemoveDir(ctx context.Context, rel string) (removeOutcome, error) {
	out := removeOutcome{dir: true}
	reason, err := s.dirRemoveVeto(ctx, rel)
	if err != nil {
		return out, err
	}
	if reason != "" {
		out.kept = reason
		return out, nil
	}
	objs, err := s.remote.List(ctx, rel)
	if err != nil {
		return out, fmt.Errorf("list remote dir %q: %w", rel, err)
	}
	if len(objs) > 0 {
		return out, fmt.Errorf("remote dir %q holds %d file(s): %w",
			rel, len(objs), rcloneclient.ErrDirNotEmpty)
	}
	err = s.remote.RemoveEmptyDir(ctx, rel)
	switch {
	case err == nil:
		out.trashed = true
		return out, nil
	case errors.Is(err, ErrRemoteNotFound):
		s.logger.Debug("remote folder already absent, remove is a no-op", "path", rel)
		return out, nil
	}
	return out, fmt.Errorf("remove dir %q: %w", rel, err)
}

// dirRemoveVeto returns why the folder rel must be kept on Drive, or "" if
// it may be removed. A journal read failure is returned as an error (and
// retried): it must never count as "known".
func (s *Syncer) dirRemoveVeto(ctx context.Context, rel string) (string, error) {
	if !rcloneclient.ValidRemotePath(rel) {
		return keptRootOrBad, nil // "" (the root), ".", "..", absolute, ...
	}
	if _, err := os.Lstat(filepath.Join(s.cfg.LocalRoot, filepath.FromSlash(rel))); err == nil {
		return keptLocalExists, nil // recreated locally since the event
	}
	under, err := s.store.ListOldSiblings(ctx, rel+"/")
	if err != nil {
		return "", fmt.Errorf("journal lookup under %q: %w", rel, err)
	}
	if len(under) > 0 {
		return "", nil
	}
	e, found, err := s.store.Get(ctx, rel)
	if err != nil {
		return "", fmt.Errorf("journal lookup %q: %w", rel, err)
	}
	if found && e.RemoteID != "" {
		return "", nil
	}
	return keptUnknown, nil
}

// recordDirRemove logs and audits the removal of a local directory whose
// remote counterpart is a folder, and reports whether it handled the
// event (false: not a directory, recordResult records it). It never writes
// a journal entry for the folder path and never says "push succeeded"
// for a folder it kept.
func (s *Syncer) recordDirRemove(rel string, out removeOutcome, err error, elapsedMs int64) bool {
	if err != nil && errors.Is(err, rcloneclient.ErrDirNotEmpty) {
		out = removeOutcome{dir: true, kept: keptNotEmpty}
		err = nil
	}
	if !out.dir || err != nil {
		return false
	}
	logger := s.logger.With("path", rel, "op", "remove_dir", "origin", "local")
	switch {
	case out.trashed:
		logger.Info("remove dir: empty folder moved to the Drive trash", "duration_ms", elapsedMs)
		s.appendAudit(AuditEntry{Timestamp: s.nowFn(), Op: "remove_dir", Path: rel,
			Origin: "local", DurationMs: elapsedMs})
		emitOutcome(s.recorder, Outcome{Op: OutPushDelete, Path: rel, At: s.nowFn()})
		s.publishEvent("push.success", map[string]any{
			"ts": s.eventTS(), "type": "remove_dir", "path": rel, "duration_ms": elapsedMs,
		})
	case out.kept != "":
		logger.Info("kept on Drive: folder not removed", "reason", out.kept, "duration_ms", elapsedMs)
		s.appendAudit(AuditEntry{Timestamp: s.nowFn(), Op: "remove_dir_kept", Path: rel,
			Origin: "local", Resolution: out.kept, DurationMs: elapsedMs})
	default:
		logger.Debug("remove dir: folder already absent on Drive")
	}
	return true
}
