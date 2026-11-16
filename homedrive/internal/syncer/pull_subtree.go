// pull_subtree.go removes the local copies of everything that was synced
// beneath a Drive folder that has been deleted (issue #152). The journal is
// the durable record of what was synced, so the sweep needs neither
// per-child change entries from Drive nor the in-memory ID-to-path cache.
package syncer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// removeSyncedDescendants removes every journaled, locally unchanged file
// beneath relPath, then the empty directories left behind. Never deleted:
// a file without a journal entry or with an incomplete baseline (not known
// to be on Drive), an excluded path, a non-regular file, and a file whose
// mtime moved since its journal fingerprint (delete-vs-modify: kept, with
// a conflict event, its journal entry dropped so bisync pushes it back as
// a new file). With dryRun it only audits what it would remove.
func (p *Puller) removeSyncedDescendants(
	ctx context.Context, relPath, localPath string, start time.Time, dryRun bool,
) error {
	entries, err := p.store.ListOldSiblings(ctx, relPath+"/")
	if err != nil {
		return fmt.Errorf("listing journal under %s: %w", relPath, err)
	}
	for _, e := range entries {
		if pathfilter.Excluded(p.cfg.Exclude, e.Path) {
			continue
		}
		if err := p.removeSyncedFile(ctx, e, start, dryRun); err != nil {
			return err
		}
	}
	if !dryRun {
		removeEmptyDirs(localPath)
	}
	return nil
}

// removeSyncedFile applies the delete-or-keep decision to one journaled
// descendant.
func (p *Puller) removeSyncedFile(
	ctx context.Context, e JournalEntry, start time.Time, dryRun bool,
) error {
	localPath := filepath.Join(p.cfg.LocalRoot, filepath.FromSlash(e.Path))
	info, err := os.Lstat(localPath)
	switch {
	case os.IsNotExist(err):
		return p.dropStaleEntry(ctx, e.Path, dryRun)
	case err != nil:
		return fmt.Errorf("stat %s: %w", e.Path, err)
	case !info.Mode().IsRegular() || !hasBaseline(&e):
		return nil
	case !mtimeClose(info.ModTime(), e.LocalMtime):
		return p.keepModifiedDescendant(ctx, e, info.ModTime(), start, dryRun)
	}

	audit := AuditEntry{
		Timestamp: start, Op: "pull_delete", Path: e.Path,
		Origin: "remote", DryRun: dryRun,
	}
	if dryRun {
		p.logAudit(audit)
		return nil
	}
	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting local file %s: %w", e.Path, err)
	}
	if err := p.store.Delete(ctx, e.Path); err != nil {
		return fmt.Errorf("removing journal entry for %s: %w", e.Path, err)
	}
	p.log.Info("remote folder deleted: removed synced local file",
		"path", e.Path, "op", "pull_delete", "origin", "remote")
	audit.DurationMs = p.clock().Sub(start).Milliseconds()
	p.logAudit(audit)
	return nil
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
// deleted along with its folder, and reports the delete-vs-modify conflict.
func (p *Puller) keepModifiedDescendant(
	ctx context.Context, e JournalEntry, localMtime, start time.Time, dryRun bool,
) error {
	const resolution = "delete_vs_modify:keep_local"
	p.log.Warn("remote folder deleted but local file modified: keeping it",
		"path", e.Path, "op", "conflict", "origin", "remote",
		"resolution", resolution)
	if p.pub != nil {
		_ = p.pub.PublishJSON(p.pub.Topic("events", "conflict.detected"), map[string]any{
			"ts":          p.clock().UTC().Format(time.RFC3339),
			"type":        "conflict.detected",
			"path":        e.Path,
			"local_mtime": localMtime.UTC().Format(time.RFC3339),
			"resolution":  resolution,
		})
	}
	p.logAudit(AuditEntry{
		Timestamp: start, Op: "conflict", Path: e.Path, Origin: "remote",
		Resolution: resolution, DryRun: dryRun,
	})
	if dryRun {
		return nil
	}
	if err := p.store.Delete(ctx, e.Path); err != nil {
		return fmt.Errorf("removing journal entry for %s: %w", e.Path, err)
	}
	return nil
}

// removeEmptyDirs removes root and every directory beneath it that is
// empty, deepest first. os.Remove refuses a non-empty directory, so
// anything still holding a file stays.
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}
