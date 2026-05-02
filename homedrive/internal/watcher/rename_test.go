package watcher

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWatcher_DirRenameSmall(t *testing.T) {
	w, root := newTestWatcher(t)
	startWatcher(t, w)

	// Create a directory with 10 files.
	srcDir := filepath.Join(root, "src_dir")
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 10; i++ {
		path := filepath.Join(srcDir, "file_"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to create file %d: %v", i, err)
		}
	}

	// Wait for create events to settle.
	collectEvents(t, w, 500*time.Millisecond)

	// Rename the directory.
	dstDir := filepath.Join(root, "dst_dir")
	if err := os.Rename(srcDir, dstDir); err != nil {
		t.Fatalf("failed to rename directory: %v", err)
	}

	// Collect events. We expect exactly 1 DirRename event and no
	// per-file events.
	events := collectEvents(t, w, 1*time.Second)

	dirRenames := 0
	childEvents := 0
	for _, ev := range events {
		if ev.DirRename != nil {
			dirRenames++
			if ev.DirRename.From != srcDir {
				t.Errorf("DirRename.From = %q, want %q", ev.DirRename.From, srcDir)
			}
			if ev.DirRename.To != dstDir {
				t.Errorf("DirRename.To = %q, want %q", ev.DirRename.To, dstDir)
			}
		}
		if ev.Event != nil {
			childEvents++
		}
	}

	if dirRenames != 1 {
		t.Errorf("expected 1 DirRename event, got %d", dirRenames)
	}
	if childEvents > 0 {
		t.Errorf("expected 0 child events (suppressed), got %d", childEvents)
	}
}

