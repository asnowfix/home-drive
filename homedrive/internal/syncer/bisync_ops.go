// bisync_ops.go contains the diff computation, sync operations, and
// conflict resolution logic used by the bisync safety net.
package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
)

// ---------------------------------------------------------------------------
// Diff logic
// ---------------------------------------------------------------------------

// diff computes the full set of differences between local and remote.
func (b *Bisync) diff(ctx context.Context) ([]FileDiff, error) {
	localFiles, err := b.walkLocal(ctx)
	if err != nil {
		return nil, fmt.Errorf("walk local: %w", err)
	}

	b.recordLocalTree(localFiles)

	remoteFiles, err := b.listRemote(ctx)
	if err != nil {
		return nil, fmt.Errorf("list remote: %w", err)
	}

	remoteMap := make(map[string]RemoteObject, len(remoteFiles))
	for _, r := range remoteFiles {
		remoteMap[r.Path] = r
	}

	localMap := make(map[string]LocalFileInfo, len(localFiles))
	for _, l := range localFiles {
		localMap[l.Path] = l
	}

	b.localCount, b.remoteCount = len(localMap), len(remoteMap)
	b.snapshot = make(map[string]struct{}, len(localMap)+len(remoteMap))
	for p := range localMap {
		b.snapshot[p] = struct{}{}
	}
	for p := range remoteMap {
		b.snapshot[p] = struct{}{}
	}

	var diffs []FileDiff
	diffs = b.diffLocalAgainstRemote(localMap, remoteMap, diffs)
	diffs = b.diffRemoteAgainstLocal(localMap, remoteMap, diffs)
	return diffs, nil
}

