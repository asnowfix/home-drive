// bisync_ops.go contains the diff computation, sync operations, and
// conflict resolution logic used by the bisync safety net.
package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	var diffs []FileDiff
	diffs = b.diffLocalAgainstRemote(localMap, remoteMap, diffs)
	diffs = b.diffRemoteAgainstLocal(localMap, remoteMap, diffs)
	return diffs, nil
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
			diffs = append(diffs, FileDiff{
				Path:      path,
				Kind:      DiffLocalOnly,
				LocalInfo: &local,
			})
			continue
		}
		if b.hasDivergence(path, local, remote) {
			remoteCopy := remote
			diffs = append(diffs, FileDiff{
				Path:       path,
				Kind:       DiffConflict,
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
			remoteCopy := remote
			diffs = append(diffs, FileDiff{
				Path:       path,
				Kind:       DiffRemoteOnly,
				RemoteInfo: &remoteCopy,
			})
		}
	}
	return diffs
}

// hasDivergence checks whether the local and remote versions of a file
// have diverged from what the journal last recorded.
func (b *Bisync) hasDivergence(
	path string,
	local LocalFileInfo,
	remote RemoteObject,
) bool {
	entry, err := b.journal.Get(path)
	if err != nil || entry == nil {
		// No journal entry: compare directly.
		return !local.ModTime.Equal(remote.ModTime)
	}
	localChanged := !local.ModTime.Equal(entry.LocalMtime)
	remoteChanged := remote.MD5 != entry.RemoteMD5 ||
		!remote.ModTime.Equal(entry.RemoteMtime)
	return localChanged || remoteChanged
}

// walkLocal returns all regular files under the local root, with
// paths relative to LocalRoot.
func (b *Bisync) walkLocal(ctx context.Context) ([]LocalFileInfo, error) {
	var files []LocalFileInfo
	root := b.cfg.LocalRoot

	err := filepath.WalkDir(root, func(
		path string, d os.DirEntry, err error,
	) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("rel path %s: %w", path, err)
		}
		relPath = filepath.ToSlash(relPath)
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
	return files, nil
}

// listRemote returns all files from the remote via RemoteFS.List.
func (b *Bisync) listRemote(ctx context.Context) ([]RemoteObject, error) {
	objects, err := b.remote.List(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("remote list: %w", err)
	}
	return objects, nil
}

// ---------------------------------------------------------------------------
// Sync operations
// ---------------------------------------------------------------------------

// syncLocalToRemote pushes a local-only file to the remote.
func (b *Bisync) syncLocalToRemote(ctx context.Context, d FileDiff) error {
	b.log.Info("bisync pushing local-only file",
		"path", d.Path,
		"op", "push",
		"origin", "local",
		"dry_run", b.cfg.DryRun,
	)

	if b.cfg.DryRun {
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

	return b.journal.Put(JournalEntry{
		Path:         d.Path,
		LocalMtime:   d.LocalInfo.ModTime,
		RemoteMtime:  obj.ModTime,
		RemoteMD5:    obj.MD5,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "local",
	})
}

// syncRemoteToLocal downloads a remote-only file to the local filesystem.
func (b *Bisync) syncRemoteToLocal(ctx context.Context, d FileDiff) error {
	b.log.Info("bisync pulling remote-only file",
		"path", d.Path,
		"op", "pull",
		"origin", "remote",
		"dry_run", b.cfg.DryRun,
	)

	if b.cfg.DryRun {
		return nil
	}

	localPath := filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(d.Path))

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", d.Path, err)
	}

	// In the real implementation, rclone downloads the file.
	// For now we create a placeholder so the journal is consistent.
	if err := os.WriteFile(localPath, []byte{}, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", d.Path, err)
	}

	return b.journal.Put(JournalEntry{
		Path:         d.Path,
		LocalMtime:   d.RemoteInfo.ModTime,
		RemoteMtime:  d.RemoteInfo.ModTime,
		RemoteMD5:    d.RemoteInfo.MD5,
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
	oldN := b.nextOldN(d.Path)
	oldPath := fmt.Sprintf("%s.old.%d", d.Path, oldN)

	b.log.Info("bisync conflict resolved: local wins",
		"path", d.Path,
		"op", "conflict",
		"resolution", "newer_wins:local",
		"kept_old_as", oldPath,
	)

	if err := b.remote.MoveFile(ctx, d.Path, oldPath); err != nil {
		return fmt.Errorf("rename remote %s to %s: %w", d.Path, oldPath, err)
	}

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
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "local",
	}); err != nil {
		return fmt.Errorf("journal put %s: %w", d.Path, err)
	}

	return b.journal.Put(JournalEntry{
		Path:         oldPath,
		RemoteMtime:  d.RemoteInfo.ModTime,
		RemoteMD5:    d.RemoteInfo.MD5,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "remote",
	})
}

// resolveRemoteWins downloads remote version, renames local to .old.<N>.
func (b *Bisync) resolveRemoteWins(ctx context.Context, d FileDiff) error {
	oldN := b.nextOldN(d.Path)
	oldPath := fmt.Sprintf("%s.old.%d", d.Path, oldN)

	b.log.Info("bisync conflict resolved: remote wins",
		"path", d.Path,
		"op", "conflict",
		"resolution", "newer_wins:remote",
		"kept_old_as", oldPath,
	)

	localPath := filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(d.Path))
	localOldPath := filepath.Join(
		b.cfg.LocalRoot,
		filepath.FromSlash(oldPath),
	)

	if err := os.Rename(localPath, localOldPath); err != nil {
		return fmt.Errorf("rename local %s to %s: %w", d.Path, oldPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", d.Path, err)
	}
	// In production, rclone downloads the file here.
	if err := os.WriteFile(localPath, []byte{}, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", d.Path, err)
	}

	if err := b.journal.Put(JournalEntry{
		Path:         d.Path,
		LocalMtime:   d.RemoteInfo.ModTime,
		RemoteMtime:  d.RemoteInfo.ModTime,
		RemoteMD5:    d.RemoteInfo.MD5,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "remote",
	}); err != nil {
		return fmt.Errorf("journal put %s: %w", d.Path, err)
	}

	return b.journal.Put(JournalEntry{
		Path:         oldPath,
		LocalMtime:   d.LocalInfo.ModTime,
		LastSyncedAt: b.clock.Now(),
		LastOrigin:   "local",
	})
}

// nextOldN computes the smallest positive integer N such that
// <path>.old.<N> does not exist in the journal.
func (b *Bisync) nextOldN(path string) int {
	n := 1
	for {
		candidate := fmt.Sprintf("%s.old.%d", path, n)
		if !b.journal.Exists(candidate) {
			return n
		}
		n++
	}
}
