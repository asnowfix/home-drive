package syncer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveConflict_LocalNewer(t *testing.T) {
	// When local mtime > remote mtime, local wins: remote is moved to .old.1
	// and local file is uploaded.
	localRoot := t.TempDir()
	localModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	writeTestFileWithMtime(t,
		filepath.Join(localRoot, "file.txt"),
		"local content",
		localModTime,
	)

	remote := newMockRemoteFS()
	store := newMockStore()
	pub := newMockPublisher()

	ch := Change{
		Path: "file.txt",
		Object: RemoteObject{
			Path: "file.txt", Size: 100, MD5: "remote-md5",
			ModTime: remoteModTime, ID: "remote-id",
		},
	}
	journal := JournalEntry{Path: "file.txt"}

	result, err := resolveConflict(
		context.Background(), slog.Default(), store, remote, pub,
		localRoot, ch, journal, localModTime,
		PolicyNewerWins, false, fixedClock(now),
	)
	if err != nil {
		t.Fatalf("resolveConflict: %v", err)
	}

	if result.Winner != "local" {
		t.Errorf("Winner = %s, want local", result.Winner)
	}
	if result.OldPath != "file.txt.old.1" {
		t.Errorf("OldPath = %s, want file.txt.old.1", result.OldPath)
	}
	if result.Resolution != "newer_wins:local" {
		t.Errorf("Resolution = %s, want newer_wins:local", result.Resolution)
	}

	// Remote file should be moved.
	if len(remote.movedFiles) != 1 {
		t.Fatalf("expected 1 MoveFile, got %d", len(remote.movedFiles))
	}
	if remote.movedFiles[0].Dst != "file.txt.old.1" {
		t.Errorf("moved to %s, want file.txt.old.1", remote.movedFiles[0].Dst)
	}

	// Local file should be uploaded (CopyFile).
	if len(remote.copiedFiles) != 1 {
		t.Fatalf("expected 1 CopyFile, got %d", len(remote.copiedFiles))
	}
}

func TestResolveConflict_RemoteNewer(t *testing.T) {
	// When remote mtime > local mtime, remote wins: local is renamed to
	// .old.1 and remote version is downloaded.
	localRoot := t.TempDir()
	localModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	localFile := filepath.Join(localRoot, "file.txt")
	writeTestFileWithMtime(t, localFile, "old local", localModTime)

	remote := newMockRemoteFS()
	store := newMockStore()
	pub := newMockPublisher()

	ch := Change{
		Path: "file.txt",
		Object: RemoteObject{
			Path: "file.txt", Size: 200, MD5: "new-remote",
			ModTime: remoteModTime, ID: "remote-id",
		},
	}
	journal := JournalEntry{Path: "file.txt"}

	result, err := resolveConflict(
		context.Background(), slog.Default(), store, remote, pub,
		localRoot, ch, journal, localModTime,
		PolicyNewerWins, false, fixedClock(now),
	)
	if err != nil {
		t.Fatalf("resolveConflict: %v", err)
	}

	if result.Winner != "remote" {
		t.Errorf("Winner = %s, want remote", result.Winner)
	}

	// Local file should be renamed to .old.1.
	oldPath := filepath.Join(localRoot, "file.txt.old.1")
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("expected .old.1 to exist: %v", err)
	}

	// Remote file should be downloaded to the original path.
	if len(remote.downloadedFiles) != 1 {
		t.Fatalf("expected 1 download, got %d", len(remote.downloadedFiles))
	}
	if remote.downloadedFiles[0].LocalPath != localFile {
		t.Errorf("downloaded to %s, want %s", remote.downloadedFiles[0].LocalPath, localFile)
	}
}

func TestResolveConflict_EqualMtime_DefaultLocalWins(t *testing.T) {
	// Equal mtime with different checksums: default to local wins.
	localRoot := t.TempDir()
	mtime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	writeTestFileWithMtime(t,
		filepath.Join(localRoot, "same.txt"),
		"local version",
		mtime,
	)

	remote := newMockRemoteFS()
	store := newMockStore()

	ch := Change{
		Path: "same.txt",
		Object: RemoteObject{
			Path: "same.txt", Size: 100, MD5: "different-md5",
			ModTime: mtime, ID: "id",
		},
	}

	result, err := resolveConflict(
		context.Background(), slog.Default(), store, remote, nil,
		localRoot, ch, JournalEntry{}, mtime,
		PolicyNewerWins, false, fixedClock(now),
	)
	if err != nil {
		t.Fatalf("resolveConflict: %v", err)
	}

	if result.Winner != "local" {
		t.Errorf("Winner = %s, want local (default for equal mtime)", result.Winner)
	}
}

