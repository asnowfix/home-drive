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
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("expected file photos/sunset.jpg to be pulled to local: %v", err)
	}
	// Regression check: bisync used to write an empty placeholder file
	// instead of actually downloading the remote content (see PLAN.md
	// §7.2 and issue #49). RemoteFS.DownloadFile must be used instead.
	if want := "content-of-photos/sunset.jpg"; string(data) != want {
		t.Errorf("expected downloaded content %q, got %q", want, string(data))
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

	var entry BisyncAuditEntry
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
	var entry BisyncAuditEntry
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

	// Regression check: resolveRemoteWins used to write an empty
	// placeholder instead of downloading the winning remote content.
	localPath := filepath.Join(root, "conflict.txt")
	data, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read resolved conflict.txt: %v", err)
	}
	if want := "content-of-conflict.txt"; string(data) != want {
		t.Errorf("expected downloaded content %q, got %q", want, string(data))
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

// TestBisync_ConflictPrunesOldSiblingsBeyondMaxPerFile is a regression test
// for the inline retention GC wiring (PLAN.md §11.5): a new conflict loser
// collapses onto the next free N (per PLAN.md §11.2/#65), and the retention
// GC then evicts older siblings beyond Retention.MaxPerFile right in the
// same bisync pass, on whichever side they live.
func TestBisync_ConflictPrunesOldSiblingsBeyondMaxPerFile(t *testing.T) {
	root := t.TempDir()
	localTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)

	createLocalFile(t, root, "conflict.txt", localTime)

	remote := newMockRemoteFS()
	journal := newMockJournal()
	mqtt := newMockMQTT()
	audit := &threadSafeBuffer{}
	clk := newMockClock(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))

	bisync, _ := NewBisync(BisyncOpts{
		Config: BisyncConfig{
			Interval:  time.Hour,
			LocalRoot: root,
			Retention: RetentionPolicy{MaxPerFile: 1},
		},
		Remote:  remote,
		Journal: journal,
		MQTT:    mqtt,
		Audit:   audit,
		Clock:   clk,
		Mu:      &sync.RWMutex{},
	})

	remote.Seed("conflict.txt", remoteTime, "remote-md5")
	remote.Seed("conflict.txt.old.1", remoteTime, "old1-md5")
	remote.Seed("conflict.txt.old.2", remoteTime, "old2-md5")
	journal.Seed(JournalEntry{
		Path:        "conflict.txt",
		LocalMtime:  time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMtime: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
		RemoteMD5:   "old-md5",
		LastOrigin:  "local",
	})
	journal.Seed(JournalEntry{
		Path:         "conflict.txt.old.1",
		LastSyncedAt: time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC),
		LastOrigin:   "remote",
	})
	journal.Seed(JournalEntry{
		Path:         "conflict.txt.old.2",
		LastSyncedAt: time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC),
		LastOrigin:   "remote",
	})

	bisync.execute(context.Background())

	if !remote.HasFile("conflict.txt.old.3") {
		t.Error("expected new loser conflict.txt.old.3 to exist")
	}
	if remote.HasFile("conflict.txt.old.1") {
		t.Error("expected conflict.txt.old.1 to be pruned (beyond max_per_file: 1)")
	}
	if remote.HasFile("conflict.txt.old.2") {
		t.Error("expected conflict.txt.old.2 to be pruned (beyond max_per_file: 1)")
	}
	if journal.Exists("conflict.txt.old.1") {
		t.Error("expected journal entry for conflict.txt.old.1 to be deleted")
	}
	if journal.Exists("conflict.txt.old.2") {
		t.Error("expected journal entry for conflict.txt.old.2 to be deleted")
	}
	if !journal.Exists("conflict.txt.old.3") {
		t.Error("expected journal entry for conflict.txt.old.3 to survive (newest)")
	}
}

// TestBisync_ShouldSweep_GatesOnInterval verifies the periodic sweep only
// fires once SweepInterval has elapsed since lastSweep, and never when
// SweepInterval is zero (disabled).
func TestBisync_ShouldSweep_GatesOnInterval(t *testing.T) {
	clk := newMockClock(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))
	bisync, _ := NewBisync(BisyncOpts{
		Config: BisyncConfig{
			Interval:      time.Hour,
			SweepInterval: 24 * time.Hour,
		},
		Remote:  newMockRemoteFS(),
		Journal: newMockJournal(),
		Clock:   clk,
		Mu:      &sync.RWMutex{},
	})

	if !bisync.shouldSweep() {
		t.Error("expected first-ever call to be due (lastSweep is zero)")
	}

	bisync.lastSweep = clk.Now()
	if bisync.shouldSweep() {
		t.Error("expected sweep not due immediately after running")
	}

	clk.Advance(24 * time.Hour)
	if !bisync.shouldSweep() {
		t.Error("expected sweep due after sweep_interval elapsed")
	}

	bisync.cfg.SweepInterval = 0
	if bisync.shouldSweep() {
		t.Error("expected sweep disabled when sweep_interval is 0")
	}
}

