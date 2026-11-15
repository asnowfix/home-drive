package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// processChange handles a single remote change: download or delete.
func (p *Puller) processChange(ctx context.Context, ch Change) error {
	start := p.clock()

	if ch.Deleted {
		return p.recordUnrecordedDelete(ch, p.processDelete(ctx, ch, start))
	}
	if ch.Object != nil && ch.Object.IsDir {
		p.skipDirChange(ch)
		return nil
	}
	return p.recordUnrecorded(ch, p.processFileChange(ctx, ch, start))
}

// recordedErr marks an error whose final outcome a leaf already recorded,
// so processChange does not record it a second time.
type recordedErr struct{ error }

func (e recordedErr) Unwrap() error { return e.error }

// recordUnrecordedDelete is recordUnrecorded for the delete path.
func (p *Puller) recordUnrecordedDelete(ch Change, err error) error {
	var rec recordedErr
	if err == nil || errors.As(err, &rec) {
		return err
	}
	return p.recordFail(Outcome{Op: OutPullDelete, Path: ch.Path, Err: err})
}

// recordFail records a failed outcome and returns its error marked as
// recorded.
func (p *Puller) recordFail(o Outcome) error {
	p.recordOutcome(o)
	return recordedErr{o.Err}
}

// recordUnrecorded records a pull-side failure for any error a leaf did
// not record itself (journal reads, stat, mkdir, ...), so every final
// failure reaches errors / errors_down / last_error.
func (p *Puller) recordUnrecorded(ch Change, err error) error {
	var rec recordedErr
	if err == nil || errors.As(err, &rec) {
		return err
	}
	return p.recordFail(Outcome{Op: OutPull, Dir: DirDown, Path: ch.Path, Err: err})
}

// processDelete handles a remote deletion.
func (p *Puller) processDelete(ctx context.Context, ch Change, start time.Time) error {
	p.log.Info("remote file deleted",
		"path", ch.Path,
		"op", "pull_delete",
		"origin", "remote",
	)

	if p.cfg.DryRun {
		p.log.Info("dry-run: would delete local file",
			"path", ch.Path,
			"op", "pull_delete",
		)
		p.logAudit(AuditEntry{
			Timestamp: start,
			Op:        "pull_delete",
			Path:      ch.Path,
			Origin:    "remote",
			DryRun:    true,
		})
		return nil
	}

	localPath := filepath.Join(p.cfg.LocalRoot, ch.Path)
	if err := p.removeLocal(ch.Path, localPath); err != nil {
		p.log.Error("failed to delete local file",
			"path", ch.Path,
			"op", "pull_delete",
			"error", err,
		)
		err = fmt.Errorf("deleting local file %s: %w", ch.Path, err)
		return p.recordFail(Outcome{Op: OutPullDelete, Path: ch.Path, Err: err})
	}

	if err := p.store.Delete(ctx, ch.Path); err != nil {
		err = fmt.Errorf("removing journal entry for %s: %w", ch.Path, err)
		return p.recordFail(Outcome{Op: OutPullDelete, Path: ch.Path, Err: err})
	}
	p.recordOutcome(Outcome{Op: OutPullDelete, Path: ch.Path})

	p.logAudit(AuditEntry{
		Timestamp:  start,
		Op:         "pull_delete",
		Path:       ch.Path,
		Origin:     "remote",
		DurationMs: p.clock().Sub(start).Milliseconds(),
	})

	return nil
}

// processFileChange handles a remote file create or update.
func (p *Puller) processFileChange(ctx context.Context, ch Change, start time.Time) error {
	journal, exists, err := p.store.Get(ctx, ch.Path)
	if err != nil {
		return fmt.Errorf("reading journal for %s: %w", ch.Path, err)
	}

	if exists {
		if same, err := p.adoptIdenticalRemote(ctx, ch, journal); err != nil || same {
			return err
		}
		isConflict, localMtime, conflictErr := p.detectConflict(ch, journal)
		if conflictErr != nil {
			return conflictErr
		}
		if isConflict {
			return p.handleConflict(ctx, ch, journal, localMtime, start)
		}
		// Remote mtime unchanged since last recorded sync: already in sync, skip.
		diff := ch.Object.ModTime.Sub(journal.RemoteMtime)
		if diff < 0 {
			diff = -diff
		}
		if diff <= time.Second {
			return nil
		}
	}

	return p.downloadFile(ctx, ch, start)
}

