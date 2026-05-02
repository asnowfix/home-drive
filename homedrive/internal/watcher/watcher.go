// Package watcher provides recursive fsnotify file watching with per-path
// debouncing and directory rename pairing via inotify cookies.
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher recursively monitors a directory tree for file system changes.
// It debounces rapid events per path, detects directory renames via
// inotify cookie pairing, applies exclusion filters, and consults a
// SyncStore to suppress self-induced echoes from recent pulls.
type Watcher struct {
	cfg    Config
	log    *slog.Logger
	store  SyncStore
	filter *filter

	fsw       *fsnotify.Watcher
	debouncer *debouncer
	pairer    *pairer

	// events is the output channel for consumers.
	events chan WatchEvent

	mu      sync.Mutex
	stopped bool
	done    chan struct{}
}

// New creates a Watcher. The store may be nil if the inode/mtime guard
// is not yet available (it will skip the guard check). Call Start() to
// begin watching.
func New(cfg Config, store SyncStore, log *slog.Logger) (*Watcher, error) {
	if cfg.LocalRoot == "" {
		return nil, fmt.Errorf("watcher: local_root must be set: %w", ErrInvalidConfig)
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = DefaultConfig().Debounce
	}
	if cfg.DirRenamePairWindow == 0 {
		cfg.DirRenamePairWindow = DefaultConfig().DirRenamePairWindow
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watcher: creating fsnotify watcher: %w", err)
	}

	w := &Watcher{
		cfg:    cfg,
		log:    log,
		store:  store,
		filter: newFilter(cfg.LocalRoot, cfg.Exclude),
		fsw:    fsw,
		events: make(chan WatchEvent, 1024),
		done:   make(chan struct{}),
	}

	w.debouncer = newDebouncer(cfg.Debounce, w.emitEvent)

	w.pairer = newPairer(
		cfg.DirRenamePairWindow,
		log,
		w.onDirRenamePaired,
		w.onDirRenameUnpaired,
	)

	return w, nil
}

// Events returns the read-only channel of watch events.
func (w *Watcher) Events() <-chan WatchEvent {
	return w.events
}

// Start performs the initial directory walk and begins the event loop.
// It blocks until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) error {
	if err := w.initialWalk(); err != nil {
		return fmt.Errorf("watcher: initial walk: %w", err)
	}

	w.log.Info("watcher started",
		"root", w.cfg.LocalRoot,
		"debounce", w.cfg.Debounce.String(),
		"pair_window", w.cfg.DirRenamePairWindow.String(),
		"exclude_patterns", len(w.cfg.Exclude),
	)

	return w.eventLoop(ctx)
}

// Stop shuts down the watcher. It is safe to call multiple times.
func (w *Watcher) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	w.mu.Unlock()

	w.debouncer.stop()
	w.pairer.stop()
	if err := w.fsw.Close(); err != nil {
		w.log.Warn("fsnotify close error", "error", err)
	}
	close(w.done)
}

// initialWalk walks the root directory tree and adds a watch on every
// directory that is not excluded.
func (w *Watcher) initialWalk() error {
	return filepath.WalkDir(w.cfg.LocalRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			w.log.Warn("walk error", "path", path, "error", err)
			return nil // Continue walking despite errors.
		}
		if !d.IsDir() {
			return nil
		}
		if w.filter.excluded(path) {
			return filepath.SkipDir
		}
		return w.addWatch(path)
	})
}

// addWatch adds an fsnotify watch on a directory and registers it with
// the pairer for rename detection.
func (w *Watcher) addWatch(path string) error {
	if err := w.fsw.Add(path); err != nil {
		return fmt.Errorf("watcher: adding watch on %s: %w", path, err)
	}
	w.pairer.trackDir(path)
	w.log.Debug("watch added", "path", path)
	return nil
}

// removeWatch removes the fsnotify watch and unregisters from the pairer.
func (w *Watcher) removeWatch(path string) {
	_ = w.fsw.Remove(path) // best-effort; path may already be unwatched after rename
	w.pairer.untrackDir(path)
}

// eventLoop reads fsnotify events and dispatches them through the
// debouncer and rename pairer. It runs until ctx is cancelled or the
// fsnotify watcher is closed.
func (w *Watcher) eventLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			w.Stop()
			return ctx.Err()

		case <-w.done:
			return nil

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handleFSEvent(ev)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Error("fsnotify error", "error", err)
		}
	}
}