// TestBisync_ChainRepair_RunsOnceThenSkips verifies the automatic chain
// repair pass runs on the first execute() after upgrade, renumbers a
// pre-existing nested chain, and is skipped on every subsequent pass
// (guarded by the journal meta key, not re-scanning every tick).
func TestBisync_ChainRepair_RunsOnceThenSkips(t *testing.T) {
	root := t.TempDir()
	createLocalFile(t, root, "notes.md", time.Now())
	createLocalFile(t, root, "notes.md.old.1.old.1", time.Now())

	remote := newMockRemoteFS()
	journal := newMockJournal()
	clk := newMockClock(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))

	bisync, _ := NewBisync(BisyncOpts{
		Config: BisyncConfig{
			Interval:     time.Hour,
			LocalRoot:    root,
			RepairChains: true,
		},
		Remote:  remote,
		Journal: journal,
		Clock:   clk,
		Mu:      &sync.RWMutex{},
	})

	bisync.execute(context.Background())

	if _, err := os.Stat(filepath.Join(root, "notes.md.old.1")); err != nil {
		t.Errorf("expected nested chain link to be renumbered to notes.md.old.1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.md.old.1.old.1")); !os.IsNotExist(err) {
		t.Errorf("expected nested chain link to be gone, stat err = %v", err)
	}
	done, err := journal.GetMeta(keyChainRepair)
	if err != nil || done != "done" {
		t.Errorf("expected chain repair completion marker to be set, got %q, err=%v", done, err)
	}

	// Second pass: repair must not run again (nothing left to repair,
	// and re-scanning every tick would be wasted work).
	createLocalFile(t, root, "notes.md.old.5.old.1", time.Now())
	bisync.execute(context.Background())
	if _, err := os.Stat(filepath.Join(root, "notes.md.old.5.old.1")); err != nil {
		t.Error("expected the second pass to leave a new nested-looking file untouched (repair already marked done)")
	}
}

// TestBisync_ChainRepair_DisabledDoesNotRun verifies RepairChains: false
// (the config opt-out) prevents the automatic pass from ever running.
func TestBisync_ChainRepair_DisabledDoesNotRun(t *testing.T) {
	root := t.TempDir()
	createLocalFile(t, root, "notes.md", time.Now())
	createLocalFile(t, root, "notes.md.old.1.old.1", time.Now())

	remote := newMockRemoteFS()
	journal := newMockJournal()
	clk := newMockClock(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))

	bisync, _ := NewBisync(BisyncOpts{
		Config: BisyncConfig{
			Interval:     time.Hour,
			LocalRoot:    root,
			RepairChains: false,
		},
		Remote:  remote,
		Journal: journal,
		Clock:   clk,
		Mu:      &sync.RWMutex{},
	})

	bisync.execute(context.Background())

	if _, err := os.Stat(filepath.Join(root, "notes.md.old.1.old.1")); err != nil {
		t.Error("expected nested chain to survive untouched when repair_chains is disabled")
	}
	if done, _ := journal.GetMeta(keyChainRepair); done == "done" {
		t.Error("expected completion marker to stay unset when repair_chains is disabled")
	}
}

// TestBisync_RunChainRepair_OnDemand verifies the on-demand entry point
// (POST /conflict/repair, `ctl conflict repair`) works even when the
// automatic pass is disabled, and that a non-dry-run marks completion so
// the automatic pass (if later enabled) does not repeat the work.
func TestBisync_RunChainRepair_OnDemand(t *testing.T) {
	root := t.TempDir()
	createLocalFile(t, root, "notes.md", time.Now())
	createLocalFile(t, root, "notes.md.old.1.old.1", time.Now())

	remote := newMockRemoteFS()
	journal := newMockJournal()
	clk := newMockClock(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))

	bisync, _ := NewBisync(BisyncOpts{
		Config: BisyncConfig{
			Interval:     time.Hour,
			LocalRoot:    root,
			RepairChains: false, // on-demand must work regardless
		},
		Remote:  remote,
		Journal: journal,
		Clock:   clk,
		Mu:      &sync.RWMutex{},
	})

	report, err := bisync.RunChainRepair(context.Background(), false)
	if err != nil {
		t.Fatalf("RunChainRepair: %v", err)
	}
	if len(report.Links) != 1 {
		t.Fatalf("len(Links) = %d, want 1", len(report.Links))
	}
	if _, err := os.Stat(filepath.Join(root, "notes.md.old.1")); err != nil {
		t.Errorf("expected on-demand repair to renumber the chain: %v", err)
	}
	done, _ := journal.GetMeta(keyChainRepair)
	if done != "done" {
		t.Error("expected a non-dry-run on-demand repair to mark completion")
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