// recordLocalTree reports the local file count and total size seen by the
// walk, for the status "local" object.
func (b *Bisync) recordLocalTree(files []LocalFileInfo) {
	if b.rec == nil {
		return
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	b.rec.RecordLocalTree(len(files), total, b.clock.Now())
}

// diffLocalAgainstRemote finds files that exist locally but not
// remotely, or that exist on both sides with divergence.
func (b *Bisync) diffLocalAgainstRemote(
	localMap map[string]LocalFileInfo,
	remoteMap map[string]RemoteObject,
	diffs []FileDiff,
) []FileDiff {
	for path, local := range localMap {
		remote, exists := remoteMap[path]
		if !exists {
			if _, skip := b.unrepresentable[path]; skip {
				continue
			}
			diffs = append(diffs, b.localOnlyDiff(path, local))
			continue
		}
		if kind, differs := b.classify(path, local, remote); differs {
			remoteCopy := remote
			diffs = append(diffs, FileDiff{
				Path:       path,
				Kind:       kind,
				LocalInfo:  &local,
				RemoteInfo: &remoteCopy,
			})
		}
	}
	return diffs
}

// diffRemoteAgainstLocal finds files that exist remotely but not locally.
func (b *Bisync) diffRemoteAgainstLocal(
	localMap map[string]LocalFileInfo,
	remoteMap map[string]RemoteObject,
	diffs []FileDiff,
) []FileDiff {
	for path, remote := range remoteMap {
		if _, exists := localMap[path]; !exists {
			if _, link := b.linkPaths[path]; link {
				// A skipped local symlink sits here (issue #191): it is not
				// "locally deleted", so Drive's copy (pushed as a file by an
				// earlier version) is neither deleted nor overwritten.
				continue
			}
			diffs = append(diffs, b.remoteOnlyDiff(path, remote))
		}
	}
	return diffs
}

// walkLocal returns all regular files under the local root, with
// paths relative to LocalRoot.
func (b *Bisync) walkLocal(ctx context.Context) ([]LocalFileInfo, error) {
	var files []LocalFileInfo
	root := b.cfg.LocalRoot
	found := make(map[string]skippedLink)

	err := filepath.WalkDir(root, func(
		path string, d os.DirEntry, err error,
	) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("rel path %s: %w", path, err)
		}
		relPath = filepath.ToSlash(relPath)
		if d.IsDir() {
			if relPath != "." && pathfilter.Excluded(b.cfg.Exclude, relPath) {
				return filepath.SkipDir
			}
			return nil
		}
		if pathfilter.Excluded(b.cfg.Exclude, relPath) {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Never a file to sync (issue #191): reported, not listed.
			if isLink, reason, target := classifyLink(root, path); isLink {
				found[relPath] = skippedLink{reason: reason, target: target}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		files = append(files, LocalFileInfo{
			Path:    relPath,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk local root %s: %w", root, err)
	}
	b.links.Reconcile(found)
	b.linkPaths = make(map[string]struct{}, len(found))
	for p := range found {
		b.linkPaths[p] = struct{}{}
	}
	return files, nil
}

// listRemote returns all non-excluded files from the remote via
// RemoteFS.List (recursive, unfiltered); watcher.exclude is applied here.
func (b *Bisync) listRemote(ctx context.Context) ([]RemoteObject, error) {
	objects, err := b.remote.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("remote list: %w", err)
	}
	kept := objects[:0:0]
	b.unrepresentable = make(map[string]struct{})
	for _, o := range objects {
		if !rcloneclient.ValidRemotePath(o.Path) {
			// Never pulled (it could address a path outside local_root),
			// but remembered: a local file at this exact path still
			// exists on Drive, so its absence from the listing must not
			// read as a remote deletion nor trigger a push (issue #161).
			b.unrepresentable[o.Path] = struct{}{}
			b.log.Warn("bisync skipping remote entry with an unsafe path",
				"op", "list", "origin", "remote", "path", o.Path)
			continue
		}
		if !pathfilter.Excluded(b.cfg.Exclude, o.Path) {
			kept = append(kept, o)
		}
	}
	return kept, nil
}

// ---------------------------------------------------------------------------
// Sync operations
// ---------------------------------------------------------------------------

// syncLocalToRemote pushes a local-only file to the remote.
func (b *Bisync) syncLocalToRemote(ctx context.Context, d FileDiff) error {
	b.log.Info("bisync pushing file",
		"path", d.Path,
		"op", "push",
		"origin", "local",
		"dry_run", b.cfg.DryRun,
	)

	if b.cfg.DryRun {
		b.auditFile("push", d.Path, "", "")
		return nil
	}

	srcPath := filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(d.Path))
	dstDir := filepath.Dir(d.Path)
	if dstDir == "." {
		dstDir = ""
	}

	obj, err := b.remote.CopyFile(ctx, srcPath, dstDir)
	if err != nil {
		return fmt.Errorf("copy %s to remote: %w", d.Path, err)
	}
	b.auditFile("push", d.Path, "", "")

	return b.journal.Put(JournalEntry{
		Path:         d.Path,
		LocalMtime:   d.LocalInfo.ModTime,
		RemoteMtime:  obj.ModTime,
		RemoteMD5:    obj.MD5,
		RemoteID:     obj.RemoteID,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "local",
	})
}

// syncRemoteToLocal downloads a remote-only file to the local filesystem.
func (b *Bisync) syncRemoteToLocal(ctx context.Context, d FileDiff) error {
	b.log.Info("bisync pulling file",
		"path", d.Path,
		"op", "pull",
		"origin", "remote",
		"dry_run", b.cfg.DryRun,
	)

	if b.cfg.DryRun {
		b.auditFile("pull", d.Path, "", "")
		return nil
	}

	localPath, err := localPathFor(b.cfg.LocalRoot, d.Path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", d.Path, err)
	}
	noteLocalWrite(b.cfg.Remover, filepath.Dir(localPath))

	if err := b.remote.DownloadFile(ctx, d.Path, localPath); err != nil {
		return fmt.Errorf("download %s: %w", d.Path, err)
	}
	b.auditFile("pull", d.Path, "", "")

	return b.journal.Put(JournalEntry{
		Path:         d.Path,
		LocalMtime:   d.RemoteInfo.ModTime,
		RemoteMtime:  d.RemoteInfo.ModTime,
		RemoteMD5:    d.RemoteInfo.MD5,
		RemoteID:     d.RemoteInfo.RemoteID,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "remote",
	})
}

// ---------------------------------------------------------------------------
// Conflict resolution (newer-wins per PLAN.md section 11.2)
// ---------------------------------------------------------------------------

// resolveConflict applies newer-wins conflict resolution.
func (b *Bisync) resolveConflict(ctx context.Context, d FileDiff) error {
	localMtime := d.LocalInfo.ModTime
	remoteMtime := d.RemoteInfo.ModTime

	b.log.Info("bisync conflict detected",
		"path", d.Path,
		"op", "conflict",
		"local_mtime", localMtime,
		"remote_mtime", remoteMtime,
		"dry_run", b.cfg.DryRun,
	)

	if b.cfg.DryRun {
		b.auditFile("conflict", d.Path, "", "")
		return nil
	}

	if localMtime.After(remoteMtime) {
		return b.resolveLocalWins(ctx, d)
	}
	if remoteMtime.After(localMtime) {
		return b.resolveRemoteWins(ctx, d)
	}
	// Equal mtime, different content: local wins by default.
	b.log.Warn("bisync conflict with equal mtimes",
		"path", d.Path,
		"op", "conflict",
	)
	return b.resolveLocalWins(ctx, d)
}

// resolveLocalWins uploads local version, renames remote to .old.<N>.
func (b *Bisync) resolveLocalWins(ctx context.Context, d FileDiff) error {
	base, oldN := oldsuffix.NextOldN(b.cfg.Matcher, d.Path, b.oldNameTaken)
	oldPath := b.cfg.Matcher.Format(base, oldN)

	b.log.Info("bisync conflict resolved: local wins",
		"path", d.Path,
		"op", "conflict",
		"resolution", "newer_wins:local",
		"kept_old_as", oldPath,
	)

	if err := b.remote.MoveFile(ctx, d.Path, oldPath); err != nil {
		return fmt.Errorf("rename remote %s to %s: %w", d.Path, oldPath, err)
	}
	b.auditFile("conflict", d.Path, "newer_wins:local", oldPath)

	srcPath := filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(d.Path))
	dstDir := filepath.Dir(d.Path)
	if dstDir == "." {
		dstDir = ""
	}
	obj, err := b.remote.CopyFile(ctx, srcPath, dstDir)
	if err != nil {
		return fmt.Errorf("upload %s after conflict: %w", d.Path, err)
	}

	if err := b.journal.Put(JournalEntry{
		Path:         d.Path,
		LocalMtime:   d.LocalInfo.ModTime,
		RemoteMtime:  obj.ModTime,
		RemoteMD5:    obj.MD5,
		RemoteID:     obj.RemoteID,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "local",
	}); err != nil {
		return fmt.Errorf("journal put %s: %w", d.Path, err)
	}

	if err := b.journal.Put(JournalEntry{
		Path:         oldPath,
		RemoteMtime:  d.RemoteInfo.ModTime,
		RemoteMD5:    d.RemoteInfo.MD5,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "remote",
	}); err != nil {
		return fmt.Errorf("journal put %s: %w", oldPath, err)
	}

	b.markForPrune(base)
	return nil
}

// resolveRemoteWins downloads remote version, renames local to .old.<N>.
func (b *Bisync) resolveRemoteWins(ctx context.Context, d FileDiff) error {
	base, oldN := oldsuffix.NextOldN(b.cfg.Matcher, d.Path, b.oldNameTaken)
	oldPath := b.cfg.Matcher.Format(base, oldN)

	b.log.Info("bisync conflict resolved: remote wins",
		"path", d.Path,
		"op", "conflict",
		"resolution", "newer_wins:remote",
		"kept_old_as", oldPath,
	)

	localPath, err := localPathFor(b.cfg.LocalRoot, d.Path)
	if err != nil {
		return err
	}
	localOldPath, err := localPathFor(b.cfg.LocalRoot, oldPath)
	if err != nil {
		return err
	}

	if err := os.Rename(localPath, localOldPath); err != nil {
		return fmt.Errorf("rename local %s to %s: %w", d.Path, oldPath, err)
	}
	b.auditFile("conflict", d.Path, "newer_wins:remote", oldPath)

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", d.Path, err)
	}
	noteLocalWrite(b.cfg.Remover, filepath.Dir(localPath))
	if err := b.remote.DownloadFile(ctx, d.Path, localPath); err != nil {
		return fmt.Errorf("download %s: %w", d.Path, err)
	}

	if err := b.journal.Put(JournalEntry{
		Path:         d.Path,
		LocalMtime:   d.RemoteInfo.ModTime,
		RemoteMtime:  d.RemoteInfo.ModTime,
		RemoteMD5:    d.RemoteInfo.MD5,
		RemoteID:     d.RemoteInfo.RemoteID,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "remote",
	}); err != nil {
		return fmt.Errorf("journal put %s: %w", d.Path, err)
	}

	if err := b.journal.Put(JournalEntry{
		Path:         oldPath,
		LocalMtime:   d.LocalInfo.ModTime,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "local",
	}); err != nil {
		return fmt.Errorf("journal put %s: %w", oldPath, err)
	}

	b.markForPrune(base)
	return nil
}
