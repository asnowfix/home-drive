package store

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

func newTestJournal(t *testing.T) *Journal {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	j, err := OpenJournal(dbPath, logger)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
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

func TestJournal_NextOldN_CollapsesSuffix(t *testing.T) {
	j := newTestJournal(t)

	// Regression for issue #65: a repeat conflict on an already-suffixed
	// path must collapse onto the tracked base, not nest a new suffix.
	for _, p := range []string{"f.md", "f.md.old.1"} {
		if err := j.Put(JournalEntry{Path: p, LastOrigin: "local"}); err != nil {
			t.Fatalf("Put %q: %v", p, err)
		}
	}

	base, n := j.NextOldN(nil, "f.md.old.1")
	if base != "f.md" || n != 2 {
		t.Errorf("NextOldN(f.md.old.1) = (%q, %d), want (\"f.md\", 2)", base, n)
	}
}

func TestJournal_NextOldN_DoesNotCollapseUnknownBase(t *testing.T) {
	j := newTestJournal(t)

	// "budget" itself was never tracked, so "budget.old.2" is a user
	// file that merely looks like a conflict artifact and must keep its
	// own numbering space.
	if err := j.Put(JournalEntry{Path: "budget.old.2", LastOrigin: "local"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	base, n := j.NextOldN(nil, "budget.old.2")
	if base != "budget.old.2" || n != 1 {
		t.Errorf("NextOldN(budget.old.2) = (%q, %d), want (\"budget.old.2\", 1)", base, n)
	}
}

func TestJournal_NextOldN_CustomMatcher(t *testing.T) {
	j := newTestJournal(t)
	m, err := oldsuffix.New(".conflict-%d")
	if err != nil {
		t.Fatalf("oldsuffix.New: %v", err)
	}

	for _, p := range []string{"notes.md", "notes.md.conflict-1"} {
		if err := j.Put(JournalEntry{Path: p, LastOrigin: "local"}); err != nil {
			t.Fatalf("Put %q: %v", p, err)
		}
	}

	base, n := j.NextOldN(m, "notes.md.conflict-1")
	if base != "notes.md" || n != 2 {
		t.Errorf("NextOldN = (%q, %d), want (\"notes.md\", 2)", base, n)
	}
}

func TestJournal_ForEach_Cases(t *testing.T) {
	t.Run("empty journal", func(t *testing.T) {
		j := newTestJournal(t)
		count := 0
		if err := j.ForEach(func(JournalEntry) error {
			count++
			return nil
		}); err != nil {
			t.Fatalf("ForEach: %v", err)
		}
		if count != 0 {
			t.Errorf("count = %d, want 0", count)
		}
	})

	t.Run("visits every entry in key order", func(t *testing.T) {
		j := newTestJournal(t)
		paths := []string{"b.txt", "a.txt", "c.txt"}
		for _, p := range paths {
			if err := j.Put(JournalEntry{Path: p, LastOrigin: "local"}); err != nil {
				t.Fatalf("Put %q: %v", p, err)
			}
		}

		var seen []string
		if err := j.ForEach(func(e JournalEntry) error {
			seen = append(seen, e.Path)
			return nil
		}); err != nil {
			t.Fatalf("ForEach: %v", err)
		}

		want := []string{"a.txt", "b.txt", "c.txt"} // bbolt cursor order = key order
		if len(seen) != len(want) {
			t.Fatalf("seen = %v, want %v", seen, want)
		}
		for i := range want {
			if seen[i] != want[i] {
				t.Errorf("seen[%d] = %q, want %q", i, seen[i], want[i])
			}
		}
	})

	t.Run("aborts on fn error", func(t *testing.T) {
		j := newTestJournal(t)
		for _, p := range []string{"a.txt", "b.txt"} {
			if err := j.Put(JournalEntry{Path: p, LastOrigin: "local"}); err != nil {
				t.Fatalf("Put %q: %v", p, err)
			}
		}

		errStop := errors.New("stop")
		calls := 0
		err := j.ForEach(func(JournalEntry) error {
			calls++
			return errStop
		})
		if !errors.Is(err, errStop) {
			t.Fatalf("ForEach error = %v, want errStop", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 (should abort on first error)", calls)
		}
	})
}

func TestJournal_MetaRoundTrip(t *testing.T) {
	j := newTestJournal(t)

	// A key that has never been set returns an empty string, no error.
	val, err := j.GetMeta([]byte("unset-key"))
	if err != nil || val != "" {
		t.Fatalf("GetMeta(unset) = %q, %v, want \"\", nil", val, err)
	}

	if err := j.SetMeta([]byte("k"), "v1"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	val, err = j.GetMeta([]byte("k"))
	if err != nil || val != "v1" {
		t.Fatalf("GetMeta(k) = %q, %v, want \"v1\", nil", val, err)
	}

	// GetPageToken/SetPageToken must still work now that they are thin
	// wrappers over GetMeta/SetMeta.
	if err := j.SetPageToken("tok-123"); err != nil {
		t.Fatalf("SetPageToken: %v", err)
	}
	tok, err := j.GetPageToken()
	if err != nil || tok != "tok-123" {
		t.Fatalf("GetPageToken = %q, %v, want \"tok-123\", nil", tok, err)
	}
}
