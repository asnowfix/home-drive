package syncer

import "time"

// danglingKey identifies one sighting of a dangling Drive shortcut. The
// remote ID and mtime are part of the key so that a changed or repaired
// object is tried again (issue #82).
type danglingKey struct {
	path     string
	remoteID string
	modTime  int64 // UnixNano
}

func newDanglingKey(ch Change) danglingKey {
	k := danglingKey{path: ch.Path}
	if ch.Object != nil {
		k.remoteID = ch.Object.RemoteID
		k.modTime = ch.Object.ModTime.UnixNano()
	}
	return k
}

// knownDangling reports whether this exact object was already found to be
// a dangling shortcut by this process.
func (p *Puller) knownDangling(ch Change) bool {
	p.danglingMu.Lock()
	defer p.danglingMu.Unlock()
	_, ok := p.dangling[newDanglingKey(ch)]
	return ok
}

// skipDanglingShortcut handles the first sighting of a dangling shortcut:
// remember it, warn once, and emit one pull.failure event and one audit
// line. It is a skip, not a failure: the caller returns nil so the cycle
// does not fail.
//
// Deliberately NO journal entry: bisync would read a journal entry with no
// local file as a local deletion and could delete the remote shortcut.
func (p *Puller) skipDanglingShortcut(ch Change, err error, start time.Time) {
	p.danglingMu.Lock()
	p.dangling[newDanglingKey(ch)] = struct{}{}
	p.danglingMu.Unlock()

	p.log.Warn("skipping dangling Drive shortcut (target missing)",
		"path", ch.Path,
		"op", "pull",
		"origin", "remote",
		"error", err,
	)
	p.emitPullFailure(ch.Path, err)
	p.logAudit(AuditEntry{
		Timestamp:  start,
		Op:         "pull",
		Path:       ch.Path,
		Origin:     "remote",
		DurationMs: p.clock().Sub(start).Milliseconds(),
		Error:      err.Error(),
	})
}
