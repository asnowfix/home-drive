// symlink.go classifies symlinks in the sync root and tracks the ones the
// push and bisync paths skip (issue #191). homedrive never syncs a symlink:
// a dangling one made every upload fail with ENOENT and burn the full retry
// backoff, and a link leaving local_root must never be followed. One Lstat
// based classifier is shared by the push path (handleEvent) and the bisync
// walk (walkLocal); the watcher only needs Lstat so it never watches through
// a link.
package syncer

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Reasons a symlink is skipped. They are stable codes, never paths.
const (
	// LinkUnresolved: the target does not exist (dangling) or cannot be
	// resolved (loop, permission).
	LinkUnresolved = "unresolved"
	// LinkOutsideRoot: the link resolves outside local_root.
	LinkOutsideRoot = "outside_root"
	// LinkInRoot: the link resolves inside local_root. It is skipped too:
	// the target is synced on its own, and following links would make one
	// file appear under several paths and let a later retarget leave the
	// tree.
	LinkInRoot = "in_root"
)

// maxTrackedLinks bounds the reported-paths set (Pi 3B+ memory rule): at
// roughly 100 bytes an entry the set stays near 100 KiB. Links past the
// bound are still skipped and counted (see SymlinkTracker.Count) but are
// neither named in the set nor logged individually.
const maxTrackedLinks = 1024

// skippedLink is what the walk learned about one skipped symlink: why, and
// the raw link value (readlink output, never resolved).
type skippedLink struct {
	reason string
	target string
}

// classifyLink reports whether path (absolute) is a symlink and, if so, why
// it is skipped and its raw target string (os.Readlink, unresolved, "" when
// unreadable). It uses Lstat and Readlink, so it never reads through the
// link to its target. A path that cannot be lstat-ed is not a link as far as
// this helper knows.
func classifyLink(root, path string) (isLink bool, reason, rawTarget string) {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false, "", ""
	}
	rawTarget, _ = os.Readlink(path)
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return true, LinkUnresolved, rawTarget
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	rel, err := filepath.Rel(realRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true, LinkOutsideRoot, rawTarget
	}
	return true, LinkInRoot, rawTarget
}

// SymlinkTracker remembers the symlinks reported as skipped, so each is
// logged at ERROR once per run and counted for /status and status v2. Safe
// for concurrent use; the zero value is not usable, use NewSymlinkTracker.
type SymlinkTracker struct {
	mu       sync.Mutex
	log      *slog.Logger
	now      func() time.Time
	links    map[string]string // root-relative slash path -> reason, <= maxTrackedLinks
	overflow int               // links past the bound, as last counted by Reconcile
	since    time.Time         // first report of the current non-empty period
	policy   *LinkPolicy       // copy_resolved_links opt-in; nil copies nothing
}

// SetPolicy installs the copy_resolved_links opt-in. Call it before the
// tracker is shared with the push syncer and bisync.
func (t *SymlinkTracker) SetPolicy(p *LinkPolicy) { t.policy = p }

// Policy returns the installed opt-in (nil when none).
func (t *SymlinkTracker) Policy() *LinkPolicy { return t.policy }

// NewSymlinkTracker returns an empty tracker logging to log.
func NewSymlinkTracker(log *slog.Logger) *SymlinkTracker {
	if log == nil {
		log = slog.Default()
	}
	return &SymlinkTracker{log: log, now: time.Now, links: make(map[string]string)}
}

// Report records rel as a skipped symlink and logs it at ERROR the first
// time only.
func (t *SymlinkTracker) Report(rel, reason, target string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.addLocked(rel, skippedLink{reason: reason, target: target})
}

// addLocked adds rel if new. It returns false when the set is full.
func (t *SymlinkTracker) addLocked(rel string, l skippedLink) bool {
	if _, ok := t.links[rel]; ok {
		return true
	}
	if len(t.links) >= maxTrackedLinks {
		return false
	}
	if len(t.links) == 0 && t.overflow == 0 {
		t.since = t.now()
	}
	t.links[rel] = l.reason
	t.log.Error("symlink skipped: not synced", "op", "symlink_skip",
		"origin", "local", "path", rel, "reason", l.reason, "target", l.target)
	return true
}

// Clear forgets rel (deleted, or a regular file again).
func (t *SymlinkTracker) Clear(rel string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.links, rel)
}

// Reconcile replaces the tracked set with found, the complete result of a
// full local walk: paths no longer links leave the set, new ones are
// reported, and links past the bound are only counted.
func (t *SymlinkTracker) Reconcile(found map[string]skippedLink) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for p := range t.links {
		if _, ok := found[p]; !ok {
			delete(t.links, p)
		}
	}
	t.overflow = 0
	for p, l := range found {
		if !t.addLocked(p, l) {
			t.overflow++
		}
	}
	if len(t.links) == 0 && t.overflow == 0 {
		t.since = time.Time{}
	}
}

// Count is the number of skipped symlinks currently known.
func (t *SymlinkTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.links) + t.overflow
}

// Since is when the current set became non-empty (zero when empty).
func (t *SymlinkTracker) Since() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.since
}

// WithSymlinkTracker makes the push syncer report skipped symlinks to t,
// shared with bisync so one path is reported once whichever sees it first.
func WithSymlinkTracker(t *SymlinkTracker) Option {
	return func(s *Syncer) {
		if t != nil {
			s.links = t
		}
	}
}

// skipSymlinkEvent reports whether ev concerns a symlink that must not be
// pushed. It runs before the retry loop, so a skipped link makes no remote
// call and is never retried. Removals are not skipped (the path is gone and
// the deletion propagates as for any file); they and regular files drop
// the path from the tracker.
func (s *Syncer) skipSymlinkEvent(logger *slog.Logger, ev Event) bool {
	if ev.Op != OpCreate && ev.Op != OpWrite {
		s.links.Clear(ev.Path)
		return false
	}
	dec := decideLink(s.cfg.LocalRoot, filepath.Join(s.cfg.LocalRoot, filepath.FromSlash(ev.Path)), ev.Path, s.links.policy)
	switch {
	case !dec.isLink:
		s.links.Clear(ev.Path)
		return false
	case dec.copy && !dec.dir:
		// Opted in (symlinks.copy_resolved_links): the content is pushed
		// like a regular file (the upload opens the link, which reads it).
		s.links.Clear(ev.Path)
		return false
	case dec.copy:
		// A link to a directory tree: bisync copies its files; there is no
		// watch through it, so nothing to push for the link itself.
		s.links.Clear(ev.Path)
		logger.Debug("symlink to a directory: copied by bisync", "origin", "local")
		return true
	}
	s.links.Report(ev.Path, dec.reason, dec.raw)
	logger.Debug("skipping symlink: never pushed", "origin", "local", "reason", dec.reason)
	return true
}
