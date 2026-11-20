// bisync_delete.go lets bisync tell a deletion from a creation using the
// journal (issue #152). A file present on one side only used to be pushed or
// pulled unconditionally, which undid every deletion the push or pull path
// had missed. The journal records what the last sync saw on both sides:
//
//   - journaled, present locally, absent on Drive, local unchanged =>
//     deleted on Drive => delete locally (audit op "pull_delete").
//   - journaled, present on Drive, absent locally, Drive unchanged =>
//     deleted locally => delete on Drive, i.e. its trash (audit op
//     "push_delete").
//   - the surviving side changed since the journal fingerprint =>
//     delete-vs-modify: the modified copy wins and is propagated as before,
//     with a conflict event.
//   - no usable journal baseline => unchanged behaviour (push / pull).
package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// localOnlyDiff classifies a file present locally and absent from the
// remote listing.
func (b *Bisync) localOnlyDiff(path string, local LocalFileInfo) FileDiff {
	d := FileDiff{Path: path, Kind: DiffLocalOnly, LocalInfo: &local}
	entry, err := b.journal.Get(path)
	if err != nil || !hasBaseline(entry) {
		return d
	}
	if mtimeClose(local.ModTime, entry.LocalMtime) {
		d.Kind = DiffRemoteDeleted
	} else {
		d.DeleteConflict = true
	}
	return d
}

// remoteOnlyDiff classifies a file present on the remote and absent from
// the local walk.
func (b *Bisync) remoteOnlyDiff(path string, remote RemoteObject) FileDiff {
	d := FileDiff{Path: path, Kind: DiffRemoteOnly, RemoteInfo: &remote}
	entry, err := b.journal.Get(path)
	if err != nil || !hasBaseline(entry) {
		return d
	}
	if remoteChangedSince(*entry, remote) {
		d.DeleteConflict = true
	} else {
		d.Kind = DiffLocalDeleted
	}
	return d
}

// plannedDeletions returns the deletions diff() decided on, computed from
// the complete diff before syncDiffs executes any of it, so the set can be
// counted (and, by a future guard, vetoed) as a whole.
func plannedDeletions(diffs []FileDiff) []FileDiff {
	var out []FileDiff
	for _, d := range diffs {
		if d.Kind == DiffRemoteDeleted || d.Kind == DiffLocalDeleted {
			out = append(out, d)
		}
	}
	return out
}

// deleteLocal removes a local file whose Drive copy was deleted. The diff
// was computed before anything ran, so everything it decided on is
// re-verified first, and any doubt keeps the file (the next pass re-judges):
//   - a direct lookup must say not-found: the recursive listing cannot
//     return everything (filtered or dangling objects), and absence from it
//     alone must never delete data;
//   - the local file must still be a regular file whose mtime matches the
//     journal, i.e. not edited since the diff.
func (b *Bisync) deleteLocal(ctx context.Context, d FileDiff) (bool, error) {
	if pathfilter.Excluded(b.cfg.Exclude, d.Path) {
		return false, nil
	}
	if _, err := b.remote.Stat(ctx, d.Path); err == nil {
		b.log.Warn("bisync: file listed as absent on Drive but found by lookup, keeping local copy",
			"path", d.Path, "op", "pull_delete")
		return false, nil
	} else if !errors.Is(err, ErrRemoteNotFound) {
		return false, fmt.Errorf("confirm %s absent on remote: %w", d.Path, err)
	}
	localPath, err := localPathFor(b.cfg.LocalRoot, d.Path)
	if err != nil {
		return false, err
	}
	if !b.localUnchanged(d.Path, localPath) {
		b.log.Warn("bisync: local file changed since the diff, keeping it",
			"path", d.Path, "op", "pull_delete")
		return false, nil
	}

	b.log.Info("bisync deleting local file removed on Drive",
		"path", d.Path, "op", "pull_delete", "origin", "remote",
		"dry_run", b.cfg.DryRun)
	if b.cfg.DryRun {
		b.auditFile("pull_delete", d.Path, "", "")
		return true, nil
	}
	if err := removeLocalPath(localPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("delete local %s: %w", d.Path, err)
	}
	b.removeEmptyParents(localPath)
	b.auditFile("pull_delete", d.Path, "", "")
	if err := b.journal.Delete(d.Path); err != nil {
		return false, fmt.Errorf("journal delete %s: %w", d.Path, err)
	}
	return true, nil
}

