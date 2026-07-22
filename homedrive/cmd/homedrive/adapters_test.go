package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

func newTestJournal(t *testing.T) *store.Journal {
	t.Helper()
	dir := t.TempDir()
	j, err := store.OpenJournal(filepath.Join(dir, "state.db"), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func TestWatcherStoreAdapter_GetSyncRecord_Cases(t *testing.T) {
	j := newTestJournal(t)
	dir := t.TempDir()

	cases := []struct {
		name       string
		setup      func(t *testing.T) string // returns path
		wantRecord bool
	}{
		{
			name: "no journal entry returns nil",
			setup: func(t *testing.T) string {
				path := filepath.Join(dir, "no-entry.txt")
				if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantRecord: false,
		},
		{
			name: "entry but file missing returns nil",
			setup: func(t *testing.T) string {
				path := filepath.Join(dir, "missing.txt")
				if err := j.Put(store.JournalEntry{Path: path, LocalMtime: time.Now()}); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantRecord: false,
		},
		{
			name: "entry and file present returns record with live size",
			setup: func(t *testing.T) string {
				path := filepath.Join(dir, "present.txt")
				if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
					t.Fatal(err)
				}
				mtime := time.Now().Add(-time.Hour)
				if err := j.Put(store.JournalEntry{Path: path, LocalMtime: mtime}); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantRecord: true,
		},
	}

	adapter := &watcherStoreAdapter{j: j}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			rec := adapter.GetSyncRecord(path)
			if tc.wantRecord && rec == nil {
				t.Fatal("expected a sync record, got nil")
			}
			if !tc.wantRecord && rec != nil {
				t.Fatalf("expected nil sync record, got %+v", rec)
			}
			if rec != nil {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				// Size.term is filled from a live stat, per the adapter's
				// documented mtime-only-guard rationale.
				if rec.Size != info.Size() {
					t.Errorf("expected Size=%d (live stat), got %d", info.Size(), rec.Size)
				}
			}
		})
	}
}

func TestBisyncJournalAdapter_RoundTrip(t *testing.T) {
	j := newTestJournal(t)
	adapter := &bisyncJournalAdapter{j: j}

	if adapter.Exists("foo.txt") {
		t.Error("expected foo.txt to not exist yet")
	}

	entry := syncer.JournalEntry{
		Path:         "foo.txt",
		LocalMtime:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		RemoteMD5:    "abc123",
		LastSyncedAt: time.Now(),
		LastOrigin:   "local",
	}
	if err := adapter.Put(entry); err != nil {
		t.Fatalf("put: %v", err)
	}

	if !adapter.Exists("foo.txt") {
		t.Error("expected foo.txt to exist after Put")
	}

	got, err := adapter.Get("foo.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected a non-nil entry")
	}
	if got.RemoteMD5 != "abc123" || got.LastOrigin != "local" {
		t.Errorf("unexpected entry: %+v", got)
	}
	if !got.LocalMtime.Equal(entry.LocalMtime) {
		t.Errorf("expected LocalMtime %v, got %v", entry.LocalMtime, got.LocalMtime)
	}
}

func TestBisyncJournalAdapter_GetMissing_ReturnsError(t *testing.T) {
	j := newTestJournal(t)
	adapter := &bisyncJournalAdapter{j: j}

	_, err := adapter.Get("does-not-exist.txt")
	if err == nil {
		t.Fatal("expected an error for a missing entry")
	}
}

// TestRcloneSyncerAdapter_DelegatesToRemoteFS covers every thin
// pass-through method on rcloneSyncerAdapter against fakeRemoteFS.
func TestRcloneSyncerAdapter_DelegatesToRemoteFS(t *testing.T) {
	remote := newFakeRemoteFS()
	adapter := &rcloneSyncerAdapter{fs: remote}
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := adapter.CopyFile(ctx, src, "dst"); err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if _, err := adapter.Stat(ctx, src); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if _, err := adapter.List(ctx, ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := adapter.ListChanges(ctx, ""); err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	changesWithObject := &fakeRemoteFSWithChanges{fakeRemoteFS: remote}
	adapterWithChanges := &rcloneSyncerAdapter{fs: changesWithObject}
	changes, err := adapterWithChanges.ListChanges(ctx, "")
	if err != nil {
		t.Fatalf("ListChanges with object: %v", err)
	}
	if len(changes.Items) != 1 || changes.Items[0].Object.Path != "obj.txt" {
		t.Errorf("unexpected changes: %+v", changes)
	}
	if _, err := adapter.GetStartPageToken(ctx); err != nil {
		t.Fatalf("GetStartPageToken: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "downloaded.txt")
	if err := adapter.DownloadFile(ctx, "remote/path.txt", dst); err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "content-of-remote/path.txt" {
		t.Errorf("unexpected downloaded content: %q, err=%v", data, err)
	}
	if err := adapter.MoveFile(ctx, src, "moved"); err != nil {
		t.Fatalf("MoveFile: %v", err)
	}
	if err := adapter.DeleteFile(ctx, "moved"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
}

// TestJournalSyncerAdapter_DelegatesToJournal covers every method on
// journalSyncerAdapter against a real store.Journal.
func TestJournalSyncerAdapter_DelegatesToJournal(t *testing.T) {
	j := newTestJournal(t)
	adapter := &journalSyncerAdapter{j: j, logger: slog.Default()}
	ctx := context.Background()

	if err := adapter.SetPageToken(ctx, "tok-1"); err != nil {
		t.Fatalf("SetPageToken: %v", err)
	}
	got, err := adapter.GetPageToken(ctx)
	if err != nil || got != "tok-1" {
		t.Fatalf("GetPageToken = %q, %v", got, err)
	}

	if _, found, err := adapter.Get(ctx, "missing.txt"); err != nil || found {
		t.Fatalf("expected not-found for missing.txt, got found=%v err=%v", found, err)
	}

	entry := syncer.JournalEntry{Path: "dir/old.txt", LastOrigin: "local"}
	if err := adapter.Put(ctx, entry); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got2, found, err := adapter.Get(ctx, "dir/old.txt")
	if err != nil || !found || got2.LastOrigin != "local" {
		t.Fatalf("Get after Put = %+v, found=%v, err=%v", got2, found, err)
	}

	n, err := adapter.NextOldN(ctx, "dir/old.txt")
	if err != nil || n != 1 {
		t.Fatalf("NextOldN = %d, %v", n, err)
	}

	if err := adapter.Put(ctx, syncer.JournalEntry{Path: "dir/child.txt"}); err != nil {
		t.Fatal(err)
	}
	count, err := adapter.RewritePrefix(ctx, "dir/", "dir2/")
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}
	if count == 0 {
		t.Error("expected RewritePrefix to move at least one entry")
	}

	if err := adapter.Delete(ctx, "dir2/old.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestAuditLoggerAdapter_Log(t *testing.T) {
	var buf bytes.Buffer
	adapter := &auditLoggerAdapter{a: store.NewAuditor(&buf, slog.Default())}

	if err := adapter.Log(syncer.AuditEntry{Op: "push", Path: "a.txt"}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected the audit log to have written a line")
	}
}

func TestNoopPublisher_TopicAndPublish(t *testing.T) {
	var p noopPublisher
	if err := p.PublishJSON("ignored", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("PublishJSON: %v", err)
	}
	if got, want := p.Topic("events", "push.success"), "events/push.success"; got != want {
		t.Errorf("Topic() = %q, want %q", got, want)
	}
}