func TestWatcher_DirRenameLarge(t *testing.T) {
	// On macOS/kqueue, processing 5k file events can delay the Rename event
	// relative to its paired Create by more than the default 200ms window.
	w, root := newTestWatcher(t, func(cfg *Config) {
		if runtime.GOOS != "linux" {
			cfg.DirRenamePairWindow = 1 * time.Second
		}
	})
	startWatcher(t, w)

	// Create a directory with 5k files.
	srcDir := filepath.Join(root, "big_dir")
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 5000; i++ {
		name := filepath.Base(srcDir) + "_file_" + intToStr(i) + ".txt"
		path := filepath.Join(srcDir, name)
		if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to create file %d: %v", i, err)
		}
	}

	// Wait for create events to settle.
	collectEvents(t, w, 3*time.Second)

	// Rename the directory and measure latency.
	dstDir := filepath.Join(root, "big_dir_renamed")
	start := time.Now()
	if err := os.Rename(srcDir, dstDir); err != nil {
		t.Fatalf("failed to rename directory: %v", err)
	}

	// Wait for the DirRename event specifically.
	var latency time.Duration
	gotRename := false
	deadline := time.After(2 * time.Second)
	for !gotRename {
		select {
		case ev := <-w.Events():
			if ev.DirRename != nil {
				latency = time.Since(start)
				gotRename = true
				if ev.DirRename.From != srcDir {
					t.Errorf("DirRename.From = %q, want %q",
						ev.DirRename.From, srcDir)
				}
				if ev.DirRename.To != dstDir {
					t.Errorf("DirRename.To = %q, want %q",
						ev.DirRename.To, dstDir)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for DirRename event for large dir")
		}
	}

	// Linux/inotify delivers paired rename events synchronously; kqueue on
	// macOS may add latency from the directory-scan step in fsnotify.
	latencyThreshold := 100 * time.Millisecond
	if runtime.GOOS != "linux" {
		latencyThreshold = 500 * time.Millisecond
	}
	if latency > latencyThreshold {
		t.Errorf("DirRename latency %v exceeds %v threshold", latency, latencyThreshold)
	}

	t.Logf("DirRename latency for 5k-file directory: %v", latency)
}

func TestWatcher_DirRenameCrossMount(t *testing.T) {
	// Cross-mount renames cannot be detected via cookies because the
	// inode spaces are different. We simulate this by renaming to a
	// path outside the watched tree (which produces only a Rename event
	// without a matching Create).
	w, root := newTestWatcher(t, func(cfg *Config) {
		cfg.DirRenamePairWindow = 200 * time.Millisecond
	})
	startWatcher(t, w)

	srcDir := filepath.Join(root, "cross_mount")
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Write a file inside so the directory is not empty.
	innerFile := filepath.Join(srcDir, "file.txt")
	if err := os.WriteFile(innerFile, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Wait for events to settle.
	collectEvents(t, w, 500*time.Millisecond)

	// Rename to a directory outside the watched root, simulating
	// a cross-mount move.
	externalDir := t.TempDir()
	dstDir := filepath.Join(externalDir, "cross_mount")
	if err := os.Rename(srcDir, dstDir); err != nil {
		t.Fatalf("failed to rename directory: %v", err)
	}

	// Wait for the pair window to expire. We should get a fallback
	// Rename event (not a DirRename).
	events := collectEvents(t, w, 1*time.Second)

	gotFallback := false
	for _, ev := range events {
		if ev.DirRename != nil {
			t.Error("unexpected DirRename event for cross-mount move")
		}
		if ev.Event != nil && ev.Event.Op == OpRename {
			gotFallback = true
		}
	}

	if !gotFallback {
		t.Error("expected fallback Rename event for cross-mount move")
	}
}

func TestWatcher_RapidDoubleRename(t *testing.T) {
	w, root := newTestWatcher(t)
	startWatcher(t, w)

	// Create two directories.
	dir1 := filepath.Join(root, "dir1")
	dir2 := filepath.Join(root, "dir2")
	if err := os.Mkdir(dir1, 0755); err != nil {
		t.Fatalf("failed to mkdir dir1: %v", err)
	}
	if err := os.Mkdir(dir2, 0755); err != nil {
		t.Fatalf("failed to mkdir dir2: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	collectEvents(t, w, 500*time.Millisecond)

	// Rapid double rename.
	dst1 := filepath.Join(root, "dir1_renamed")
	dst2 := filepath.Join(root, "dir2_renamed")
	if err := os.Rename(dir1, dst1); err != nil {
		t.Fatalf("failed to rename dir1: %v", err)
	}
	if err := os.Rename(dir2, dst2); err != nil {
		t.Fatalf("failed to rename dir2: %v", err)
	}

	events := collectEvents(t, w, 1*time.Second)

	dirRenames := 0
	for _, ev := range events {
		if ev.DirRename != nil {
			dirRenames++
		}
	}

	if dirRenames != 2 {
		t.Errorf("expected 2 DirRename events, got %d", dirRenames)
	}
}

func TestWatcher_DirRenameWithExcludedFiles(t *testing.T) {
	w, root := newTestWatcher(t, func(cfg *Config) {
		cfg.Exclude = []string{"**/*.swp"}
	})
	startWatcher(t, w)

	// Create a directory containing only excluded files.
	srcDir := filepath.Join(root, "excl_dir")
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	swpPath := filepath.Join(srcDir, "file.swp")
	if err := os.WriteFile(swpPath, []byte("swap"), 0644); err != nil {
		t.Fatalf("failed to write excluded file: %v", err)
	}

	// Wait for events to settle.
	collectEvents(t, w, 500*time.Millisecond)

	// Rename the directory. The rename should still be paired even
	// though all files inside are excluded.
	dstDir := filepath.Join(root, "excl_dir_renamed")
	if err := os.Rename(srcDir, dstDir); err != nil {
		t.Fatalf("failed to rename directory: %v", err)
	}

	events := collectEvents(t, w, 1*time.Second)

	dirRenames := 0
	for _, ev := range events {
		if ev.DirRename != nil {
			dirRenames++
			if ev.DirRename.From != srcDir {
				t.Errorf("DirRename.From = %q, want %q",
					ev.DirRename.From, srcDir)
			}
			if ev.DirRename.To != dstDir {
				t.Errorf("DirRename.To = %q, want %q",
					ev.DirRename.To, dstDir)
			}
		}
	}

	if dirRenames != 1 {
		t.Errorf("expected 1 DirRename event, got %d", dirRenames)
	}
}