// detectConflict checks whether a remote change conflicts with local state.
// A conflict exists when the local file has been modified since the last
// recorded sync (local mtime differs from what the journal expected).
func (p *Puller) detectConflict(
	ch Change,
	journal JournalEntry,
) (bool, time.Time, error) {
	localPath := filepath.Join(p.cfg.LocalRoot, ch.Path)
	info, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, fmt.Errorf("stat local file %s: %w", ch.Path, err)
	}

	localMtime := info.ModTime()

	// Same remote MD5: no meaningful remote change, no conflict.
	if ch.Object.MD5 == journal.RemoteMD5 {
		return false, localMtime, nil
	}

	// Local mtime matches journal within 1s tolerance: no local change.
	diff := localMtime.Sub(journal.LocalMtime)
	if diff < 0 {
		diff = -diff
	}
	if diff <= time.Second {
		return false, localMtime, nil
	}

	// Local file was modified AND remote changed: conflict.
	return true, localMtime, nil
}

// handleConflict runs the conflict resolution algorithm and updates the
// journal with the winning state.
func (p *Puller) handleConflict(
	ctx context.Context,
	ch Change,
	journal JournalEntry,
	localMtime time.Time,
	start time.Time,
) error {
	result, err := resolveConflict(ctx, conflictDeps{
		log:       p.log,
		store:     p.store,
		remote:    p.remote,
		pub:       p.pub,
		localRoot: p.cfg.LocalRoot,
		matcher:   p.cfg.Matcher,
		policy:    p.cfg.ConflictPolicy,
		dryRun:    p.cfg.DryRun,
		clock:     p.clock,
		retention: p.cfg.Retention,
		auditor:   p.cfg.Auditor,
	}, ch, journal, localMtime)
	if err != nil {
		p.emitPullFailure(ch.Path, err)
		p.logAudit(AuditEntry{
			Timestamp:  start,
			Op:         "conflict",
			Path:       ch.Path,
			Origin:     "remote",
			DurationMs: p.clock().Sub(start).Milliseconds(),
			Error:      err.Error(),
		})
		err = fmt.Errorf("resolving conflict for %s: %w", ch.Path, err)
		return p.recordFail(Outcome{Op: OutConflict, Path: ch.Path, Err: err})
	}

	if !p.cfg.DryRun {
		entry := p.buildConflictEntry(ch, result, localMtime)
		if err := p.store.Put(ctx, entry); err != nil {
			return p.recordFail(Outcome{Op: OutConflict, Path: ch.Path,
				Err: fmt.Errorf("updating journal after conflict for %s: %w", ch.Path, err)})
		}
	}

	p.logAudit(AuditEntry{
		Timestamp:  start,
		Op:         "conflict",
		Path:       ch.Path,
		Origin:     "remote",
		Resolution: result.Resolution,
		OldPath:    result.OldPath,
		DurationMs: p.clock().Sub(start).Milliseconds(),
		DryRun:     p.cfg.DryRun,
	})

	if !p.cfg.DryRun {
		p.recordOutcome(Outcome{Op: OutConflict, Path: ch.Path})
	}
	p.emitPullSuccess(ch.Path, ch.Object.Size)
	return nil
}

// buildConflictEntry creates a JournalEntry after conflict resolution.
func (p *Puller) buildConflictEntry(
	ch Change,
	result ConflictResult,
	localMtime time.Time,
) JournalEntry {
	now := p.clock()
	entry := JournalEntry{
		Path:         ch.Path,
		RemoteMtime:  ch.Object.ModTime,
		RemoteMD5:    ch.Object.MD5,
		RemoteID:     ch.Object.RemoteID,
		LastSyncedAt: now,
		LastOrigin:   "remote",
	}
	if result.Winner == "remote" {
		entry.LocalMtime = ch.Object.ModTime
	} else {
		entry.LocalMtime = localMtime
	}
	return entry
}

