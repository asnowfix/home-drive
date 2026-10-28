package syncer

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
