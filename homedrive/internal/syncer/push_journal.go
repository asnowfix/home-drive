package syncer

import (
	"context"
	"crypto/md5" //nolint:gosec // Drive reports MD5; used for equality, not security.
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// pushEntry builds the journal entry for a successful push of rel. When
// the upload returned the remote object it records the remote mtime, MD5
// and ID, and the local file's mtime, exactly like the bisync upload path
// does. Without that baseline the next pull of our own upload compares the
// remote change against an empty record and reports a false conflict
// (issue #106). obj may be nil (e.g. a delete), in which case only the
// sync bookkeeping is recorded.
func (s *Syncer) pushEntry(rel string, obj *RemoteObject) JournalEntry {
	e := JournalEntry{
		Path:         rel,
		LastSyncedAt: s.nowFn(),
		LastOrigin:   "local",
	}
	if obj == nil {
		return e
	}
	e.RemoteMtime = obj.ModTime
	e.RemoteMD5 = obj.MD5
	e.RemoteID = obj.RemoteID
	info, err := os.Stat(filepath.Join(s.cfg.LocalRoot, filepath.FromSlash(rel)))
	if err != nil {
		s.logger.Warn("stat after push failed, local mtime not journaled",
			"path", rel, "error", err.Error())
		return e
	}
	e.LocalMtime = info.ModTime()
	return e
}

// adoptIdenticalRemote handles a remote change for a path whose journal
// entry carries no baseline (no remote MD5 and no local mtime: entries
// written by pushes before #106 was fixed). If the local file is
// byte-identical to the remote object, nothing differs, so there is no
// conflict and no download: it journals the remote state and reports true.
// Different content, a missing local file or a journal that does have a
// baseline all return false and leave the normal detection untouched.
func (p *Puller) adoptIdenticalRemote(ctx context.Context, ch Change, journal JournalEntry) (bool, error) {
	if journal.RemoteMD5 != "" || !journal.LocalMtime.IsZero() || ch.Object.MD5 == "" {
		return false, nil
	}
	localPath := filepath.Join(p.cfg.LocalRoot, ch.Path)
	info, err := os.Stat(localPath)
	if err != nil || info.IsDir() {
		// Not comparable: fall back to the normal detection.
		return false, nil
	}
	sum, err := fileMD5(localPath)
	if err != nil {
		return false, fmt.Errorf("hashing local file %s: %w", ch.Path, err)
	}
	if !strings.EqualFold(sum, ch.Object.MD5) {
		return false, nil
	}
	p.log.Info("local file identical to remote, adopting remote state",
		"path", ch.Path, "op", "pull", "origin", "remote")
	if p.cfg.DryRun {
		return true, nil
	}
	entry := JournalEntry{
		Path:         ch.Path,
		LocalMtime:   info.ModTime(),
		RemoteMtime:  ch.Object.ModTime,
		RemoteMD5:    ch.Object.MD5,
		RemoteID:     ch.Object.RemoteID,
		LastSyncedAt: p.clock(),
		LastOrigin:   journal.LastOrigin,
	}
	if err := p.store.Put(ctx, entry); err != nil {
		return false, fmt.Errorf("updating journal for %s: %w", ch.Path, err)
	}
	return true, nil
}

// fileMD5 returns the lowercase hex MD5 of the file at path.
func fileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error is not actionable
	h := md5.New() //nolint:gosec // equality check against Drive's MD5
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
