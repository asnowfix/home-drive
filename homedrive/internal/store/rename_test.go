package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestJournalWithAuditor(t *testing.T) (*Journal, *Auditor, *bytes.Buffer) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	j, err := OpenJournal(dbPath, logger)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { j.Close() })

	var auditBuf bytes.Buffer
	auditor := NewAuditor(&auditBuf, logger)

	return j, auditor, &auditBuf
}

func TestRewritePrefix_BasicRename(t *testing.T) {
	j, auditor, _ := newTestJournalWithAuditor(t)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	// Seed entries under "photos/2026/".
	for i := 0; i < 10; i++ {
		entry := JournalEntry{
			Path:        fmt.Sprintf("photos/2026/img_%03d.jpg", i),
			LocalMtime:  now,
			RemoteMD5:   fmt.Sprintf("md5-%d", i),
			LastOrigin:  "local",
		}
		if err := j.Put(entry); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	count, err := RewritePrefix(j, "photos/2026/", "archive/photos/2026/", auditor, logger)
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}

	if count != 10 {
		t.Errorf("count = %d, want 10", count)
	}

	// Old prefix should be empty.
	old, err := j.ListByPrefix("photos/2026/")
	if err != nil {
		t.Fatalf("ListByPrefix old: %v", err)
	}
	if len(old) != 0 {
		t.Errorf("old prefix still has %d entries, want 0", len(old))
	}

	// New prefix should have all entries.
	newEntries, err := j.ListByPrefix("archive/photos/2026/")
	if err != nil {
		t.Fatalf("ListByPrefix new: %v", err)
	}
	if len(newEntries) != 10 {
		t.Errorf("new prefix has %d entries, want 10", len(newEntries))
	}

	// Verify path was rewritten in the entry data.
	for _, e := range newEntries {
		if !strings.HasPrefix(e.Path, "archive/photos/2026/") {
			t.Errorf("entry Path = %q, want prefix 'archive/photos/2026/'", e.Path)
		}
	}
}

func TestRewritePrefix_LargeRename(t *testing.T) {
	j, auditor, _ := newTestJournalWithAuditor(t)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	// Seed 150 entries to exceed the 100+ requirement.
	const numEntries = 150
	for i := 0; i < numEntries; i++ {
		entry := JournalEntry{
			Path:         fmt.Sprintf("big_dir/subdir/file_%05d.dat", i),
			LocalMtime:   now,
			RemoteMtime:  now,
			RemoteMD5:    fmt.Sprintf("hash-%d", i),
			RemoteID:     fmt.Sprintf("id-%d", i),
			LastSyncedAt: now,
			LastOrigin:   "remote",
		}
		if err := j.Put(entry); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	count, err := RewritePrefix(j, "big_dir/", "renamed_big_dir/", auditor, logger)
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}

	if count != numEntries {
		t.Errorf("count = %d, want %d", count, numEntries)
	}

	// Verify old prefix is gone.
	oldEntries, err := j.ListByPrefix("big_dir/")
	if err != nil {
		t.Fatalf("ListByPrefix old: %v", err)
	}
	if len(oldEntries) != 0 {
		t.Errorf("old prefix has %d entries, want 0", len(oldEntries))
	}

	// Verify new prefix has all entries.
	newEntries, err := j.ListByPrefix("renamed_big_dir/")
	if err != nil {
		t.Fatalf("ListByPrefix new: %v", err)
	}
	if len(newEntries) != numEntries {
		t.Errorf("new prefix has %d entries, want %d", len(newEntries), numEntries)
	}

	// Spot-check that metadata is preserved.
	entry, err := j.Get("renamed_big_dir/subdir/file_00042.dat")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.RemoteMD5 != "hash-42" {
		t.Errorf("RemoteMD5 = %q, want %q", entry.RemoteMD5, "hash-42")
	}
	if entry.RemoteID != "id-42" {
		t.Errorf("RemoteID = %q, want %q", entry.RemoteID, "id-42")
	}
	if entry.LastOrigin != "remote" {
		t.Errorf("LastOrigin = %q, want %q", entry.LastOrigin, "remote")
	}
}

func TestRewritePrefix_SamePrefix(t *testing.T) {
	j, auditor, _ := newTestJournalWithAuditor(t)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	count, err := RewritePrefix(j, "same/", "same/", auditor, logger)
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 for same prefix", count)
	}
}

func TestRewritePrefix_NoMatch(t *testing.T) {
	j, auditor, _ := newTestJournalWithAuditor(t)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// Seed entries under a different prefix.
	if err := j.Put(JournalEntry{Path: "other/file.txt", LastOrigin: "local"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	count, err := RewritePrefix(j, "nonexistent/", "target/", auditor, logger)
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 for unmatched prefix", count)
	}

	// Original entry untouched.
	if !j.Exists("other/file.txt") {
		t.Error("original entry should still exist")
	}
}

func TestRewritePrefix_DoesNotAffectSiblings(t *testing.T) {
	j, auditor, _ := newTestJournalWithAuditor(t)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// "photos/" and "photos_backup/" are siblings, not nested.
	if err := j.Put(JournalEntry{Path: "photos/a.jpg", LastOrigin: "local"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := j.Put(JournalEntry{Path: "photos_backup/b.jpg", LastOrigin: "local"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	count, err := RewritePrefix(j, "photos/", "renamed/", auditor, logger)
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}

	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// photos_backup should be untouched.
	if !j.Exists("photos_backup/b.jpg") {
		t.Error("sibling 'photos_backup/b.jpg' should not be affected")
	}
}

func TestRewritePrefix_AuditLogEntry(t *testing.T) {
	j, auditor, auditBuf := newTestJournalWithAuditor(t)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	if err := j.Put(JournalEntry{Path: "src/main.go", LastOrigin: "local"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := RewritePrefix(j, "src/", "pkg/", auditor, logger)
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}

	var entry AuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(auditBuf.String())), &entry); err != nil {
		t.Fatalf("parse audit: %v\nraw: %s", err, auditBuf.String())
	}

	if entry.Op != "dir_rename" {
		t.Errorf("audit Op = %q, want %q", entry.Op, "dir_rename")
	}
	if entry.Path != "src/" {
		t.Errorf("audit Path = %q, want %q", entry.Path, "src/")
	}
	if entry.NewPath != "pkg/" {
		t.Errorf("audit NewPath = %q, want %q", entry.NewPath, "pkg/")
	}
	if entry.FilesCount != 1 {
		t.Errorf("audit FilesCount = %d, want 1", entry.FilesCount)
	}
}
