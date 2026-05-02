package watcher

import (
	"sync"
	"time"
)

// debouncer coalesces rapid file system events per path. Each event resets
// the path's timer; the callback fires only after the debounce window
// elapses with no new events for that path.
type debouncer struct {
	mu     sync.Mutex
	window time.Duration
	timers map[string]*time.Timer
	// pending tracks the latest event per path during the debounce window.
	pending map[string]Event
	// emit is called when the debounce window expires for a path.
	emit func(Event)
}

// newDebouncer creates a debouncer with the given window and emission callback.
func newDebouncer(window time.Duration, emit func(Event)) *debouncer {
	return &debouncer{
		window:  window,
		timers:  make(map[string]*time.Timer),
		pending: make(map[string]Event),
		emit:    emit,
	}
}

// add registers or resets a debounce timer for the given event. If a timer
// already exists for the path, it is stopped and reset. The latest event's
// Op and At are preserved for emission.
func (d *debouncer) add(ev Event) {
	d.mu.Lock()
	defer d.mu.Unlock()

	path := ev.Path

	// Update the pending event. Don't downgrade Create → Write: os.WriteFile
	// fires IN_CREATE then IN_MODIFY in rapid succession; the Create must win.
	if existing, ok := d.pending[path]; !ok || existing.Op != OpCreate || ev.Op != OpWrite {
		d.pending[path] = ev
	}

	// If there is an existing timer, stop and reset it.
	if t, ok := d.timers[path]; ok {
		t.Stop()
	}

	d.timers[path] = time.AfterFunc(d.window, func() {
		d.mu.Lock()
		pending, ok := d.pending[path]
		if ok {
			delete(d.pending, path)
			delete(d.timers, path)
		}
		d.mu.Unlock()

		if ok {
			d.emit(pending)
		}
	})
}

// suppress cancels any pending debounce for the given path. This is used
// when a directory rename is paired and child events should be dropped.
func (d *debouncer) suppress(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if t, ok := d.timers[path]; ok {
		t.Stop()
		delete(d.timers, path)
		delete(d.pending, path)
	}
}

// suppressPrefix cancels all pending debounces whose paths start with the
// given prefix. Used when a directory rename pairs and all child events
// within the renamed subtree should be dropped.
func (d *debouncer) suppressPrefix(prefix string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for path, t := range d.timers {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			t.Stop()
			delete(d.timers, path)
			delete(d.pending, path)
		}
	}
}

// stop cancels all pending timers. Must be called during shutdown.
func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for path, t := range d.timers {
		t.Stop()
		delete(d.timers, path)
		delete(d.pending, path)
	}
}
