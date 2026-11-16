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
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// deleteConflictResolution is the audit/event resolution for a
// delete-vs-modify conflict: the modified copy is kept.
const deleteConflictResolution = "delete_vs_modify:keep_modified"

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

// deleteLocal removes a local file whose Drive copy was deleted. Before
// deleting it confirms with a direct lookup that Drive really has no such
// object: the recursive listing cannot return everything (filtered or
// dangling objects), and absence from it alone must never delete data. Any
// lookup outcome other than a definite not-found skips the deletion.
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

	b.log.Info("bisync deleting local file removed on Drive",
		"path", d.Path, "op", "pull_delete", "origin", "remote",
		"dry_run", b.cfg.DryRun)
	if b.cfg.DryRun {
		b.auditFile("pull_delete", d.Path, "", "")
		return true, nil
	}
	localPath := filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(d.Path))
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

// deleteRemote deletes, on Drive, a file that was deleted locally. Before
// deleting it confirms the local file is really gone.
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
	if b.mqtt == nil {
		return
	}
	_ = b.mqtt.PublishJSON(b.mqtt.Topic("events", "conflict.detected"), map[string]any{
		"ts":         b.clock.Now().UTC().Format(time.RFC3339),
		"type":       "conflict.detected",
		"path":       d.Path,
		"resolution": deleteConflictResolution,
	})
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
