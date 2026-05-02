package store

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestJournal(t *testing.T) *Journal {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	j, err := OpenJournal(dbPath, logger)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func TestJournal_PutAndGet(t *testing.T) {
	j := newTestJournal(t)
	now := time.Date(2026, 4, 28, 14, 0, 0, 0, time.UTC)

	entry := JournalEntry{
		Path:         "docs/notes.md",
		LocalMtime:   now,
		RemoteMtime:  now.Add(-10 * time.Second),
		RemoteMD5:    "abc123",
		RemoteID:     "drive-id-1",
		LastSyncedAt: now,
		LastOrigin:   "local",
	}

	if err := j.Put(entry); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := j.Get("docs/notes.md")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Path != entry.Path {
		t.Errorf("Path = %q, want %q", got.Path, entry.Path)
	}
	if got.RemoteMD5 != entry.RemoteMD5 {
		t.Errorf("RemoteMD5 = %q, want %q", got.RemoteMD5, entry.RemoteMD5)
	}
	if got.RemoteID != entry.RemoteID {
		t.Errorf("RemoteID = %q, want %q", got.RemoteID, entry.RemoteID)
	}
	if got.LastOrigin != entry.LastOrigin {
		t.Errorf("LastOrigin = %q, want %q", got.LastOrigin, entry.LastOrigin)
	}
}

func TestJournal_GetNotFound(t *testing.T) {
	j := newTestJournal(t)

	_, err := j.Get("nonexistent")
	if err != ErrNotFound {
		t.Errorf("Get non-existent: got %v, want ErrNotFound", err)
	}
}

func TestJournal_Exists(t *testing.T) {
	j := newTestJournal(t)

	if j.Exists("missing") {
		t.Error("Exists returned true for missing key")
	}

	entry := JournalEntry{Path: "present.txt", LastOrigin: "local"}
	if err := j.Put(entry); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if !j.Exists("present.txt") {
		t.Error("Exists returned false for existing key")
	}
}

func TestJournal_Delete(t *testing.T) {
	j := newTestJournal(t)

	entry := JournalEntry{Path: "delete-me.txt", LastOrigin: "local"}
	if err := j.Put(entry); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := j.Delete("delete-me.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if j.Exists("delete-me.txt") {
		t.Error("entry still exists after Delete")
	}
}

func TestJournal_ListByPrefix(t *testing.T) {
	j := newTestJournal(t)

	paths := []string{
		"photos/2026/jan/img1.jpg",
		"photos/2026/jan/img2.jpg",
		"photos/2026/feb/img3.jpg",
		"docs/readme.md",
	}
	for _, p := range paths {
		if err := j.Put(JournalEntry{Path: p, LastOrigin: "local"}); err != nil {
			t.Fatalf("Put %q: %v", p, err)
		}
	}

	tests := []struct {
		name   string
		prefix string
		want   int
	}{
		{name: "all photos", prefix: "photos/", want: 3},
		{name: "january only", prefix: "photos/2026/jan/", want: 2},
		{name: "docs", prefix: "docs/", want: 1},
		{name: "no match", prefix: "videos/", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := j.ListByPrefix(tc.prefix)
			if err != nil {
				t.Fatalf("ListByPrefix(%q): %v", tc.prefix, err)
			}
			if len(entries) != tc.want {
				t.Errorf("ListByPrefix(%q) returned %d entries, want %d",
					tc.prefix, len(entries), tc.want)
			}
		})
	}
}

func TestJournal_Count(t *testing.T) {
	j := newTestJournal(t)

	count, err := j.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Errorf("Count = %d, want 0 for empty journal", count)
	}

	for i := 0; i < 5; i++ {
		entry := JournalEntry{Path: filepath.Join("dir", string(rune('a'+i))+".txt"), LastOrigin: "local"}
		if err := j.Put(entry); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	count, err = j.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 5 {
		t.Errorf("Count = %d, want 5", count)
	}
}

func TestJournal_PutOverwrites(t *testing.T) {
	j := newTestJournal(t)

	entry := JournalEntry{
		Path:      "overwrite.txt",
		RemoteMD5: "first",
	}
	if err := j.Put(entry); err != nil {
		t.Fatalf("Put first: %v", err)
	}

	entry.RemoteMD5 = "second"
	if err := j.Put(entry); err != nil {
		t.Fatalf("Put second: %v", err)
	}

	got, err := j.Get("overwrite.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RemoteMD5 != "second" {
		t.Errorf("RemoteMD5 = %q, want %q", got.RemoteMD5, "second")
	}
}
