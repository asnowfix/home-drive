package syncer

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultDeleteGuardTTL bounds how long a local deletion made by homedrive
// stays "expected". The watcher debounce plus queueing behind a busy worker
// pool delayed the echoes seen on the NAS by 2-3 s; 30 s leaves a wide margin
// without keeping a mark around long enough to matter.
const DefaultDeleteGuardTTL = 30 * time.Second

// DeleteGuard is the expected-deletion set (issue #154). Loop prevention
// (PLAN.md §7.3) matches writes against the journal mtime, but a deletion
// leaves nothing to match, so the watcher's remove event for a directory
// homedrive itself removed (puller, bisync, emptied parent) would be pushed
// back to Drive.
//
// It is a LocalRemover decorator and marks only directories: the Trasher
// moves files into the freedesktop.org trash, which the watcher reports as an
// unpaired rename that the push side already drops, so a file mark would never
// be consumed and could swallow a genuine user deletion later. A mark is
// consumed by the first matching remove event, cleared by a create/write
// event for the path or by any write homedrive itself makes there
// (noteLocalWrite), and expires after the TTL.
//
// Not covered on purpose: conflict-loser retention (conflict.go,
// bisync_retention.go) unlinks `.old.N` copies with os.Remove, outside
// removeLocalPath. Its echoed push remove is what deletes the pruned copy
// from Drive (#83), so it must stay unguarded.
type DeleteGuard struct {
	root  string
	inner LocalRemover
	ttl   time.Duration
	now   func() time.Time

	mu        sync.Mutex
	marks     map[string]time.Time // rel path -> expiry
	lastPrune time.Time
}

// NewDeleteGuard wraps inner so deletions made through it are remembered
// for ttl. root is the local sync root; now defaults to time.Now.
func NewDeleteGuard(root string, inner LocalRemover, ttl time.Duration, now func() time.Time) *DeleteGuard {
	if now == nil {
		now = time.Now
	}
	return &DeleteGuard{
		root: filepath.Clean(root), inner: inner, ttl: ttl, now: now,
		marks: make(map[string]time.Time),
	}
}

// Remove deletes through the wrapped remover and, on success, marks the path
// as expected. The mark is set before the deletion so an event cannot beat
// it, and rolled back to its previous state if the deletion fails (a
// refused non-empty directory, or a path an earlier call already removed
// whose mark must survive).
func (g *DeleteGuard) Remove(path string) error {
	rel, ok := g.rel(path)
	if info, err := os.Lstat(path); ok && (err != nil || !info.IsDir()) {
		ok = false // only a directory removal echoes as a remove event
	}
	var prev time.Time
	var had bool
	if ok {
		prev, had = g.mark(rel)
	}
	err := removeLocalPath(g.inner, path)
	if err != nil && ok {
		g.restore(rel, prev, had)
	}
	return err
}

// noteLocalWrite tells r, when it is a DeleteGuard, that homedrive itself
// just created path (a file download or the directories made for it): any
// mark on path or its ancestors is stale, because the path exists again and a
// later remove event is the user's. The watcher drops these self-made creates
// as self-induced, so the push side never sees them and cannot clear the marks.
func noteLocalWrite(r LocalRemover, path string) {
	g, ok := r.(*DeleteGuard)
	if !ok || g == nil {
		return
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		rel, ok := g.rel(p)
		if !ok {
			return
		}
		g.Forget(rel)
	}
}

// Expected reports whether rel (root-relative, slash-separated) was just
// deleted by homedrive, consuming the mark.
func (g *DeleteGuard) Expected(rel string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.marks[rel]
	delete(g.marks, rel)
	return ok && g.now().Before(exp)
}

// Forget drops any mark for rel.
func (g *DeleteGuard) Forget(rel string) {
	g.mu.Lock()
	delete(g.marks, rel)
	g.mu.Unlock()
}

func (g *DeleteGuard) restore(rel string, prev time.Time, had bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if had {
		g.marks[rel] = prev
	} else {
		delete(g.marks, rel)
	}
}

// mark records rel and returns the mark it replaced, if any.
func (g *DeleteGuard) mark(rel string) (prev time.Time, had bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	prev, had = g.marks[rel]
	g.marks[rel] = now.Add(g.ttl)
	if now.Sub(g.lastPrune) < g.ttl {
		return prev, had
	}
	g.lastPrune = now
	for k, exp := range g.marks {
		if !now.Before(exp) {
			delete(g.marks, k)
		}
	}
	return prev, had
}

// rel converts an absolute path under the root to the watcher's
// root-relative slash form; paths outside the root are not tracked.
func (g *DeleteGuard) rel(path string) (string, bool) {
	r, err := filepath.Rel(g.root, path)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// WithDeleteGuard makes the push syncer drop remove events for deletions
// recorded in g.
func WithDeleteGuard(g *DeleteGuard) Option {
	return func(s *Syncer) { s.deleteGuard = g }
}

// skipExpectedDelete reports whether ev is the echo of a deletion homedrive
// made itself (no Drive call). A create or write event clears the mark: the
// path exists again, so any later remove event is the user's.
func (s *Syncer) skipExpectedDelete(logger *slog.Logger, ev Event) bool {
	if s.deleteGuard == nil {
		return false
	}
	switch ev.Op {
	case OpCreate, OpWrite:
		s.deleteGuard.Forget(ev.Path)
	case OpRemove:
		if s.deleteGuard.Expected(ev.Path) {
			logger.Info("ignoring remove event for a deletion made by homedrive: no Drive call",
				"op", "remove_suppressed", "origin", "local")
			s.appendAudit(AuditEntry{
				Timestamp: s.nowFn(), Op: "remove_suppressed", Path: ev.Path, Origin: "local",
			})
			return true
		}
	}
	return false
}
