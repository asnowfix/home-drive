package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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

	// Write 5 times with no sleep — a true burst that must coalesce regardless
	// of scheduler jitter. (10ms sleeps inflated on loaded CI runners past the
	// 100ms debounce window, causing spurious splits.)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
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

	// Refresh the store record immediately before the rewrite so the guard
	// reference mtime is always within 1s of the write, regardless of how
	// long watcher initialisation took on a loaded race-detector runner.
	info, err = os.Stat(filePath)
	if err != nil {
		t.Fatalf("failed to re-stat before rewrite: %v", err)
	}
	store.set(filePath, &SyncRecord{LocalMtime: info.ModTime(), Size: info.Size()})

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

// createdPaths returns the set of paths of non-rename Event values.
func createdPaths(events []WatchEvent) map[string]int {
	got := make(map[string]int)
	for _, ev := range events {
		if ev.Event != nil && ev.Event.Op == OpCreate {
			got[ev.Event.Path]++
		}
	}
	return got
}

// TestWatcher_NewDirFiles covers the inotify new-directory race (#118):
// files written immediately after their parent directory is created (or
// already inside a directory moved in from elsewhere) must still produce
// a Create event, because the watch on the parent did not exist yet.
func TestWatcher_NewDirFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []string // relative to root; parents created with MkdirAll
	}{
		{"file_in_new_dir", []string{"sub/a.txt"}},
		{"many_files_in_new_dir", []string{"sub/a.txt", "sub/b.txt", "sub/c.txt"}},
		{"file_in_nested_new_dirs", []string{"sub/x/y/deep.txt", "sub/top.txt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skip("requires Linux inotify cookies")
			}
			w, root := newTestWatcher(t)
			startWatcher(t, w)

			// Back to back, with no pause: the race window.
			for _, rel := range tc.files {
				p := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(p, []byte("v1\n"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}

			got := createdPaths(collectEvents(t, w, 800*time.Millisecond))
			for _, rel := range tc.files {
				if got[filepath.Join(root, rel)] == 0 {
					t.Errorf("no create event for %s", rel)
				}
			}
			if n := got[filepath.Join(root, "sub")]; n != 0 {
				// Directories are not pushed (#105); a Create for the dir
				// itself is filtered downstream, so only warn on files.
				t.Logf("dir create events: %d", n)
			}
		})
	}
}

// TestWatcher_NewDirMovedIn is the deterministic variant: a populated
// directory built outside the watched tree is moved in (no rename pair),
// so every file in it pre-exists the watch.
func TestWatcher_NewDirMovedIn(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		exclude  []string
		wantNone []string // must not produce events
		guarded  []string // journaled with matching mtime
		want     []string
	}{
		{
			name:  "plain_and_nested",
			files: []string{"a.txt", "n/b.txt"},
			want:  []string{"a.txt", "n/b.txt"},
		},
		{
			name:     "excluded_files_skipped",
			files:    []string{"keep.txt", "skip.tmp", ".git/config", "n/also.tmp"},
			exclude:  []string{"**/*.tmp", "**/.git/**"},
			want:     []string{"keep.txt"},
			wantNone: []string{"skip.tmp", ".git/config", "n/also.tmp"},
		},
		{
			name:     "mtime_guard_suppresses_journaled",
			files:    []string{"synced.txt", "fresh.txt"},
			guarded:  []string{"synced.txt"},
			want:     []string{"fresh.txt"},
			wantNone: []string{"synced.txt"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skip("requires Linux inotify cookies")
			}
			root := t.TempDir()
			staging := t.TempDir()
			src := filepath.Join(staging, "moved")
			store := &stubStore{records: map[string]*SyncRecord{}}
			dst := filepath.Join(root, "moved")
			for _, rel := range tc.files {
				p := filepath.Join(src, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			for _, rel := range tc.guarded {
				info, err := os.Stat(filepath.Join(src, rel))
				if err != nil {
					t.Fatalf("stat: %v", err)
				}
				store.set(filepath.Join(dst, rel), &SyncRecord{LocalMtime: info.ModTime(), Size: info.Size()})
			}

			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
			w, err := New(Config{
				LocalRoot:           root,
				Exclude:             tc.exclude,
				Debounce:            100 * time.Millisecond,
				DirRenamePairWindow: 200 * time.Millisecond,
			}, store, log)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			startWatcher(t, w)

			if err := os.Rename(src, dst); err != nil {
				t.Skipf("cannot rename across filesystems: %v", err)
			}

			events := collectEvents(t, w, 800*time.Millisecond)
			for _, ev := range events {
				if ev.DirRename != nil {
					t.Errorf("unexpected DirRename %+v for an unpaired move-in", ev.DirRename)
				}
			}
			got := createdPaths(events)
			for _, rel := range tc.want {
				if got[filepath.Join(dst, rel)] == 0 {
					t.Errorf("no create event for %s", rel)
				}
			}
			for _, rel := range tc.wantNone {
				if got[filepath.Join(dst, rel)] != 0 {
					t.Errorf("unexpected create event for %s", rel)
				}
			}
		})
	}
}
