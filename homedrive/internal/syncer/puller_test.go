package syncer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestFile creates a file with the given content, creating parent dirs.
func writeTestFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// writeTestFileWithMtime creates a file and sets its mtime.
func writeTestFileWithMtime(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := writeTestFile(path, content); err != nil {
		t.Fatalf("writing test file %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("setting mtime for %s: %v", path, err)
	}
}

func TestPuller_SingleRemoteChange(t *testing.T) {
	// Single remote change should be downloaded and store updated.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{
			{
				Path: "docs/readme.txt",
				Object: RemoteObject{
					Path:    "docs/readme.txt",
					Size:    42,
					MD5:     "abc123",
					ModTime: now,
					ID:      "remote-id-1",
				},
			},
		},
		NextPageToken: "token-2",
	}

	store := newMockStore()
	audit := newMockAuditLogger()
	pub := newMockPublisher()

	p := NewPuller(
		PullerConfig{
			Interval:       30 * time.Second,
			LocalRoot:      localRoot,
			ConflictPolicy: PolicyNewerWins,
		},
		remote, store, audit, pub,
		slog.Default(),
		fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Verify file was downloaded.
	if len(remote.downloadedFiles) != 1 {
		t.Fatalf("expected 1 download, got %d", len(remote.downloadedFiles))
	}
	dl := remote.downloadedFiles[0]
	if dl.RemotePath != "docs/readme.txt" {
		t.Errorf("downloaded wrong path: %s", dl.RemotePath)
	}

	// Verify local file exists.
	localPath := filepath.Join(localRoot, "docs/readme.txt")
	if _, err := os.Stat(localPath); err != nil {
		t.Errorf("local file not created: %v", err)
	}

	// Verify journal entry updated.
	entry, ok, _ := store.Get(context.Background(), "docs/readme.txt")
	if !ok {
		t.Fatal("journal entry not created")
	}
	if entry.RemoteMD5 != "abc123" {
		t.Errorf("journal RemoteMD5 = %s, want abc123", entry.RemoteMD5)
	}
	if entry.RemoteID != "remote-id-1" {
		t.Errorf("journal RemoteID = %s, want remote-id-1", entry.RemoteID)
	}
	if entry.LastOrigin != "remote" {
		t.Errorf("journal LastOrigin = %s, want remote", entry.LastOrigin)
	}

	// Verify page token persisted.
	token, _ := store.GetPageToken(context.Background())
	if token != "token-2" {
		t.Errorf("page token = %s, want token-2", token)
	}

	// Verify audit log entry.
	entries := audit.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Op != "pull" {
		t.Errorf("audit op = %s, want pull", entries[0].Op)
	}
	if entries[0].Origin != "remote" {
		t.Errorf("audit origin = %s, want remote", entries[0].Origin)
	}

	// Verify MQTT event.
	pullEvents := pub.getMessagesByTopic(pub.Topic("events", "pull.success"))
	if len(pullEvents) != 1 {
		t.Fatalf("expected 1 pull.success event, got %d", len(pullEvents))
	}
}

