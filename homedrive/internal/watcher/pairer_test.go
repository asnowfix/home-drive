package watcher

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPairer_PairDirectoryRename(t *testing.T) {
	// Create a real directory that the pairer can stat.
	tmpDir := t.TempDir()
	srcDir := tmpDir + "/src"
	dstDir := tmpDir + "/dst"
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	var mu sync.Mutex
	var paired []DirRename
	var unpaired []Event

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	p := newPairer(
		500*time.Millisecond,
		log,
		func(dr DirRename) {
			mu.Lock()
			paired = append(paired, dr)
			mu.Unlock()
		},
		func(ev Event) {
			mu.Lock()
			unpaired = append(unpaired, ev)
			mu.Unlock()
		},
	)
	defer p.stop()

	// Track the source directory.
	p.trackDir(srcDir)

	// Simulate rename event (buffer it).
	now := time.Now()
	handled := p.handleRename(srcDir, now)
	if !handled {
		t.Fatal("expected handleRename to return true for tracked dir")
	}

	// Rename the actual directory so os.Stat in handleCreate works.
	if err := os.Rename(srcDir, dstDir); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Simulate create event (should pair).
	consumed := p.handleCreate(dstDir, now)
	if !consumed {
		t.Fatal("expected handleCreate to return true for paired rename")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paired) != 1 {
		t.Fatalf("expected 1 paired event, got %d", len(paired))
	}
	if paired[0].From != srcDir {
		t.Errorf("From = %q, want %q", paired[0].From, srcDir)
	}
	if paired[0].To != dstDir {
		t.Errorf("To = %q, want %q", paired[0].To, dstDir)
	}
}

func TestPairer_UnpairedExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := tmpDir + "/expiring"
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var mu sync.Mutex
	var unpaired []Event

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	p := newPairer(
		100*time.Millisecond,
		log,
		func(dr DirRename) {
			t.Error("unexpected pair callback")
		},
		func(ev Event) {
			mu.Lock()
			unpaired = append(unpaired, ev)
			mu.Unlock()
		},
	)
	defer p.stop()

	p.trackDir(srcDir)
	p.handleRename(srcDir, time.Now())

	// Wait for the pair window to expire.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(unpaired) != 1 {
		t.Fatalf("expected 1 unpaired event, got %d", len(unpaired))
	}
	if unpaired[0].Path != srcDir {
		t.Errorf("Path = %q, want %q", unpaired[0].Path, srcDir)
	}
}

func TestPairer_NonTrackedDirIgnored(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	p := newPairer(
		500*time.Millisecond,
		log,
		func(dr DirRename) { t.Error("unexpected pair") },
		func(ev Event) { t.Error("unexpected unpaired") },
	)
	defer p.stop()

	// handleRename on a non-tracked path should return false.
	if p.handleRename("/not/tracked", time.Now()) {
		t.Error("expected handleRename to return false for non-tracked path")
	}
}

func TestPairer_ChildSuppression(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := tmpDir + "/parent"
	dstDir := tmpDir + "/parent_renamed"
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	p := newPairer(
		500*time.Millisecond,
		log,
		func(dr DirRename) {},
		func(ev Event) {},
	)
	defer p.stop()

	p.trackDir(srcDir)
	p.handleRename(srcDir, time.Now())

	// Rename for real so handleCreate can stat.
	if err := os.Rename(srcDir, dstDir); err != nil {
		t.Fatalf("rename: %v", err)
	}

	p.handleCreate(dstDir, time.Now())

	// After pairing, child events under both old and new paths should
	// be suppressed.
	if !p.isSuppressed(srcDir + "/child.txt") {
		t.Error("expected child under old path to be suppressed")
	}
	if !p.isSuppressed(dstDir + "/child.txt") {
		t.Error("expected child under new path to be suppressed")
	}

	// A path outside the renamed directory should not be suppressed.
	if p.isSuppressed(tmpDir + "/other.txt") {
		t.Error("expected path outside renamed dir to NOT be suppressed")
	}
}

func TestPairer_HandleCreateForFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a regular file (not a directory).
	filePath := tmpDir + "/regular.txt"
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	p := newPairer(
		500*time.Millisecond,
		log,
		func(dr DirRename) { t.Error("unexpected pair for file") },
		func(ev Event) {},
	)
	defer p.stop()

	// handleCreate should return false for a regular file.
	if p.handleCreate(filePath, time.Now()) {
		t.Error("expected handleCreate to return false for a regular file")
	}
}
