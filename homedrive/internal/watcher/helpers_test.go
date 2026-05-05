package watcher

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// stubStore implements SyncStore for testing the mtime guard.
type stubStore struct {
	records map[string]*SyncRecord
}

func (s *stubStore) GetSyncRecord(path string) *SyncRecord {
	if s == nil || s.records == nil {
		return nil
	}
	return s.records[path]
}

// newTestWatcher creates a Watcher for testing with a temp directory and
// short debounce/pair windows.
func newTestWatcher(t *testing.T, opts ...func(*Config)) (*Watcher, string) {
	t.Helper()
	root := t.TempDir()

	cfg := Config{
		LocalRoot:           root,
		Debounce:            100 * time.Millisecond,
		DirRenamePairWindow: 200 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	w, err := New(cfg, nil, log)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	return w, root
}

// startWatcher starts the watcher in a goroutine and returns a cancel func.
func startWatcher(t *testing.T, w *Watcher) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		if err := w.Start(ctx); err != nil && err != context.Canceled {
			t.Logf("watcher.Start returned: %v", err)
		}
	}()

	// Give the watcher time to set up watches.
	time.Sleep(50 * time.Millisecond)

	t.Cleanup(func() {
		cancel()
		w.Stop()
	})

	return cancel
}

// collectEvents reads events from the watcher channel with a timeout.
func collectEvents(t *testing.T, w *Watcher, timeout time.Duration) []WatchEvent {
	t.Helper()
	var events []WatchEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-w.Events():
			events = append(events, ev)
		case <-deadline:
			return events
		}
	}
}

// waitForEvent reads one event from the watcher channel with a timeout.
func waitForEvent(t *testing.T, w *Watcher, timeout time.Duration) (WatchEvent, bool) {
	t.Helper()
	select {
	case ev := <-w.Events():
		return ev, true
	case <-time.After(timeout):
		return WatchEvent{}, false
	}
}

// intToStr converts an integer to a string without importing strconv
// to keep test dependencies minimal.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
