package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBisync_DetectsDrift_LocalOnlyPushes(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "docs/readme.txt", now)

	bisync, _, remote, journal, _, _ := newTestBisync(t, root, false)
	bisync.execute(context.Background())

	if !remote.HasFile("docs/readme.txt") {
		t.Error("expected file docs/readme.txt to be pushed to remote")
	}
	if remote.CopyCount() != 1 {
		t.Errorf("expected 1 copy call, got %d", remote.CopyCount())
	}
	if !journal.Exists("docs/readme.txt") {
		t.Error("expected journal entry for docs/readme.txt")
	}
	entry, _ := journal.Get("docs/readme.txt")
	if entry.LastOrigin != "local" {
		t.Errorf("expected origin=local, got %s", entry.LastOrigin)
	}
}

func TestBisync_DetectsDrift_RemoteOnlyPulls(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	bisync, _, remote, journal, _, _ := newTestBisync(t, root, false)
	remote.Seed("photos/sunset.jpg", now, "abc123")

	bisync.execute(context.Background())

	localPath := filepath.Join(root, "photos", "sunset.jpg")
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		t.Error("expected file photos/sunset.jpg to be pulled to local")
	}
	if !journal.Exists("photos/sunset.jpg") {
		t.Error("expected journal entry for photos/sunset.jpg")
	}
	entry, _ := journal.Get("photos/sunset.jpg")
	if entry.LastOrigin != "remote" {
		t.Errorf("expected origin=remote, got %s", entry.LastOrigin)
	}
}

func TestBisync_GlobalLockBlocksPushWorkers(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	createLocalFile(t, root, "file.txt", now)

	bisync, _, _, _, _, _ := newTestBisync(t, root, false)
	mu := bisync.Mu()

	// Simulate bisync holding the write lock.
	mu.Lock()

	pushBlocked := make(chan struct{})
	pushDone := make(chan struct{})

	go func() {
		close(pushBlocked)
		mu.RLock()
		defer mu.RUnlock()
		close(pushDone)
	}()

	<-pushBlocked

	select {
	case <-pushDone:
		t.Fatal("push worker should be blocked while bisync holds write lock")
	case <-time.After(50 * time.Millisecond):
		// Good: push is blocked.
	}

	mu.Unlock()

	select {
	case <-pushDone:
		// Good: push resumed.
	case <-time.After(time.Second):
		t.Fatal("push worker did not resume after bisync released lock")
	}
}

func TestBisync_PushWorkersResumeAfterCompletion(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	createLocalFile(t, root, "file.txt", now)

	bisync, _, _, _, _, _ := newTestBisync(t, root, false)
	mu := bisync.Mu()

	var pushAcquired atomic.Int32
	pushReady := make(chan struct{})

	const numWorkers = 3
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-pushReady
			mu.RLock()
			pushAcquired.Add(1)
			mu.RUnlock()
		}()
	}

	// Run bisync (takes write lock internally).
	bisync.execute(context.Background())

	// After bisync completes, signal push workers.
	close(pushReady)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if int(pushAcquired.Load()) != numWorkers {
			t.Errorf("expected %d push workers to acquire lock, got %d",
				numWorkers, pushAcquired.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("push workers did not resume after bisync completed")
	}
}

func TestBisync_AuditLogRecordsDurationAndCounts(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "local-only.txt", now)
	bisync, _, remote, _, _, audit := newTestBisync(t, root, false)
	remote.Seed("remote-only.txt", now, "xyz789")

	bisync.execute(context.Background())

	logStr := audit.String()
	if logStr == "" {
		t.Fatal("audit log is empty")
	}

	var entry AuditEntry
	if err := json.Unmarshal(
		[]byte(strings.TrimSpace(logStr)), &entry,
	); err != nil {
		t.Fatalf("failed to parse audit JSONL: %v\nlog: %s", err, logStr)
	}

	if entry.Op != "bisync" {
		t.Errorf("expected op=bisync, got %s", entry.Op)
	}
	if entry.FilesPushed < 1 {
		t.Errorf("expected files_pushed >= 1, got %d", entry.FilesPushed)
	}
	if entry.FilesPulled < 1 {
		t.Errorf("expected files_pulled >= 1, got %d", entry.FilesPulled)
	}
	if entry.FilesChanged < 2 {
		t.Errorf("expected files_changed >= 2, got %d", entry.FilesChanged)
	}
	if entry.Duration == "" {
		t.Error("expected duration_ms to be set")
	}
	if entry.DryRun {
		t.Error("expected dry_run=false")
	}
}

