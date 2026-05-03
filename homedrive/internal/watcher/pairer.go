package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// renameEntry records a buffered directory Rename event waiting for a
// matching Create to arrive within the pair window.
type renameEntry struct {
	path  string
	at    time.Time
	timer *time.Timer
}

// pairer detects directory renames by matching Rename events with
// subsequent Create events within a configurable time window. On Linux,
// inotify guarantees that IN_MOVED_FROM and IN_MOVED_TO arrive in order;
// on macOS/kqueue the Create(dst) can arrive before the Rename(src). Both
// orderings are handled via the pending and recentCreates maps.
type pairer struct {
	mu     sync.Mutex
	window time.Duration
	log    *slog.Logger

	// pending maps old directory paths to their rename entries.
	// Used on Linux/inotify where Rename arrives before Create.
	pending map[string]*renameEntry

	// recentCreates records directory Create events that arrived before any
	// matching Rename (macOS/kqueue path). Keyed by the new directory path,
	// value is the event time. Entries are lazily expired by handleRename.
	recentCreates map[string]time.Time

	// suppressedPrefixes tracks directory paths that have been paired.
	// Child events under these prefixes are suppressed for the duration
	// of the pair window to avoid redundant per-file events.
	suppressedPrefixes map[string]time.Time

	// onPair is called when a directory rename pair is detected.
	onPair func(DirRename)

	// onUnpaired is called when a Rename expires without a match (fallback
	// to delete handling).
	onUnpaired func(Event)

	// watchedDirs tracks which paths had watches, so we can identify
	// whether a Rename event is for a directory we were watching.
	watchedDirs map[string]struct{}
}

// newPairer creates a rename pairer with the given window and callbacks.
func newPairer(window time.Duration, log *slog.Logger, onPair func(DirRename), onUnpaired func(Event)) *pairer {
	return &pairer{
		window:             window,
		log:                log,
		pending:            make(map[string]*renameEntry),
		recentCreates:      make(map[string]time.Time),
		suppressedPrefixes: make(map[string]time.Time),
		onPair:             onPair,
		onUnpaired:         onUnpaired,
		watchedDirs:        make(map[string]struct{}),
	}
}

// trackDir registers a directory path as being watched. This is needed
// to determine whether a Rename event is for a directory (eligible for
// pairing) or a file (handled normally).
func (p *pairer) trackDir(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.watchedDirs[path] = struct{}{}
}

// untrackDir removes a directory from the tracked set.
func (p *pairer) untrackDir(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.watchedDirs, path)
}

// isTrackedDir checks if a path was a watched directory.
func (p *pairer) isTrackedDir(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.watchedDirs[path]
	return ok
}

// handleRename buffers a Rename event for a directory. It first checks
// recentCreates for a reverse match (macOS/kqueue: Create arrived before
// Rename). If found, the pair is completed immediately. Otherwise the
// Rename is buffered for the pair window. Returns true if the event was
// consumed (caller should not emit it).
func (p *pairer) handleRename(path string, at time.Time) bool {
	if !p.isTrackedDir(path) {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Check for a Create that arrived before this Rename (macOS/kqueue path).
	// Pick the oldest recent create within the pair window.
	cutoff := at.Add(-p.window)
	var matchDst string
	var matchAt time.Time
	for dstPath, createAt := range p.recentCreates {
		if createAt.Before(cutoff) {
			delete(p.recentCreates, dstPath) // lazy expiry
			continue
		}
		if matchDst == "" || createAt.Before(matchAt) {
			matchDst = dstPath
			matchAt = createAt
		}
	}

	if matchDst != "" {
		delete(p.recentCreates, matchDst)

		suppressUntil := time.Now().Add(p.window)
		p.suppressedPrefixes[path+string(filepath.Separator)] = suppressUntil
		p.suppressedPrefixes[matchDst+string(filepath.Separator)] = suppressUntil

		p.log.Info("directory rename paired (reverse)",
			"from", path,
			"to", matchDst,
			"op", "dir_rename",
		)

		dr := DirRename{From: path, To: matchDst, At: at}
		p.mu.Unlock()
		p.onPair(dr)
		p.mu.Lock()
		return true
	}

	// No recent Create found; buffer the Rename (Linux/inotify path).
	entry := &renameEntry{path: path, at: at}
	entry.timer = time.AfterFunc(p.window, func() {
		p.expireRename(path)
	})

	p.pending[path] = entry

	p.log.Debug("rename buffered for pairing",
		"path", path,
		"window", p.window.String(),
	)
	return true
}

// handleCreate checks if a Create event on a directory matches a pending
// Rename (Linux/inotify path). If so, the pair is emitted. If no pending
// Rename exists, the Create is recorded in recentCreates for reverse
// matching when the Rename arrives later (macOS/kqueue path).
// Returns true only when the event is fully consumed by a forward pair.
func (p *pairer) handleCreate(path string, at time.Time) bool {
	// Check if the created path is a directory.
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Look for the best matching pending rename (forward pair: Rename before
	// Create). Match the oldest pending rename.
	var matchKey string
	var matchEntry *renameEntry

	for key, entry := range p.pending {
		if matchEntry == nil || entry.at.Before(matchEntry.at) {
			matchKey = key
			matchEntry = entry
		}
	}

	if matchEntry == nil {
		// No pending Rename yet. Record this Create so that a subsequent
		// Rename can pair with it (reverse pair: Create before Rename).
		p.recentCreates[path] = at
		p.log.Debug("create recorded for reverse pairing",
			"path", path,
			"window", p.window.String(),
		)
		return false
	}

	// Found a forward pair.
	matchEntry.timer.Stop()
	delete(p.pending, matchKey)

	// Set up child event suppression for both the old and new paths.
	suppressUntil := time.Now().Add(p.window)
	p.suppressedPrefixes[matchKey+string(filepath.Separator)] = suppressUntil
	p.suppressedPrefixes[path+string(filepath.Separator)] = suppressUntil

	p.log.Info("directory rename paired",
		"from", matchKey,
		"to", path,
		"op", "dir_rename",
	)

	dr := DirRename{
		From: matchKey,
		To:   path,
		At:   at,
	}

	// Release lock before calling back.
	p.mu.Unlock()
	p.onPair(dr)
	p.mu.Lock()

	return true
}

// isSuppressed returns true if the path falls under a recently-paired
// directory rename and should be silently dropped.
func (p *pairer) isSuppressed(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for prefix, until := range p.suppressedPrefixes {
		if now.After(until) {
			delete(p.suppressedPrefixes, prefix)
			continue
		}
		// Also match the directory itself (IN_MOVE_SELF fires a Rename on
		// the dir's own wd after the pair completes and the dir is untracked).
		dirPath := strings.TrimSuffix(prefix, string(filepath.Separator))
		if path == dirPath || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// expireRename is called when the pair window expires without a match.
// It falls back to standard delete handling for the renamed path.
func (p *pairer) expireRename(path string) {
	p.mu.Lock()
	entry, ok := p.pending[path]
	if !ok {
		p.mu.Unlock()
		return
	}
	delete(p.pending, path)
	p.mu.Unlock()

	p.log.Debug("rename unpaired, falling back to delete",
		"path", path,
		"op", "rename",
	)

	p.onUnpaired(Event{
		Path: path,
		Op:   OpRename,
		At:   entry.at,
	})
}

// stop cancels all pending pair timers.
func (p *pairer) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for path, entry := range p.pending {
		entry.timer.Stop()
		delete(p.pending, path)
	}
	p.suppressedPrefixes = make(map[string]time.Time)
	p.recentCreates = make(map[string]time.Time)
}
