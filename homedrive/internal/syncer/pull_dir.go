package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// processDirChange handles a remote folder create or update by creating the
// matching local directory (issue #105). Folders are never downloaded as
// files and carry no journal entry: only files are journaled, and the push
// side does not propagate directories on their own, so an empty remote
// folder becomes an empty local directory but not the reverse.
func (p *Puller) processDirChange(ch Change, start time.Time) error {
	p.log.Info("remote folder",
		"path", ch.Path,
		"op", "pull_mkdir",
		"origin", "remote",
	)

	if p.cfg.DryRun {
		p.logAudit(AuditEntry{
			Timestamp: start,
			Op:        "pull_mkdir",
			Path:      ch.Path,
			Origin:    "remote",
			DryRun:    true,
		})
		return nil
	}

	localPath := filepath.Join(p.cfg.LocalRoot, filepath.FromSlash(ch.Path))
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		p.emitPullFailure(ch.Path, err)
		return fmt.Errorf("creating directory %s: %w", ch.Path, err)
	}

	p.logAudit(AuditEntry{
		Timestamp:  start,
		Op:         "pull_mkdir",
		Path:       ch.Path,
		Origin:     "remote",
		DurationMs: p.clock().Sub(start).Milliseconds(),
	})
	return nil
}