func TestPuller_ConflictDetected(t *testing.T) {
	// Remote change where local file was also modified should trigger
	// conflict resolution and emit MQTT events.
	localRoot := t.TempDir()
	syncedAt := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	localModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	// Create a local file with a newer mtime than the remote.
	writeTestFileWithMtime(t,
		filepath.Join(localRoot, "notes.md"),
		"local content",
		localModTime,
	)

	remote := newMockRemoteFS()
	remote.startToken = "tok-1"
	remote.changes["tok-1"] = Changes{
		Items: []Change{
			{
				Path: "notes.md",
				Object: RemoteObject{
					Path:    "notes.md",
					Size:    100,
					MD5:     "new-remote-md5",
					ModTime: remoteModTime,
					ID:      "remote-id-notes",
				},
			},
		},
		NextPageToken: "tok-2",
	}
	remote.files["notes.md"] = RemoteObject{
		Path: "notes.md", MD5: "new-remote-md5", ModTime: remoteModTime, ID: "remote-id-notes",
	}

	store := newMockStore()
	// Pre-seed journal entry showing the file was last synced before both
	// local and remote modifications.
	store.entries["notes.md"] = JournalEntry{
		Path:         "notes.md",
		LocalMtime:   syncedAt,
		RemoteMtime:  syncedAt,
		RemoteMD5:    "old-md5",
		RemoteID:     "remote-id-notes",
		LastSyncedAt: syncedAt,
		LastOrigin:   "remote",
	}

	audit := newMockAuditLogger()
	pub := newMockPublisher()

	p := NewPuller(
		PullerConfig{
			Interval:       30 * time.Second,
			LocalRoot:      localRoot,
			ConflictPolicy: PolicyNewerWins,
		},
		remote, store, audit, pub,
		slog.Default(),
		fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Local is newer (14:00) than remote (13:00): local wins.
	// Remote should be moved to .old.1 (MoveFile on remote side).
	if len(remote.movedFiles) != 1 {
		t.Fatalf("expected 1 MoveFile call, got %d", len(remote.movedFiles))
	}
	mv := remote.movedFiles[0]
	if mv.Src != "notes.md" || mv.Dst != "notes.md.old.1" {
		t.Errorf("MoveFile(%s, %s), want (notes.md, notes.md.old.1)", mv.Src, mv.Dst)
	}

	// Verify MQTT conflict events.
	detected := pub.getMessagesByTopic(pub.Topic("events", "conflict.detected"))
	if len(detected) != 1 {
		t.Fatalf("expected 1 conflict.detected, got %d", len(detected))
	}
	resolved := pub.getMessagesByTopic(pub.Topic("events", "conflict.resolved"))
	if len(resolved) != 1 {
		t.Fatalf("expected 1 conflict.resolved, got %d", len(resolved))
	}

	// Verify audit log has a conflict entry.
	auditEntries := audit.getEntries()
	found := false
	for _, e := range auditEntries {
		if e.Op == "conflict" && e.Resolution == "newer_wins:local" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected audit entry with op=conflict and resolution=newer_wins:local")
	}
}

func TestPuller_ConflictRemoteNewer(t *testing.T) {
	// When remote is newer, remote wins: local file is renamed to .old.N
	// and remote version is downloaded.
	localRoot := t.TempDir()
	syncedAt := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	localModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	writeTestFileWithMtime(t,
		filepath.Join(localRoot, "report.pdf"),
		"local report",
		localModTime,
	)

	remote := newMockRemoteFS()
	remote.startToken = "tok-1"
	remote.changes["tok-1"] = Changes{
		Items: []Change{
			{
				Path: "report.pdf",
				Object: RemoteObject{
					Path:    "report.pdf",
					Size:    200,
					MD5:     "new-remote-md5",
					ModTime: remoteModTime,
					ID:      "remote-id-report",
				},
			},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	store.entries["report.pdf"] = JournalEntry{
		Path:         "report.pdf",
		LocalMtime:   syncedAt,
		RemoteMtime:  syncedAt,
		RemoteMD5:    "old-md5",
		RemoteID:     "remote-id-report",
		LastSyncedAt: syncedAt,
		LastOrigin:   "local",
	}

	audit := newMockAuditLogger()
	pub := newMockPublisher()

	p := NewPuller(
		PullerConfig{
			Interval:       30 * time.Second,
			LocalRoot:      localRoot,
			ConflictPolicy: PolicyNewerWins,
		},
		remote, store, audit, pub,
		slog.Default(),
		fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Remote is newer: local file should be renamed to .old.1.
	oldPath := filepath.Join(localRoot, "report.pdf.old.1")
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("expected local .old.1 file to exist: %v", err)
	}

	// Remote file should be downloaded to the original path.
	if len(remote.downloadedFiles) != 1 {
		t.Fatalf("expected 1 download, got %d", len(remote.downloadedFiles))
	}
	if remote.downloadedFiles[0].RemotePath != "report.pdf" {
		t.Errorf("downloaded wrong path: %s", remote.downloadedFiles[0].RemotePath)
	}

	// Journal should have the .old.1 entry.
	_, oldExists, _ := store.Get(context.Background(), "report.pdf.old.1")
	if !oldExists {
		t.Error("expected journal entry for report.pdf.old.1")
	}
}

func TestPuller_PageTokenPersistedAcrossRestarts(t *testing.T) {
	// Verify that the page token is persisted and a new Puller instance
	// resumes from the stored token.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{
			{
				Path: "file1.txt",
				Object: RemoteObject{
					Path: "file1.txt", Size: 10, MD5: "m1",
					ModTime: now, ID: "id-1",
				},
			},
		},
		NextPageToken: "token-after-1",
	}
	remote.changes["token-after-1"] = Changes{
		Items: []Change{
			{
				Path: "file2.txt",
				Object: RemoteObject{
					Path: "file2.txt", Size: 20, MD5: "m2",
					ModTime: now, ID: "id-2",
				},
			},
		},
		NextPageToken: "token-after-2",
	}

	// Shared store simulating persistence across restarts.
	store := newMockStore()

	// First puller instance processes first batch.
	p1 := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)
	if err := p1.PollOnce(context.Background()); err != nil {
		t.Fatalf("p1.PollOnce: %v", err)
	}

	// Verify token was persisted.
	token, _ := store.GetPageToken(context.Background())
	if token != "token-after-1" {
		t.Fatalf("after p1: token = %s, want token-after-1", token)
	}

	// Second puller instance (simulating restart) should resume from token.
	remote2 := newMockRemoteFS()
	remote2.changes["token-after-1"] = Changes{
		Items: []Change{
			{
				Path: "file2.txt",
				Object: RemoteObject{
					Path: "file2.txt", Size: 20, MD5: "m2",
					ModTime: now, ID: "id-2",
				},
			},
		},
		NextPageToken: "token-after-2",
	}

	p2 := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote2, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)
	if err := p2.PollOnce(context.Background()); err != nil {
		t.Fatalf("p2.PollOnce: %v", err)
	}

	// Verify token advanced.
	token, _ = store.GetPageToken(context.Background())
	if token != "token-after-2" {
		t.Fatalf("after p2: token = %s, want token-after-2", token)
	}

	// Verify file2.txt was downloaded by p2.
	if len(remote2.downloadedFiles) != 1 {
		t.Fatalf("expected 1 download by p2, got %d", len(remote2.downloadedFiles))
	}
	if remote2.downloadedFiles[0].RemotePath != "file2.txt" {
		t.Errorf("p2 downloaded %s, want file2.txt", remote2.downloadedFiles[0].RemotePath)
	}
}