func TestBisync_DryRunDetectsButDoesNotSync(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "local-only.txt", now)
	bisync, _, remote, journal, _, audit := newTestBisync(t, root, true)
	remote.Seed("remote-only.txt", now, "xyz789")

	bisync.execute(context.Background())

	if remote.CopyCount() != 0 {
		t.Errorf("dry-run should not copy files, got %d copies",
			remote.CopyCount())
	}
	localPath := filepath.Join(root, "remote-only.txt")
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Error("dry-run should not download files to local")
	}
	if journal.Exists("local-only.txt") {
		t.Error("dry-run should not create journal entries")
	}
	if journal.Exists("remote-only.txt") {
		t.Error("dry-run should not create journal entries")
	}

	logStr := audit.String()
	if logStr == "" {
		t.Fatal("audit log should be written even in dry-run mode")
	}
	var entry AuditEntry
	if err := json.Unmarshal(
		[]byte(strings.TrimSpace(logStr)), &entry,
	); err != nil {
		t.Fatalf("failed to parse audit JSONL: %v", err)
	}
	if !entry.DryRun {
		t.Error("expected dry_run=true in audit log")
	}
}

func TestBisync_ForceTriggerRunsImmediately(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	createLocalFile(t, root, "force-test.txt", now)

	bisync, forceCh, remote, _, _, _ := newTestBisync(t, root, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- bisync.Run(ctx)
	}()

	forceCh <- struct{}{}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("force trigger did not cause bisync to run")
		default:
		}
		if remote.CopyCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-errCh; err != ErrBisyncCanceled {
		t.Errorf("expected ErrBisyncCanceled, got %v", err)
	}
}

func TestBisync_MQTTEventsPublished(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	createLocalFile(t, root, "mqtt-test.txt", now)

	bisync, _, _, _, mqtt, _ := newTestBisync(t, root, false)
	bisync.execute(context.Background())

	events := mqtt.Events()
	if len(events) < 2 {
		t.Fatalf("expected >= 2 MQTT events, got %d", len(events))
	}
	if events[0].Type != "bisync.started" {
		t.Errorf("expected first event bisync.started, got %s",
			events[0].Type)
	}
	last := events[len(events)-1]
	if last.Type != "bisync.completed" {
		t.Errorf("expected last event bisync.completed, got %s",
			last.Type)
	}
}

func TestBisync_ConflictDetectedAndResolved(t *testing.T) {
	root := t.TempDir()
	localTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "conflict.txt", localTime)

	bisync, _, remote, journal, _, _ := newTestBisync(t, root, false)
	remote.Seed("conflict.txt", remoteTime, "remote-md5")
	journal.Seed(JournalEntry{
		Path:        "conflict.txt",
		LocalMtime:  time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMtime: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMD5:   "old-md5",
		LastOrigin:  "local",
	})

	bisync.execute(context.Background())

	if !remote.HasFile("conflict.txt.old.1") {
		t.Error("expected remote conflict.txt.old.1 to exist")
	}
	if remote.MoveCount() != 1 {
		t.Errorf("expected 1 move (rename to .old), got %d",
			remote.MoveCount())
	}
	if !journal.Exists("conflict.txt") {
		t.Error("expected journal entry for conflict.txt")
	}
	if !journal.Exists("conflict.txt.old.1") {
		t.Error("expected journal entry for conflict.txt.old.1")
	}
}

