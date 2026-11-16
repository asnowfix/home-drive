package syncer

import (
	"context"
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

// removeLocal removes localPath for a remote delete. A missing path is
// not an error. A deleted change carries no Object, so a remote folder
// delete arrives here too (issue #114).
//
// Drive is not known to report an entry per descendant of a deleted
// folder, and the ID-to-path cache that resolves such entries is lost on
// restart, so the folder entry alone must be enough (issue #152): when
// relPath is not a regular file, every journaled descendant is removed
// via removeSyncedDescendants. A directory that still holds entries after
// that is left in place: whatever remains is unsynced or locally modified
// data that must not be deleted. File removal failures stay errors.
func (p *Puller) removeLocal(
	ctx context.Context, relPath, localPath string, start time.Time,
) error {
	info, statErr := os.Lstat(localPath)
	if statErr == nil && !info.IsDir() {
		return removeIgnoringMissing(localPath)
	}
	if err := p.removeSyncedDescendants(ctx, relPath, localPath, start, false); err != nil {
		return err
	}
	err := removeLocalPath(localPath)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	if info, statErr := os.Lstat(localPath); statErr == nil && info.IsDir() {
		p.log.Info("remote folder deleted: keeping non-empty local directory",
			"path", relPath,
			"op", "pull_delete_dir",
			"origin", "remote",
			"error", err,
		)
		return nil
	}
	return err
}

func removeIgnoringMissing(localPath string) error {
	err := removeLocalPath(localPath)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return err
}