func TestPuller_410Gone_TokenReset(t *testing.T) {
	// When ListChanges returns ErrGone, the puller should reset the token
	// and emit a warning event.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "fresh-start"
	remote.goneTokens["stale-token"] = true
	remote.changes["fresh-start"] = Changes{
		Items:         []Change{},
		NextPageToken: "fresh-start-next",
	}

	store := newMockStore()
	// Pre-set a stale token.
	_ = store.SetPageToken(context.Background(), "stale-token")

	pub := newMockPublisher()

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), pub,
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Token should be reset to the fresh one.
	token, _ := store.GetPageToken(context.Background())
	if token != "fresh-start-next" {
		t.Errorf("token = %s, want fresh-start-next", token)
	}

	// Should have emitted a pull.failure event about 410.
	failEvents := pub.getMessagesByTopic(pub.Topic("events", "pull.failure"))
	if len(failEvents) != 1 {
		t.Fatalf("expected 1 pull.failure event for 410, got %d", len(failEvents))
	}
	payload, ok := failEvents[0].Payload.(map[string]any)
	if !ok {
		t.Fatal("expected map payload")
	}
	errMsg, _ := payload["error"].(string)
	if errMsg == "" {
		t.Error("expected non-empty error in pull.failure payload")
	}
}

func TestPuller_DryRun(t *testing.T) {
	// In dry-run mode, changes are detected but not downloaded. Store is
	// not modified (no journal entry for the file).
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{
			{
				Path: "secret.txt",
				Object: RemoteObject{
					Path: "secret.txt", Size: 99, MD5: "dry-md5",
					ModTime: now, ID: "dry-id",
				},
			},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	audit := newMockAuditLogger()
	pub := newMockPublisher()

	p := NewPuller(
		PullerConfig{
			Interval:  30 * time.Second,
			LocalRoot: localRoot,
			DryRun:    true,
		},
		remote, store, audit, pub,
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// No file should be downloaded.
	if len(remote.downloadedFiles) != 0 {
		t.Errorf("expected 0 downloads in dry-run, got %d", len(remote.downloadedFiles))
	}

	// Local file should not exist.
	localPath := filepath.Join(localRoot, "secret.txt")
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Errorf("expected file to not exist in dry-run, but it does")
	}

	// Journal entry should not be created for the file.
	_, exists, _ := store.Get(context.Background(), "secret.txt")
	if exists {
		t.Error("expected no journal entry in dry-run mode")
	}

	// Audit log should record the dry-run.
	entries := audit.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if !entries[0].DryRun {
		t.Error("expected audit entry to have DryRun=true")
	}

	// Page token should still be persisted (we track position even in dry-run).
	token, _ := store.GetPageToken(context.Background())
	if token != "tok-2" {
		t.Errorf("token = %s, want tok-2", token)
	}
}

func TestPuller_LoopPrevention(t *testing.T) {
	// After pulling a file, the journal records its local mtime.
	// A subsequent "watcher event" (simulated by checking detectConflict)
	// with the same mtime should NOT be detected as a conflict.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteMtime := time.Date(2026, 4, 28, 13, 59, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{
			{
				Path: "loop-test.txt",
				Object: RemoteObject{
					Path: "loop-test.txt", Size: 50, MD5: "loop-md5",
					ModTime: remoteMtime, ID: "loop-id",
				},
			},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	audit := newMockAuditLogger()

	p := NewPuller(
		PullerConfig{
			Interval:  30 * time.Second,
			LocalRoot: localRoot,
		},
		remote, store, audit, nil,
		slog.Default(), fixedClock(now),
	)

	// First poll: downloads the file.
	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Verify journal entry was created with the local mtime.
	entry, ok, _ := store.Get(context.Background(), "loop-test.txt")
	if !ok {
		t.Fatal("journal entry not created after pull")
	}
	if entry.LastOrigin != "remote" {
		t.Errorf("LastOrigin = %s, want remote", entry.LastOrigin)
	}

	// The local file was written by the mock, so its mtime is set by the OS.
	// The journal recorded this mtime. If the watcher fires an event for
	// this file, the puller's detectConflict should see that local mtime
	// matches the journal's LocalMtime (within 1s) and NOT flag a conflict.
	localPath := filepath.Join(localRoot, "loop-test.txt")
	info, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Simulate a second remote change where MD5 is the same (no real change).
	ch := Change{
		Path: "loop-test.txt",
		Object: RemoteObject{
			Path: "loop-test.txt", Size: 50, MD5: "loop-md5",
			ModTime: remoteMtime, ID: "loop-id",
		},
	}

	isConflict, _, conflictErr := p.detectConflict(ch, entry)
	if conflictErr != nil {
		t.Fatalf("detectConflict: %v", conflictErr)
	}
	if isConflict {
		t.Error("expected no conflict: MD5 unchanged, should be loop prevention")
	}

	// Now simulate a change with different MD5 but matching local mtime.
	ch2 := Change{
		Path: "loop-test.txt",
		Object: RemoteObject{
			Path: "loop-test.txt", Size: 55, MD5: "different-md5",
			ModTime: remoteMtime.Add(time.Minute), ID: "loop-id",
		},
	}
	// Update journal to reflect the actual local mtime.
	entry.LocalMtime = info.ModTime()
	isConflict2, _, conflictErr2 := p.detectConflict(ch2, entry)
	if conflictErr2 != nil {
		t.Fatalf("detectConflict: %v", conflictErr2)
	}
	if isConflict2 {
		t.Error("expected no conflict: local mtime matches journal within tolerance")
	}
}

func TestPuller_RemoteDelete(t *testing.T) {
	// A remote deletion should remove the local file and journal entry.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	// Create the local file.
	localFile := filepath.Join(localRoot, "deleted.txt")
	writeTestFileWithMtime(t, localFile, "to be deleted", now)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{
			{Path: "deleted.txt", Deleted: true},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	store.entries["deleted.txt"] = JournalEntry{
		Path: "deleted.txt", LastOrigin: "local",
	}
	audit := newMockAuditLogger()

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, audit, nil,
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Local file should be removed.
	if _, err := os.Stat(localFile); !os.IsNotExist(err) {
		t.Error("expected local file to be deleted")
	}

	// Journal entry should be removed.
	_, exists, _ := store.Get(context.Background(), "deleted.txt")
	if exists {
		t.Error("expected journal entry to be removed")
	}

	// Audit should record the deletion.
	entries := audit.getEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].Op != "pull_delete" {
		t.Errorf("audit op = %s, want pull_delete", entries[0].Op)
	}
}

func TestPuller_DryRunDelete(t *testing.T) {
	// Dry-run delete should not remove the file or journal entry.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	localFile := filepath.Join(localRoot, "keep.txt")
	writeTestFileWithMtime(t, localFile, "should stay", now)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{
			{Path: "keep.txt", Deleted: true},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	store.entries["keep.txt"] = JournalEntry{Path: "keep.txt"}
	audit := newMockAuditLogger()

	p := NewPuller(
		PullerConfig{
			Interval:  30 * time.Second,
			LocalRoot: localRoot,
			DryRun:    true,
		},
		remote, store, audit, nil,
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// File should still exist.
	if _, err := os.Stat(localFile); err != nil {
		t.Errorf("file should still exist in dry-run: %v", err)
	}

	// Journal entry should still exist.
	_, exists, _ := store.Get(context.Background(), "keep.txt")
	if !exists {
		t.Error("journal entry should still exist in dry-run")
	}
}

func TestPuller_NoChanges(t *testing.T) {
	// When there are no changes, the puller should still persist the token.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items:         []Change{},
		NextPageToken: "start-1-next",
	}

	store := newMockStore()

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	token, _ := store.GetPageToken(context.Background())
	if token != "start-1-next" {
		t.Errorf("token = %s, want start-1-next", token)
	}
}

func TestPuller_MultipleChanges(t *testing.T) {
	// Multiple changes in a single batch should all be processed.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{
		Items: []Change{
			{
				Path:   "a.txt",
				Object: RemoteObject{Path: "a.txt", Size: 10, MD5: "m1", ModTime: now, ID: "id-a"},
			},
			{
				Path:   "b.txt",
				Object: RemoteObject{Path: "b.txt", Size: 20, MD5: "m2", ModTime: now, ID: "id-b"},
			},
			{
				Path:   "sub/c.txt",
				Object: RemoteObject{Path: "sub/c.txt", Size: 30, MD5: "m3", ModTime: now, ID: "id-c"},
			},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	if len(remote.downloadedFiles) != 3 {
		t.Fatalf("expected 3 downloads, got %d", len(remote.downloadedFiles))
	}

	// All journal entries should exist.
	for _, path := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		_, ok, _ := store.Get(context.Background(), path)
		if !ok {
			t.Errorf("missing journal entry for %s", path)
		}
	}
}

func TestPuller_RunCancelledByContext(t *testing.T) {
	// The Run loop should exit when context is cancelled.
	localRoot := t.TempDir()
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	// No changes, so polling is fast.
	remote.changes["start-1"] = Changes{
		Items:         []Change{},
		NextPageToken: "start-1",
	}

	store := newMockStore()

	p := NewPuller(
		PullerConfig{Interval: 100 * time.Millisecond, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx)
	}()

	// Let it run briefly then cancel.
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestDetermineWinner(t *testing.T) {
	earlier := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	later := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		localTime  time.Time
		remoteTime time.Time
		policy     ConflictPolicy
		want       string
	}{
		{
			name:       "NewerWins_LocalNewer",
			localTime:  later,
			remoteTime: earlier,
			policy:     PolicyNewerWins,
			want:       "local",
		},
		{
			name:       "NewerWins_RemoteNewer",
			localTime:  earlier,
			remoteTime: later,
			policy:     PolicyNewerWins,
			want:       "remote",
		},
		{
			name:       "NewerWins_EqualMtime",
			localTime:  later,
			remoteTime: later,
			policy:     PolicyNewerWins,
			want:       "local", // default for equal mtime
		},
		{
			name:       "LocalWins_Always",
			localTime:  earlier,
			remoteTime: later,
			policy:     PolicyLocalWins,
			want:       "local",
		},
		{
			name:       "RemoteWins_Always",
			localTime:  later,
			remoteTime: earlier,
			policy:     PolicyRemoteWins,
			want:       "remote",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := determineWinner(tc.localTime, tc.remoteTime, tc.policy)
			if got != tc.want {
				t.Errorf("determineWinner() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPuller_OldNCollision(t *testing.T) {
	// When .old.1 already exists in the journal, the next conflict should
	// produce .old.2.
	localRoot := t.TempDir()
	syncedAt := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	localModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	writeTestFileWithMtime(t,
		filepath.Join(localRoot, "data.csv"),
		"local data",
		localModTime,
	)

	remote := newMockRemoteFS()
	remote.startToken = "tok-1"
	remote.changes["tok-1"] = Changes{
		Items: []Change{
			{
				Path: "data.csv",
				Object: RemoteObject{
					Path: "data.csv", Size: 100, MD5: "new-md5",
					ModTime: remoteModTime, ID: "id-data",
				},
			},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	store.entries["data.csv"] = JournalEntry{
		Path: "data.csv", LocalMtime: syncedAt, RemoteMtime: syncedAt,
		RemoteMD5: "old-md5", LastSyncedAt: syncedAt,
	}
	// Pre-fill .old.1 to force .old.2.
	store.entries["data.csv.old.1"] = JournalEntry{Path: "data.csv.old.1"}

	p := NewPuller(
		PullerConfig{
			Interval:       30 * time.Second,
			LocalRoot:      localRoot,
			ConflictPolicy: PolicyNewerWins,
		},
		remote, store, newMockAuditLogger(), newMockPublisher(),
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Local wins (newer), remote should be moved to .old.2 (not .old.1).
	if len(remote.movedFiles) != 1 {
		t.Fatalf("expected 1 MoveFile call, got %d", len(remote.movedFiles))
	}
	mv := remote.movedFiles[0]
	if mv.Dst != "data.csv.old.2" {
		t.Errorf("MoveFile dst = %s, want data.csv.old.2", mv.Dst)
	}
}

func TestPuller_DryRunConflict(t *testing.T) {
	// Dry-run conflict: detected but not resolved (no file operations).
	localRoot := t.TempDir()
	syncedAt := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	localModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	writeTestFileWithMtime(t,
		filepath.Join(localRoot, "doc.txt"),
		"local doc",
		localModTime,
	)

	remote := newMockRemoteFS()
	remote.startToken = "tok-1"
	remote.changes["tok-1"] = Changes{
		Items: []Change{
			{
				Path: "doc.txt",
				Object: RemoteObject{
					Path: "doc.txt", Size: 100, MD5: "new-md5",
					ModTime: remoteModTime, ID: "id-doc",
				},
			},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	store.entries["doc.txt"] = JournalEntry{
		Path: "doc.txt", LocalMtime: syncedAt, RemoteMtime: syncedAt,
		RemoteMD5: "old-md5", LastSyncedAt: syncedAt,
	}
	audit := newMockAuditLogger()

	p := NewPuller(
		PullerConfig{
			Interval:  30 * time.Second,
			LocalRoot: localRoot,
			DryRun:    true,
		},
		remote, store, audit, newMockPublisher(),
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// No remote moves or downloads should happen.
	if len(remote.movedFiles) != 0 {
		t.Errorf("expected 0 MoveFile in dry-run, got %d", len(remote.movedFiles))
	}
	if len(remote.downloadedFiles) != 0 {
		t.Errorf("expected 0 downloads in dry-run, got %d", len(remote.downloadedFiles))
	}

	// Audit should show dry-run conflict.
	entries := audit.getEntries()
	found := false
	for _, e := range entries {
		if e.Op == "conflict" && e.DryRun {
			found = true
		}
	}
	if !found {
		t.Error("expected dry-run conflict audit entry")
	}
}

func TestPuller_FileDoesNotExistLocally(t *testing.T) {
	// When the journal has an entry but the local file was deleted,
	// downloading the remote file should not trigger a conflict.
	localRoot := t.TempDir()
	syncedAt := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	remote.startToken = "tok-1"
	remote.changes["tok-1"] = Changes{
		Items: []Change{
			{
				Path: "gone-local.txt",
				Object: RemoteObject{
					Path: "gone-local.txt", Size: 30, MD5: "new-md5",
					ModTime: now, ID: "id-gone",
				},
			},
		},
		NextPageToken: "tok-2",
	}

	store := newMockStore()
	store.entries["gone-local.txt"] = JournalEntry{
		Path: "gone-local.txt", LocalMtime: syncedAt, RemoteMD5: "old-md5",
	}

	p := NewPuller(
		PullerConfig{Interval: 30 * time.Second, LocalRoot: localRoot},
		remote, store, newMockAuditLogger(), nil,
		slog.Default(), fixedClock(now),
	)

	err := p.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}

	// Should download without conflict.
	if len(remote.downloadedFiles) != 1 {
		t.Fatalf("expected 1 download, got %d", len(remote.downloadedFiles))
	}
	if len(remote.movedFiles) != 0 {
		t.Errorf("expected no MoveFile (no conflict), got %d", len(remote.movedFiles))
	}
}
