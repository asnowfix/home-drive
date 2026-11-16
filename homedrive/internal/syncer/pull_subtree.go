// pull_subtree.go removes the local copies of everything that was synced
// beneath a Drive folder that has been deleted (issue #152). The journal is
// the durable record of what was synced, so the sweep needs neither
// per-child change entries from Drive nor the in-memory ID-to-path cache.
package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// removeSyncedDescendants removes every journaled, locally unchanged file
// beneath relPath, then the directories that held them if they are empty.
// Never deleted: a file without a journal entry or with an incomplete
// baseline (not known to be on Drive), an excluded path, a non-regular file,
// and a file whose mtime moved since its journal fingerprint
// (delete-vs-modify: kept, with a conflict event; its journal entry is
// dropped so that bisync pushes it back as a new file). With dryRun it only
// audits what it would remove.
func (p *Puller) removeSyncedDescendants(
	ctx context.Context, relPath, localPath string, start time.Time, dryRun bool,
) error {
	entries, err := p.store.ListOldSiblings(ctx, relPath+"/")
	if err != nil {
		return fmt.Errorf("listing journal under %s: %w", relPath, err)
	}
	touched := make(map[string]struct{})
	for _, e := range entries {
		if pathfilter.Excluded(p.cfg.Exclude, e.Path) {
			continue
		}
		gone, err := p.removeSyncedFile(ctx, e, start, dryRun)
		if err != nil {
			return err
		}
		if gone {
			dir := filepath.Dir(filepath.Join(p.cfg.LocalRoot, filepath.FromSlash(e.Path)))
			touched[dir] = struct{}{}
		}
	}
	if !dryRun {
		p.pruneEmptyDirs(touched, localPath)
	}
	return nil
}

// removeSyncedFile applies the delete-or-keep decision to one journaled
// descendant. gone reports that the local file no longer exists because of
// this call (removed, or already missing with its stale entry dropped).
func (p *Puller) removeSyncedFile(
	ctx context.Context, e JournalEntry, start time.Time, dryRun bool,
) (gone bool, err error) {
	localPath := filepath.Join(p.cfg.LocalRoot, filepath.FromSlash(e.Path))
	info, err := os.Lstat(localPath)
	switch {
	case os.IsNotExist(err):
		return !dryRun, p.dropStaleEntry(ctx, e.Path, dryRun)
	case err != nil:
		return false, fmt.Errorf("stat %s: %w", e.Path, err)
	case !info.Mode().IsRegular() || !hasBaseline(&e):
		return false, nil
	case !mtimeClose(info.ModTime(), e.LocalMtime):
		return false, p.keepModifiedDescendant(ctx, e, info.ModTime(), start, dryRun)
	}

	audit := AuditEntry{
		Timestamp: start, Op: "pull_delete", Path: e.Path,
		Origin: "remote", DryRun: dryRun,
	}
	if dryRun {
		p.logAudit(audit)
		return false, nil
	}
	if err := removeLocalPath(localPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("deleting local file %s: %w", e.Path, err)
	}
	if err := p.store.Delete(ctx, e.Path); err != nil {
		return true, fmt.Errorf("removing journal entry for %s: %w", e.Path, err)
	}
	p.log.Info("remote folder deleted: removed synced local file",
		"path", e.Path, "op", "pull_delete", "origin", "remote")
	audit.DurationMs = p.clock().Sub(start).Milliseconds()
	p.logAudit(audit)
	return true, nil
}

// dropStaleEntry forgets a journal entry whose local file is already gone.
func (p *Puller) dropStaleEntry(ctx context.Context, path string, dryRun bool) error {
	if dryRun {
		return nil
	}
	if err := p.store.Delete(ctx, path); err != nil {
		return fmt.Errorf("removing journal entry for %s: %w", path, err)
	}
	return nil
}

// keepModifiedDescendant keeps a locally modified file whose Drive copy was
// deleted, and reports the delete-vs-modify conflict. Its journal entry is
// dropped (not re-baselined, unlike bisync which pushes in the same step):
// the pull path has no upload to baseline against, and an entry-less local
// file is exactly what the next bisync pass pushes back to Drive.
func (p *Puller) keepModifiedDescendant(
	ctx context.Context, e JournalEntry, localMtime, start time.Time, dryRun bool,
) error {
	p.log.Warn("remote file deleted but local file modified: keeping it",
		"path", e.Path, "op", "conflict", "origin", "remote",
		"resolution", deleteConflictResolution, "dry_run", dryRun)
	publishDeleteConflict(p.pub, p.log, p.clock(), e.Path, dryRun)
	p.logAudit(AuditEntry{
		Timestamp: start, Op: "conflict", Path: e.Path, Origin: "remote",
		Resolution: deleteConflictResolution, DryRun: dryRun,
	})
	if dryRun {
		return nil
	}
	if err := p.store.Delete(ctx, e.Path); err != nil {
		return fmt.Errorf("removing journal entry for %s: %w", e.Path, err)
	}
	return nil
}

// pruneEmptyDirs removes, from each directory in dirs upward to and
// including folder, the directories that are empty. Only directories that
// held a file removed by this operation are visited, and excluded ones are
// skipped, so an empty directory the user created (or one under an excluded
// tree such as .git) is never touched. os.Remove refuses a non-empty
// directory, which ends the climb.
func (p *Puller) pruneEmptyDirs(dirs map[string]struct{}, folder string) {
	for dir := range dirs {
		for d := dir; d == folder || strings.HasPrefix(d, folder+string(filepath.Separator)); d = filepath.Dir(d) {
			rel, err := filepath.Rel(p.cfg.LocalRoot, d)
			if err != nil || pathfilter.Excluded(p.cfg.Exclude, filepath.ToSlash(rel)) {
				break
			}
			if err := removeLocalPath(d); err != nil && !os.IsNotExist(err) {
				break
			}
		}
	}
}