func TestBisync_ConflictRemoteWins(t *testing.T) {
	root := t.TempDir()
	localTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	remoteTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "conflict.txt", localTime)

	bisync, _, remote, journal, _, _ := newTestBisync(t, root, false)
	remote.Seed("conflict.txt", remoteTime, "remote-md5-new")
	journal.Seed(JournalEntry{
		Path:        "conflict.txt",
		LocalMtime:  time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMtime: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMD5:   "old-md5",
		LastOrigin:  "local",
	})

	bisync.execute(context.Background())

	localOldPath := filepath.Join(root, "conflict.txt.old.1")
	if _, err := os.Stat(localOldPath); os.IsNotExist(err) {
		t.Error("expected local conflict.txt.old.1 to exist")
	}
	if !journal.Exists("conflict.txt") {
		t.Error("expected journal entry for conflict.txt")
	}
	entry, _ := journal.Get("conflict.txt")
	if entry.LastOrigin != "remote" {
		t.Errorf("expected origin=remote, got %s", entry.LastOrigin)
	}
	if !journal.Exists("conflict.txt.old.1") {
		t.Error("expected journal entry for conflict.txt.old.1")
	}
}

func TestBisync_OldNCollision(t *testing.T) {
	root := t.TempDir()
	localTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "notes.md", localTime)

	bisync, _, remote, journal, _, _ := newTestBisync(t, root, false)
	remote.Seed("notes.md", remoteTime, "remote-md5")
	journal.Seed(JournalEntry{
		Path:        "notes.md",
		LocalMtime:  time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMtime: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMD5:   "old-md5",
		LastOrigin:  "local",
	})
	journal.Seed(JournalEntry{
		Path:       "notes.md.old.1",
		LocalMtime: time.Date(2026, 4, 28, 11, 0, 0, 0, time.UTC),
		LastOrigin: "local",
	})

	bisync.execute(context.Background())

	if !remote.HasFile("notes.md.old.2") {
		t.Error("expected remote notes.md.old.2 (old.1 taken)")
	}
	if !journal.Exists("notes.md.old.2") {
		t.Error("expected journal entry for notes.md.old.2")
	}
}

func TestBisync_ForceRunReturnsErrorWhenRunning(t *testing.T) {
	root := t.TempDir()
	bisync, _, _, _, _, _ := newTestBisync(t, root, false)

	bisync.runMu.Lock()
	bisync.running = true
	bisync.runMu.Unlock()

	err := bisync.ForceRun(context.Background())
	if err != ErrBisyncRunning {
		t.Errorf("expected ErrBisyncRunning, got %v", err)
	}
}

func TestBisync_NoDiffsNoAction(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "synced.txt", now)
	bisync, _, remote, journal, _, _ := newTestBisync(t, root, false)
	remote.Seed("synced.txt", now, "same-md5")
	journal.Seed(JournalEntry{
		Path:        "synced.txt",
		LocalMtime:  now,
		RemoteMtime: now,
		RemoteMD5:   "same-md5",
		LastOrigin:  "local",
	})

	bisync.execute(context.Background())

	if remote.CopyCount() != 0 {
		t.Errorf("expected 0 copies, got %d", remote.CopyCount())
	}
	if remote.MoveCount() != 0 {
		t.Errorf("expected 0 moves, got %d", remote.MoveCount())
	}
}

func TestBisync_DefaultIntervalOneHour(t *testing.T) {
	root := t.TempDir()
	bisync, _, _, _, _, _ := newTestBisync(t, root, false)
	if bisync.cfg.Interval != time.Hour {
		t.Errorf("expected 1h, got %v", bisync.cfg.Interval)
	}
}

func TestBisync_MultipleLocalOnlyFiles(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "a.txt", now)
	createLocalFile(t, root, "b.txt", now)
	createLocalFile(t, root, "sub/c.txt", now)

	bisync, _, remote, _, _, _ := newTestBisync(t, root, false)
	bisync.execute(context.Background())

	if remote.CopyCount() != 3 {
		t.Errorf("expected 3 copy calls, got %d", remote.CopyCount())
	}
}