// localUnchanged reports whether localPath is a regular file whose mtime
// still matches the journal baseline for path.
func (b *Bisync) localUnchanged(path, localPath string) bool {
	info, err := os.Lstat(localPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	entry, err := b.journal.Get(path)
	if err != nil || !hasBaseline(entry) {
		return false
	}
	return mtimeClose(info.ModTime(), entry.LocalMtime)
}

// deleteRemote deletes, on Drive, a file that was deleted locally. Before
// trashing it re-verifies, like deleteLocal: the local file must really be
// gone, and Drive's copy must still match the journal (an edit made on Drive
// after the listing is kept and pulled back as a delete-vs-modify conflict).
func (b *Bisync) deleteRemote(ctx context.Context, d FileDiff) (bool, error) {
	if pathfilter.Excluded(b.cfg.Exclude, d.Path) {
		return false, nil
	}
	localPath := filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(d.Path))
	if _, err := os.Lstat(localPath); err == nil {
		b.log.Warn("bisync: file absent from local walk but present on disk, keeping remote copy",
			"path", d.Path, "op", "push_delete")
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("confirm %s absent locally: %w", d.Path, err)
	}
	obj, err := b.remote.Stat(ctx, d.Path)
	switch {
	case errors.Is(err, ErrRemoteNotFound):
		if b.cfg.DryRun {
			return false, nil
		}
		return false, b.journal.Delete(d.Path) // already gone on Drive
	case err != nil:
		return false, fmt.Errorf("re-check %s on remote: %w", d.Path, err)
	}
	if entry, jerr := b.journal.Get(d.Path); jerr != nil || !hasBaseline(entry) ||
		remoteChangedSince(*entry, obj) {
		pull := FileDiff{Path: d.Path, Kind: DiffRemoteOnly, RemoteInfo: &obj, DeleteConflict: true}
		b.reportDeleteConflict(pull)
		return false, b.syncRemoteToLocal(ctx, pull)
	}

	b.log.Info("bisync deleting Drive file removed locally",
		"path", d.Path, "op", "push_delete", "origin", "local",
		"dry_run", b.cfg.DryRun)
	if b.cfg.DryRun {
		b.auditFile("push_delete", d.Path, "", "")
		return true, nil
	}
	if err := b.remote.DeleteFile(ctx, d.Path); err != nil &&
		!errors.Is(err, ErrRemoteNotFound) {
		return false, fmt.Errorf("delete remote %s: %w", d.Path, err)
	}
	b.auditFile("push_delete", d.Path, "", "")
	if err := b.journal.Delete(d.Path); err != nil {
		return false, fmt.Errorf("journal delete %s: %w", d.Path, err)
	}
	return true, nil
}

// reportDeleteConflict records a delete-vs-modify conflict: one side
// deleted a file the other side changed. The modified copy is propagated
// by the push or pull that follows, which also re-baselines the journal.
func (b *Bisync) reportDeleteConflict(d FileDiff) {
	b.log.Warn("bisync delete-vs-modify conflict: keeping modified copy",
		"path", d.Path, "op", "conflict", "resolution", deleteConflictResolution,
		"dry_run", b.cfg.DryRun)
	b.auditFile("conflict", d.Path, deleteConflictResolution, "")
	publishDeleteConflict(b.mqtt, b.log, b.clock.Now(), d.Path, b.cfg.DryRun)
}

// removeEmptyParents removes now-empty directories above localPath, up to
// but excluding LocalRoot. os.Remove refuses a non-empty directory.
func (b *Bisync) removeEmptyParents(localPath string) {
	root := filepath.Clean(b.cfg.LocalRoot)
	for dir := filepath.Dir(localPath); dir != root && len(dir) > len(root); dir = filepath.Dir(dir) {
		if removeLocalPath(dir) != nil {
			return
		}
	}
}

// syncDelete propagates one deletion and reports whether it was applied
// (a skipped or failed deletion is logged and leaves everything in place).
func (b *Bisync) syncDelete(ctx context.Context, d FileDiff) bool {
	var (
		applied bool
		err     error
	)
	if d.Kind == DiffRemoteDeleted {
		applied, err = b.deleteLocal(ctx, d)
	} else {
		applied, err = b.deleteRemote(ctx, d)
	}
	if err != nil {
		b.log.Error("bisync delete failed", "path", d.Path, "error", err)
		return false
	}
	return applied
}

// logPlannedDeletions logs how many deletions the diff holds, before any
// of them executes.
func (b *Bisync) logPlannedDeletions(diffs []FileDiff) {
	if planned := plannedDeletions(diffs); len(planned) > 0 {
		b.log.Info("bisync planned deletions", "count", len(planned),
			"of_diffs", len(diffs), "dry_run", b.cfg.DryRun)
	}
}