// handleFSEvent processes a single fsnotify event through the filter,
// pairer, mtime guard, and debouncer.
func (w *Watcher) handleFSEvent(ev fsnotify.Event) {
	path := ev.Name

	// Skip excluded paths.
	if w.filter.excluded(path) {
		return
	}

	// Check if this event is suppressed by a recent directory rename.
	if w.pairer.isSuppressed(path) {
		return
	}

	now := time.Now()

	// Handle rename events: buffer directory renames for pairing.
	if ev.Has(fsnotify.Rename) {
		if w.pairer.handleRename(path, now) {
			return
		}
		// File rename (not a directory we were watching) falls through
		// to the debouncer as a regular rename event.
		w.debouncePath(path, OpRename, now)
		return
	}

	// Handle create events: check if they pair with a buffered rename.
	if ev.Has(fsnotify.Create) {
		if w.pairer.handleCreate(path, now) {
			return
		}

		// Not a paired rename. If it is a new directory, add a watch.
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if addErr := w.addWatch(path); addErr != nil {
				w.log.Warn("failed to add watch on new directory",
					"path", path,
					"error", addErr,
				)
			}
			// Walk the new directory to catch any files already inside.
			w.walkNewDir(path)
		}

		w.debouncePath(path, OpCreate, now)
		return
	}

	// Handle write events.
	if ev.Has(fsnotify.Write) {
		w.debouncePath(path, OpWrite, now)
		return
	}

	// Handle remove events.
	if ev.Has(fsnotify.Remove) {
		// The kernel automatically removes the inotify watch when the
		// directory is deleted; just clean up our tracking.
		w.pairer.untrackDir(path)
		w.debouncePath(path, OpRemove, now)
		return
	}
}

// debouncePath checks the mtime guard and feeds the event to the debouncer.
func (w *Watcher) debouncePath(path string, op Op, at time.Time) {
	// Inode/mtime guard: suppress self-induced echoes.
	if w.store != nil && (op == OpWrite || op == OpCreate) {
		if w.isSelfInduced(path) {
			w.log.Debug("suppressed self-induced event",
				"path", path,
				"op", op.String(),
			)
			return
		}
	}

	w.debouncer.add(Event{Path: path, Op: op, At: at})
}

// isSelfInduced checks if a file event matches the last recorded sync
// state within a 1-second tolerance, indicating it was caused by a
// recent pull rather than a user modification.
func (w *Watcher) isSelfInduced(path string) bool {
	rec := w.store.GetSyncRecord(path)
	if rec == nil {
		return false
	}

	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	mtime := info.ModTime()
	diff := mtime.Sub(rec.LocalMtime)
	if diff < 0 {
		diff = -diff
	}

	return diff <= time.Second && info.Size() == rec.Size
}

// walkNewDir adds watches to all subdirectories of a newly created directory.
func (w *Watcher) walkNewDir(root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() || path == root {
			return nil
		}
		if w.filter.excluded(path) {
			return filepath.SkipDir
		}
		if addErr := w.addWatch(path); addErr != nil {
			w.log.Warn("failed to add watch during new dir walk",
				"path", path,
				"error", addErr,
			)
		}
		return nil
	})
}

// emitEvent sends a debounced event to the output channel.
func (w *Watcher) emitEvent(ev Event) {
	select {
	case w.events <- WatchEvent{Event: &ev}:
	default:
		w.log.Warn("event channel full, dropping event",
			"path", ev.Path,
			"op", ev.Op.String(),
		)
	}
}

// onDirRenamePaired handles a successful directory rename pairing.
// It re-watches the new subtree and suppresses child events.
func (w *Watcher) onDirRenamePaired(dr DirRename) {
	// Remove watches under the old path.
	w.removeSubtreeWatches(dr.From)

	// Add watches under the new path.
	if err := w.addWatch(dr.To); err != nil {
		w.log.Warn("failed to re-watch renamed directory",
			"path", dr.To,
			"error", err,
		)
	}
	w.walkNewDir(dr.To)

	// Suppress any debounced events under both old and new paths.
	w.debouncer.suppressPrefix(dr.From + string(filepath.Separator))
	w.debouncer.suppressPrefix(dr.To + string(filepath.Separator))

	// Emit the paired rename event.
	select {
	case w.events <- WatchEvent{DirRename: &dr}:
	default:
		w.log.Warn("event channel full, dropping dir_rename",
			"from", dr.From,
			"to", dr.To,
		)
	}
}

// onDirRenameUnpaired handles an expired rename (no matching Create).
// It falls back to standard delete handling.
func (w *Watcher) onDirRenameUnpaired(ev Event) {
	w.emitEvent(ev)
}

// removeSubtreeWatches removes watches on the given directory and all
// its known subdirectories.
func (w *Watcher) removeSubtreeWatches(root string) {
	prefix := root + string(filepath.Separator)
	w.removeWatch(root)

	// Walk the pairer's tracked dirs to find children.
	w.pairer.mu.Lock()
	var toRemove []string
	for dir := range w.pairer.watchedDirs {
		if len(dir) > len(prefix) && dir[:len(prefix)] == prefix {
			toRemove = append(toRemove, dir)
		}
	}
	w.pairer.mu.Unlock()

	for _, dir := range toRemove {
		w.removeWatch(dir)
	}
}
