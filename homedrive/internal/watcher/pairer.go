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
// inotify guarantees that IN_MOVED_FROM and IN_MOVED_TO with the same
// cookie arrive sequentially; fsnotify translates these into Rename and
// Create events. The pairer buffers Rename events on watched directories
// and pairs them with the next Create event for a directory.
type pairer struct {
	mu     sync.Mutex
	window time.Duration
	log    *slog.Logger

	// pending maps old directory paths to their rename entries.
	pending map[string]*renameEntry

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

// handleRename buffers a Rename event for a directory. If the path was
// a watched directory, it becomes a candidate for pairing. Returns true
// if the event was buffered (caller should not emit it yet).
func (p *pairer) handleRename(path string, at time.Time) bool {
	if !p.isTrackedDir(path) {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

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
// Rename. If so, the pair is emitted and child suppression begins.
// Returns true if the event was consumed by pairing.
func (p *pairer) handleCreate(path string, at time.Time) bool {
	// Check if the created path is a directory.
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Look for the best matching pending rename. On Linux, the inotify
	// events arrive in order, so there is typically exactly one pending
	// rename. We match the oldest pending rename since it should be the
	// one that pairs with this Create.
	var matchKey string
	var matchEntry *renameEntry

	for key, entry := range p.pending {
		if matchEntry == nil || entry.at.Before(matchEntry.at) {
			matchKey = key
			matchEntry = entry
		}
	}

	if matchEntry == nil {
		return false
	}

	// Found a pair.
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
		if strings.HasPrefix(path, prefix) {
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
}