func TestResolveConflict_EqualMtime_RemoteWinsPolicy(t *testing.T) {
	// With PolicyRemoteWins, remote should win even if mtimes are equal.
	localRoot := t.TempDir()
	mtime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	localFile := filepath.Join(localRoot, "policy.txt")
	writeTestFileWithMtime(t, localFile, "local", mtime)

	remote := newMockRemoteFS()
	store := newMockStore()

	ch := Change{
		Path: "policy.txt",
		Object: RemoteObject{
			Path: "policy.txt", Size: 100, MD5: "diff",
			ModTime: mtime, ID: "id",
		},
	}

	result, err := resolveConflict(
		context.Background(), slog.Default(), store, remote, nil,
		localRoot, ch, JournalEntry{}, mtime,
		PolicyRemoteWins, false, fixedClock(now),
	)
	if err != nil {
		t.Fatalf("resolveConflict: %v", err)
	}

	if result.Winner != "remote" {
		t.Errorf("Winner = %s, want remote (PolicyRemoteWins)", result.Winner)
	}

	// Local file should be renamed.
	oldPath := filepath.Join(localRoot, "policy.txt.old.1")
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("expected .old.1 to exist: %v", err)
	}
}

func TestResolveConflict_DryRun(t *testing.T) {
	// In dry-run mode, no files should be moved or downloaded.
	localRoot := t.TempDir()
	localModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	remote := newMockRemoteFS()
	store := newMockStore()

	ch := Change{
		Path: "dry.txt",
		Object: RemoteObject{
			Path: "dry.txt", ModTime: remoteModTime, ID: "id",
		},
	}

	result, err := resolveConflict(
		context.Background(), slog.Default(), store, remote, nil,
		localRoot, ch, JournalEntry{}, localModTime,
		PolicyNewerWins, true, fixedClock(now),
	)
	if err != nil {
		t.Fatalf("resolveConflict: %v", err)
	}

	if result.Winner != "local" {
		t.Errorf("Winner = %s, want local", result.Winner)
	}
	if result.Resolution != "newer_wins:local" {
		t.Errorf("Resolution = %s, want newer_wins:local", result.Resolution)
	}

	// No file operations.
	if len(remote.movedFiles) != 0 {
		t.Errorf("expected 0 moves in dry-run, got %d", len(remote.movedFiles))
	}
	if len(remote.copiedFiles) != 0 {
		t.Errorf("expected 0 copies in dry-run, got %d", len(remote.copiedFiles))
	}
}

func TestResolveConflict_MQTTEvents(t *testing.T) {
	// Verify that conflict.detected and conflict.resolved MQTT events
	// are emitted with correct payloads.
	localRoot := t.TempDir()
	localModTime := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)
	remoteModTime := time.Date(2026, 4, 28, 13, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)

	writeTestFileWithMtime(t,
		filepath.Join(localRoot, "mqtt-test.txt"),
		"content",
		localModTime,
	)

	remote := newMockRemoteFS()
	store := newMockStore()
	pub := newMockPublisher()

	ch := Change{
		Path: "mqtt-test.txt",
		Object: RemoteObject{
			Path: "mqtt-test.txt", MD5: "m", ModTime: remoteModTime, ID: "id",
		},
	}

	_, err := resolveConflict(
		context.Background(), slog.Default(), store, remote, pub,
		localRoot, ch, JournalEntry{}, localModTime,
		PolicyNewerWins, false, fixedClock(now),
	)
	if err != nil {
		t.Fatalf("resolveConflict: %v", err)
	}

	// Check conflict.detected event.
	detected := pub.getMessagesByTopic(pub.Topic("events", "conflict.detected"))
	if len(detected) != 1 {
		t.Fatalf("expected 1 conflict.detected, got %d", len(detected))
	}
	dp, ok := detected[0].Payload.(map[string]any)
	if !ok {
		t.Fatal("expected map payload for conflict.detected")
	}
	if dp["type"] != "conflict.detected" {
		t.Errorf("type = %v, want conflict.detected", dp["type"])
	}
	if dp["path"] != "mqtt-test.txt" {
		t.Errorf("path = %v, want mqtt-test.txt", dp["path"])
	}

	// Check conflict.resolved event.
	resolved := pub.getMessagesByTopic(pub.Topic("events", "conflict.resolved"))
	if len(resolved) != 1 {
		t.Fatalf("expected 1 conflict.resolved, got %d", len(resolved))
	}
	rp, ok := resolved[0].Payload.(map[string]any)
	if !ok {
		t.Fatal("expected map payload for conflict.resolved")
	}
	if rp["resolution"] != "newer_wins:local" {
		t.Errorf("resolution = %v, want newer_wins:local", rp["resolution"])
	}
	if rp["kept_old_as"] != "mqtt-test.txt.old.1" {
		t.Errorf("kept_old_as = %v, want mqtt-test.txt.old.1", rp["kept_old_as"])
	}
}
