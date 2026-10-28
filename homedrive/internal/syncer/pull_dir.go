package syncer

import "os"

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
// delete arrives here too (issue #114): a directory that still holds
// entries is left in place, since its synced children are removed by
// their own change entries and anything else in it is unsynced local
// data that must not be deleted. File removal failures stay errors.
func (p *Puller) removeLocal(relPath, localPath string) error {
	err := os.Remove(localPath)
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
