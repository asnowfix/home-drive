package watcher

import (
	"sync"
	"testing"
	"time"
)

func TestDebouncer_SingleEvent(t *testing.T) {
	var mu sync.Mutex
	var received []Event

	d := newDebouncer(50*time.Millisecond, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})
	defer d.stop()

	d.add(Event{Path: "/tmp/file.txt", Op: OpWrite, At: time.Now()})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	if received[0].Path != "/tmp/file.txt" {
		t.Errorf("expected path /tmp/file.txt, got %s", received[0].Path)
	}
}

func TestDebouncer_MultipleWritesCoalesced(t *testing.T) {
	var mu sync.Mutex
	var received []Event

	d := newDebouncer(100*time.Millisecond, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})
	defer d.stop()

	// Send 10 rapid write events on the same path.
	for i := 0; i < 10; i++ {
		d.add(Event{
			Path: "/tmp/file.txt",
			Op:   OpWrite,
			At:   time.Now(),
		})
		time.Sleep(10 * time.Millisecond) // 10ms apart, well within 100ms window
	}

	// Wait for debounce to expire (100ms after last event + margin).
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 debounced event, got %d", len(received))
	}
}

func TestDebouncer_DifferentPathsIndependent(t *testing.T) {
	var mu sync.Mutex
	var received []Event

	d := newDebouncer(50*time.Millisecond, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})
	defer d.stop()

	d.add(Event{Path: "/tmp/a.txt", Op: OpWrite, At: time.Now()})
	d.add(Event{Path: "/tmp/b.txt", Op: OpWrite, At: time.Now()})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected 2 events (one per path), got %d", len(received))
	}

	paths := map[string]bool{}
	for _, ev := range received {
		paths[ev.Path] = true
	}
	if !paths["/tmp/a.txt"] || !paths["/tmp/b.txt"] {
		t.Errorf("expected events for both paths, got %v", paths)
	}
}

func TestDebouncer_SuppressPath(t *testing.T) {
	var mu sync.Mutex
	var received []Event

	d := newDebouncer(50*time.Millisecond, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})
	defer d.stop()

	d.add(Event{Path: "/tmp/file.txt", Op: OpWrite, At: time.Now()})
	d.suppress("/tmp/file.txt")

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Fatalf("expected 0 events after suppress, got %d", len(received))
	}
}

func TestDebouncer_SuppressPrefix(t *testing.T) {
	var mu sync.Mutex
	var received []Event

	d := newDebouncer(100*time.Millisecond, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})
	defer d.stop()

	d.add(Event{Path: "/tmp/dir/a.txt", Op: OpCreate, At: time.Now()})
	d.add(Event{Path: "/tmp/dir/b.txt", Op: OpCreate, At: time.Now()})
	d.add(Event{Path: "/tmp/other.txt", Op: OpCreate, At: time.Now()})

	d.suppressPrefix("/tmp/dir/")

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event (only /tmp/other.txt), got %d", len(received))
	}
	if received[0].Path != "/tmp/other.txt" {
		t.Errorf("expected /tmp/other.txt, got %s", received[0].Path)
	}
}

func TestDebouncer_Stop(t *testing.T) {
	var mu sync.Mutex
	var received []Event

	d := newDebouncer(100*time.Millisecond, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})

	d.add(Event{Path: "/tmp/file.txt", Op: OpWrite, At: time.Now()})
	d.stop()

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Fatalf("expected 0 events after stop, got %d", len(received))
	}
}

func TestDebouncer_CreateNotDowngradedToWrite(t *testing.T) {
	var mu sync.Mutex
	var received []Event

	d := newDebouncer(50*time.Millisecond, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})
	defer d.stop()

	// os.WriteFile fires IN_CREATE then IN_MODIFY in quick succession.
	// Create must not be downgraded to Write.
	d.add(Event{Path: "/tmp/file.txt", Op: OpCreate, At: time.Now()})
	d.add(Event{Path: "/tmp/file.txt", Op: OpWrite, At: time.Now()})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	if received[0].Op != OpCreate {
		t.Errorf("expected OpCreate (not downgraded to Write), got %v", received[0].Op)
	}
}
