package store

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// Compile-time interface compliance check.
var _ Store = (*JournalStore)(nil)

func newTestJournalForStore(t *testing.T) *Journal {
	t.Helper()
	dir := t.TempDir()
	j, err := OpenJournal(filepath.Join(dir, "state.db"), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

// TestJournalStore_DelegatesToJournal covers every method on JournalStore
// against a real *Journal (bbolt against t.TempDir(), per the
// homedrive-test-mocks skill).
func TestJournalStore_DelegatesToJournal(t *testing.T) {
	j := newTestJournalForStore(t)
	s := NewJournalStore(j, slog.Default())
	ctx := context.Background()

	if err := s.SetPageToken(ctx, "tok-1"); err != nil {
		t.Fatalf("SetPageToken: %v", err)
	}
	got, err := s.GetPageToken(ctx)
	if err != nil || got != "tok-1" {
		t.Fatalf("GetPageToken = %q, %v", got, err)
	}

	if _, found, err := s.Get(ctx, "missing.txt"); err != nil || found {
		t.Fatalf("expected not-found for missing.txt, got found=%v err=%v", found, err)
	}

	entry := JournalEntry{Path: "dir/old.txt", LastOrigin: "local"}
	if err := s.Put(ctx, entry); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got2, found, err := s.Get(ctx, "dir/old.txt")
	if err != nil || !found || got2.LastOrigin != "local" {
		t.Fatalf("Get after Put = %+v, found=%v, err=%v", got2, found, err)
	}

	base, n, err := s.NextOldN(ctx, "dir/old.txt")
	if err != nil || base != "dir/old.txt" || n != 1 {
		t.Fatalf("NextOldN = (%q, %d), %v", base, n, err)
	}

	if err := s.Put(ctx, JournalEntry{Path: "dir/child.txt"}); err != nil {
		t.Fatal(err)
	}
	count, err := s.RewritePrefix(ctx, "dir/", "dir2/")
	if err != nil {
		t.Fatalf("RewritePrefix: %v", err)
	}
	if count == 0 {
		t.Error("expected RewritePrefix to move at least one entry")
	}

	if err := s.Delete(ctx, "dir2/old.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
