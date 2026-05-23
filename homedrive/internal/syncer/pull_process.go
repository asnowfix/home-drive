package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// processChange handles a single remote change: download or delete.
func (p *Puller) processChange(ctx context.Context, ch Change) error {
	start := p.clock()

	if ch.Deleted {
		return p.processDelete(ctx, ch, start)
	}
	return p.processFileChange(ctx, ch, start)
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
	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		p.log.Error("failed to delete local file",
			"path", ch.Path,
			"op", "pull_delete",
			"error", err,
		)
		return fmt.Errorf("deleting local file %s: %w", ch.Path, err)
	}

	if err := p.store.Delete(ctx, ch.Path); err != nil {
		return fmt.Errorf("removing journal entry for %s: %w", ch.Path, err)
	}

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
		isConflict, localMtime, conflictErr := p.detectConflict(ch, journal)
		if conflictErr != nil {
			return conflictErr
		}
		if isConflict {
			return p.handleConflict(ctx, ch, journal, localMtime, start)
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
	result, err := resolveConflict(
		ctx, p.log, p.store, p.remote, p.pub,
		p.cfg.LocalRoot, ch, journal, localMtime,
		p.cfg.ConflictPolicy, p.cfg.DryRun, p.clock,
	)
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
		return fmt.Errorf("resolving conflict for %s: %w", ch.Path, err)
	}

	if !p.cfg.DryRun {
		entry := p.buildConflictEntry(ch, result, localMtime)
		if err := p.store.Put(ctx, entry); err != nil {
			return fmt.Errorf("updating journal after conflict for %s: %w", ch.Path, err)
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
		RemoteID:     ch.Object.ID,
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

	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	if err := p.remote.DownloadFile(ctx, ch.Path, localPath); err != nil {
		p.emitPullFailure(ch.Path, err)
		p.logAudit(AuditEntry{
			Timestamp:  start,
			Op:         "pull",
			Path:       ch.Path,
			Origin:     "remote",
			DurationMs: p.clock().Sub(start).Milliseconds(),
			Error:      err.Error(),
		})
		return fmt.Errorf("downloading %s: %w", ch.Path, err)
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
		return fmt.Errorf("stat after download %s: %w", ch.Path, err)
	}

	now := p.clock()
	entry := JournalEntry{
		Path:         ch.Path,
		LocalMtime:   info.ModTime(),
		RemoteMtime:  ch.Object.ModTime,
		RemoteMD5:    ch.Object.MD5,
		RemoteID:     ch.Object.ID,
		LastSyncedAt: now,
		LastOrigin:   "remote",
	}
	if err := p.store.Put(ctx, entry); err != nil {
		return fmt.Errorf("updating journal for %s: %w", ch.Path, err)
	}

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
