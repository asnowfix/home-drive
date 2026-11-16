package syncer

import (
	"context"
	"fmt"
	"os"
	"time"
)

// skipDirChange handles a remote folder create or update by ignoring it
// (issue #105). Only files are synced: local directories come into
// existence when a file beneath them is downloaded, so an empty Drive
// folder is deliberately not mirrored (and an empty local directory is not
// pushed either). Folders are never downloaded as files and carry no
// journal entry.
func (p *Puller) skipDirChange(ch Change) {
	p.log.Debug("skipping remote folder: only files are synced",
		"path", ch.Path,
		"op", "pull_skip_dir",
		"origin", "remote",
	)
}

// deleteOutcome says what removeLocal did, so processDelete audits and
// journals only what really happened.
type deleteOutcome int

const (
	// deleteNothing: the path was already absent, or a directory was kept.
	deleteNothing deleteOutcome = iota
	// deleteRemoved: a file or directory was removed.
	deleteRemoved
	// deleteKept: a locally modified file was kept (conflict reported, its
	// journal entry already dropped).
	deleteKept
	// deleteStale: the change's file ID is not the one journaled for the
	// path, so it concerns a different file; nothing touched, journal intact.
	deleteStale
)

// removeLocal applies a remote delete of ch.Path at localPath. A missing
// path is not an error. A deleted change carries no Object, so a remote
// folder delete arrives here too (issue #114).
//
// A regular file is deleted only if it is safe to: when the journal holds a
// baseline for it and its mtime moved since, it is kept as a delete-vs-modify
// conflict; when the journal recorded another file ID for the path, the
// change is stale and ignored. A path with no journal entry is deleted as
// before (an explicit delete of a path we have no record of).
//
// Drive is not known to report an entry per descendant of a deleted folder,
// and the ID-to-path cache that resolves such entries is lost on restart,
// so the folder entry alone must be enough (issue #152): when the path is a
// directory (or already gone), every journaled descendant is removed via
// removeSyncedDescendants. A directory that still holds entries after that
// is left in place: whatever remains is unsynced or locally modified data
// that must not be deleted. File removal failures stay errors.
func (p *Puller) removeLocal(
	ctx context.Context, ch Change, localPath string, start time.Time,
) (deleteOutcome, error) {
	info, statErr := os.Lstat(localPath)
	if statErr == nil && !info.IsDir() {
		return p.removeLocalFile(ctx, ch, info, localPath, start)
	}
	existed := statErr == nil
	if err := p.removeSyncedDescendants(ctx, ch.Path, localPath, start, false); err != nil {
		return deleteNothing, err
	}
	err := removeLocalPath(localPath)
	switch {
	case err == nil:
		return deleteRemoved, nil
	case os.IsNotExist(err):
		if existed {
			// removed by the descendant sweep's pruning
			return deleteRemoved, nil
		}
		return deleteNothing, nil
	}
	if info, statErr := os.Lstat(localPath); statErr == nil && info.IsDir() {
		p.log.Info("remote folder deleted: keeping non-empty local directory",
			"path", ch.Path,
			"op", "pull_delete_dir",
			"origin", "remote",
			"error", err,
		)
		return deleteNothing, nil
	}
	return deleteNothing, err
}

// removeLocalFile is removeLocal for a path that is not a directory.
func (p *Puller) removeLocalFile(
	ctx context.Context, ch Change, info os.FileInfo, localPath string, start time.Time,
) (deleteOutcome, error) {
	e, found, err := p.store.Get(ctx, ch.Path)
	if err != nil {
		return deleteNothing, fmt.Errorf("reading journal for %s: %w", ch.Path, err)
	}
	if found && e.RemoteID != "" && ch.ID != "" && e.RemoteID != ch.ID {
		p.log.Warn("remote delete is for another file ID than the journaled one: ignoring",
			"path", ch.Path, "op", "pull_delete", "change_id", ch.ID, "journal_id", e.RemoteID)
		return deleteStale, nil
	}
	if found && info.Mode().IsRegular() && hasBaseline(&e) &&
		!mtimeClose(info.ModTime(), e.LocalMtime) {
		return deleteKept, p.keepModifiedDescendant(ctx, e, info.ModTime(), start, false)
	}
	err = removeLocalPath(localPath)
	if err == nil {
		return deleteRemoved, nil
	}
	if os.IsNotExist(err) {
		return deleteNothing, nil
	}
	return deleteNothing, err
}