// downloadFile downloads a remote file to the local root and records the
// result in the journal and audit log.
func (p *Puller) downloadFile(ctx context.Context, ch Change, start time.Time) error {
	localPath := filepath.Join(p.cfg.LocalRoot, ch.Path)

	p.log.Info("downloading remote file",
		"path", ch.Path,
		"op", "pull",
		"bytes", ch.Object.Size,
		"origin", "remote",
	)

	if p.cfg.DryRun {
		return p.recordDryRunDownload(ch, start)
	}

	if p.knownDangling(ch) {
		p.log.Debug("skipping known dangling shortcut",
			"path", ch.Path, "op", "pull", "origin", "remote")
		return nil
	}

	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	if err := p.remote.DownloadFile(ctx, ch.Path, localPath); err != nil {
		if errors.Is(err, ErrDanglingShortcut) {
			p.skipDanglingShortcut(ch, err, start)
			return nil
		}
		p.emitPullFailure(ch.Path, err)
		p.logAudit(AuditEntry{
			Timestamp:  start,
			Op:         "pull",
			Path:       ch.Path,
			Origin:     "remote",
			DurationMs: p.clock().Sub(start).Milliseconds(),
			Error:      err.Error(),
		})
		err = fmt.Errorf("downloading %s: %w", ch.Path, err)
		return p.recordFail(Outcome{Op: OutPull, Dir: DirDown, Path: ch.Path, Err: err})
	}

	return p.recordDownload(ctx, ch, localPath, start)
}

// recordDryRunDownload logs and audits a download that was skipped
// because dry-run mode is active.
func (p *Puller) recordDryRunDownload(ch Change, start time.Time) error {
	p.log.Info("dry-run: would download file",
		"path", ch.Path,
		"op", "pull",
		"bytes", ch.Object.Size,
	)
	p.logAudit(AuditEntry{
		Timestamp: start,
		Op:        "pull",
		Path:      ch.Path,
		Origin:    "remote",
		Bytes:     ch.Object.Size,
		DryRun:    true,
	})
	return nil
}

// recordDownload updates the journal and audit log after a successful
// file download. The recorded local mtime enables loop prevention:
// the watcher will ignore the filesystem event for this file.
func (p *Puller) recordDownload(
	ctx context.Context,
	ch Change,
	localPath string,
	start time.Time,
) error {
	info, err := os.Stat(localPath)
	if err != nil {
		err = fmt.Errorf("stat after download %s: %w", ch.Path, err)
		return p.recordFail(Outcome{Op: OutPull, Dir: DirDown, Path: ch.Path, Err: err})
	}

	now := p.clock()
	entry := JournalEntry{
		Path:         ch.Path,
		LocalMtime:   info.ModTime(),
		RemoteMtime:  ch.Object.ModTime,
		RemoteMD5:    ch.Object.MD5,
		RemoteID:     ch.Object.RemoteID,
		LastSyncedAt: now,
		LastOrigin:   "remote",
	}
	if err := p.store.Put(ctx, entry); err != nil {
		err = fmt.Errorf("updating journal for %s: %w", ch.Path, err)
		return p.recordFail(Outcome{Op: OutPull, Dir: DirDown, Path: ch.Path, Err: err})
	}
	p.recordOutcome(Outcome{Op: OutPull, Dir: DirDown, Path: ch.Path, Bytes: ch.Object.Size})

	elapsed := p.clock().Sub(start)
	p.logAudit(AuditEntry{
		Timestamp:  start,
		Op:         "pull",
		Path:       ch.Path,
		Origin:     "remote",
		Bytes:      ch.Object.Size,
		DurationMs: elapsed.Milliseconds(),
	})

	p.emitPullSuccess(ch.Path, ch.Object.Size)
	return nil
}
