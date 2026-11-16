// bisync_classify.go decides, for a file present on both sides, whether the
// bisync has nothing to do, a plain push, a plain pull, or a real conflict
// (PLAN.md §11.1: both sides changed since the last sync). Issue #146.
package syncer

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"time"
)

// mtimeTolerance is the clock/rounding slack when comparing mtimes, the
// same +-1s the puller (detectConflict) and loop prevention use.
const mtimeTolerance = time.Second

func mtimeClose(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= mtimeTolerance
}

// hasBaseline reports whether a journal entry records both what the local
// file looked like and what Drive held at the last sync. Without both,
// nothing can say which side moved, so such an entry (nil, or a
// conflict-loser entry) is treated as no baseline at all.
func hasBaseline(e *JournalEntry) bool {
	return e != nil && !e.LocalMtime.IsZero() &&
		(e.RemoteMD5 != "" || !e.RemoteMtime.IsZero())
}

// remoteChangedSince reports whether the remote copy moved since the
// journal entry: by MD5 when both sides have one (a touch-only mtime change
// is not a content change), else by mtime.
func remoteChangedSince(entry JournalEntry, remote RemoteObject) bool {
	if remote.MD5 != "" && entry.RemoteMD5 != "" {
		return remote.MD5 != entry.RemoteMD5
	}
	return !mtimeClose(remote.ModTime, entry.RemoteMtime)
}

// classify reports what to do with a file that exists on both sides.
// differs=false means the two sides are already in sync.
//
// With a usable journal baseline, local and remote are each compared with
// what the last sync recorded: only a change on both sides is a conflict.
// Remote change is judged by MD5 when both sides have one (a touch-only
// mtime change is not a content change), else by mtime.
//
// Without a baseline (no entry, or one lacking a local mtime or any remote
// state, such as a conflict-loser entry) nothing says who moved, so equal
// content (MD5, when the remote has one) or mtimes within +-1s mean in sync,
// and anything else is a conflict for newer-wins to settle.
func (b *Bisync) classify(
	path string, local LocalFileInfo, remote RemoteObject,
) (kind DiffKind, differs bool) {
	entry, err := b.journal.Get(path)
	if err != nil || !hasBaseline(entry) {
		if mtimeClose(local.ModTime, remote.ModTime) || b.sameContent(path, remote) {
			return 0, false
		}
		return DiffConflict, true
	}

	localChanged := !mtimeClose(local.ModTime, entry.LocalMtime)
	remoteChanged := remoteChangedSince(*entry, remote)

	switch {
	case localChanged && remoteChanged:
		if b.sameContent(path, remote) {
			return 0, false
		}
		return DiffConflict, true
	case localChanged:
		return DiffLocalChanged, true
	case remoteChanged:
		return DiffRemoteChanged, true
	}
	return 0, false
}

// sameContent reports whether the local file at path has the same MD5 as
// remote. False when the remote carries no MD5 or the file is unreadable.
func (b *Bisync) sameContent(path string, remote RemoteObject) bool {
	if remote.MD5 == "" {
		return false
	}
	f, err := os.Open(filepath.Join(b.cfg.LocalRoot, filepath.FromSlash(path)))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == remote.MD5
}

// oldNameTaken reports whether p is already used as a file name on either
// side or in the journal, so a conflict loser must not be renamed onto it.
// The journal alone (PLAN.md §11.2) misses a .old.<N> that exists on disk or
// on Drive without an entry (another path pulled it, or its entry was
// evicted); renaming onto it would silently overwrite that loser.
func (b *Bisync) oldNameTaken(p string) bool {
	if b.journal.Exists(p) {
		return true
	}
	_, ok := b.snapshot[p]
	return ok
}
