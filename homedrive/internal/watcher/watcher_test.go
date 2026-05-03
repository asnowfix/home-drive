package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_CreateFile(t *testing.T) {
	w, root := newTestWatcher(t)
	startWatcher(t, w)

	path := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	ev, ok := waitForEvent(t, w, 500*time.Millisecond)
	if !ok {
		t.Fatal("timed out waiting for create event")
	}
	if ev.Event == nil {
		t.Fatal("expected Event, got DirRename")
	}
	if ev.Event.Path != path {
		t.Errorf("expected path %s, got %s", path, ev.Event.Path)
	}
	if ev.Event.Op != OpCreate {
		t.Errorf("expected OpCreate, got %v", ev.Event.Op)
	}
}

func TestWatcher_WriteDebounced(t *testing.T) {
	w, root := newTestWatcher(t)
	startWatcher(t, w)

	// Create a file first.
	path := filepath.Join(root, "burst.txt")
	if err := os.WriteFile(path, []byte("v0"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	// Wait for the create event to clear.
	collectEvents(t, w, 300*time.Millisecond)

	// Write 10 times rapidly.
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Collect events. The debouncer (100ms) should coalesce to 1 event.
	events := collectEvents(t, w, 500*time.Millisecond)

	writeCount := 0
	for _, ev := range events {
		if ev.Event != nil && ev.Event.Path == path {
			writeCount++
		}
	}

	if writeCount != 1 {
		t.Errorf("expected 1 debounced write event, got %d (total events: %d)",
			writeCount, len(events))
	}
}

func TestWatcher_DynamicAddWatch(t *testing.T) {
	w, root := newTestWatcher(t)
	startWatcher(t, w)

	// Create a new subdirectory.
	subdir := filepath.Join(root, "newdir")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}

	// Wait for the directory create event and watch setup.
	time.Sleep(200 * time.Millisecond)
	collectEvents(t, w, 300*time.Millisecond)

	// Create a file inside the new directory.
	filePath := filepath.Join(subdir, "file.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write file in new dir: %v", err)
	}

	ev, ok := waitForEvent(t, w, 500*time.Millisecond)
	if !ok {
		t.Fatal("timed out waiting for event in dynamically watched dir")
	}
	if ev.Event == nil {
		t.Fatal("expected Event, got DirRename")
	}
	if ev.Event.Path != filePath {
		t.Errorf("expected path %s, got %s", filePath, ev.Event.Path)
	}
}

func TestWatcher_ExcludeFilter(t *testing.T) {
	w, root := newTestWatcher(t, func(cfg *Config) {
		cfg.Exclude = []string{"**/*.swp", "**/.git/**"}
	})
	startWatcher(t, w)

	// Create an excluded file.
	swpPath := filepath.Join(root, "file.swp")
	if err := os.WriteFile(swpPath, []byte("swap"), 0644); err != nil {
		t.Fatalf("failed to write swp file: %v", err)
	}

	// Create a non-excluded file.
	txtPath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(txtPath, []byte("text"), 0644); err != nil {
		t.Fatalf("failed to write txt file: %v", err)
	}

	events := collectEvents(t, w, 500*time.Millisecond)

	for _, ev := range events {
		if ev.Event != nil && ev.Event.Path == swpPath {
			t.Errorf("received event for excluded path %s", swpPath)
		}
	}

	gotTxt := false
	for _, ev := range events {
		if ev.Event != nil && ev.Event.Path == txtPath {
			gotTxt = true
		}
	}
	if !gotTxt {
		t.Error("expected event for non-excluded file.txt")
	}
}

func TestWatcher_MtimeGuard(t *testing.T) {
	root := t.TempDir()

	filePath := filepath.Join(root, "synced.txt")
	if err := os.WriteFile(filePath, []byte("synced content"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	store := &stubStore{
		records: map[string]*SyncRecord{
			filePath: {
				LocalMtime: info.ModTime(),
				Size:       info.Size(),
			},
		},
	}

	cfg := Config{
		LocalRoot:           root,
		Debounce:            100 * time.Millisecond,
		DirRenamePairWindow: 200 * time.Millisecond,
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	w, err := New(cfg, store, log)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	startWatcher(t, w)

	// Rewrite with same content (simulating a pull echo).
	if err := os.WriteFile(filePath, []byte("synced content"), 0644); err != nil {
		t.Fatalf("failed to rewrite file: %v", err)
	}

	events := collectEvents(t, w, 500*time.Millisecond)

	for _, ev := range events {
		if ev.Event != nil && ev.Event.Path == filePath && ev.Event.Op == OpWrite {
			t.Error("expected mtime guard to suppress self-induced write")
		}
	}
}

func TestWatcher_InvalidConfig(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	_, err := New(Config{}, nil, log)
	if err == nil {
		t.Fatal("expected error for empty config")
	}
}

func TestWatcher_RemoveFile(t *testing.T) {
	w, root := newTestWatcher(t)
	startWatcher(t, w)

	path := filepath.Join(root, "doomed.txt")
	if err := os.WriteFile(path, []byte("goodbye"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	collectEvents(t, w, 300*time.Millisecond)

	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove file: %v", err)
	}

	ev, ok := waitForEvent(t, w, 500*time.Millisecond)
	if !ok {
		t.Fatal("timed out waiting for remove event")
	}
	if ev.Event == nil {
		t.Fatal("expected Event, got DirRename")
	}
	if ev.Event.Op != OpRemove {
		t.Errorf("expected OpRemove, got %v", ev.Event.Op)
	}
}
