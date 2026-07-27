package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// pruneFixture is an in-memory harness satisfying PruneDeps' function
// fields, used to test PruneOldSiblings/SweepOldFiles without a real
// bbolt journal or RemoteFS. PruneDeps is deliberately function-based
// (not a Journal/Store interface) so a fixture like this needs no
// adapter type -- see the homedrive-test-mocks skill.
type pruneFixture struct {
	entries         map[string]JournalEntry
	localDeleted    []string
	remoteDeleted   []string
	deleteLocalErr  error
	deleteRemoteErr error
}

func newPruneFixture() *pruneFixture {
	return &pruneFixture{entries: make(map[string]JournalEntry)}
}

func (f *pruneFixture) seed(entries ...JournalEntry) {
	for _, e := range entries {
		f.entries[e.Path] = e
	}
}

func (f *pruneFixture) listByPrefix(prefix string) ([]JournalEntry, error) {
	var out []JournalEntry
	for p, e := range f.entries {
		if strings.HasPrefix(p, prefix) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *pruneFixture) forEach(fn func(JournalEntry) error) error {
	for _, e := range f.entries {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (f *pruneFixture) deleteEntry(path string) error {
	delete(f.entries, path)
	return nil
}

func (f *pruneFixture) removeLocal(relPath string) error {
	if f.deleteLocalErr != nil {
		return f.deleteLocalErr
	}
	f.localDeleted = append(f.localDeleted, relPath)
	return nil
}

func (f *pruneFixture) removeRemote(_ context.Context, path string) error {
	if f.deleteRemoteErr != nil {
		return f.deleteRemoteErr
	}
	f.remoteDeleted = append(f.remoteDeleted, path)
	return nil
}

func (f *pruneFixture) deps(t *testing.T) PruneDeps {
	t.Helper()
	m, err := oldsuffix.New("")
	if err != nil {
		t.Fatalf("oldsuffix.New: %v", err)
	}
	return PruneDeps{
		Matcher:      m,
		ListByPrefix: f.listByPrefix,
		ForEach:      f.forEach,
		DeleteEntry:  f.deleteEntry,
		RemoveLocal:  f.removeLocal,
		RemoveRemote: f.removeRemote,
	}
}

func TestPruneOldSiblings_MaxPerFile(t *testing.T) {
	f := newPruneFixture()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for n := 1; n <= 5; n++ {
		f.seed(JournalEntry{
			Path:         fmt.Sprintf("f.md.old.%d", n),
			LastOrigin:   "local",
			LastSyncedAt: t0.Add(time.Duration(n) * time.Hour),
		})
	}

	removed, err := PruneOldSiblings(context.Background(), f.deps(t), RetentionPolicy{MaxPerFile: 3}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}

	if len(removed) != 2 {
		t.Fatalf("removed = %v, want 2 entries", removed)
	}
	wantRemoved := map[string]bool{"f.md.old.1": true, "f.md.old.2": true}
	for _, p := range removed {
		if !wantRemoved[p] {
			t.Errorf("unexpected removal: %s", p)
		}
	}
	for _, keep := range []string{"f.md.old.3", "f.md.old.4", "f.md.old.5"} {
		if _, ok := f.entries[keep]; !ok {
			t.Errorf("%s was removed from the journal, want kept (newest 3)", keep)
		}
	}
	if len(f.localDeleted) != 2 {
		t.Errorf("localDeleted = %v, want 2 files removed", f.localDeleted)
	}
}

func TestPruneOldSiblings_MaxAge(t *testing.T) {
	f := newPruneFixture()
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	f.seed(
		JournalEntry{Path: "f.md.old.1", LastOrigin: "local", LastSyncedAt: now.Add(-48 * time.Hour)}, // expired
		JournalEntry{Path: "f.md.old.2", LastOrigin: "local", LastSyncedAt: now.Add(-1 * time.Hour)},  // fresh
	)

	deps := f.deps(t)
	deps.Clock = func() time.Time { return now }

	removed, err := PruneOldSiblings(context.Background(), deps, RetentionPolicy{MaxAge: 24 * time.Hour}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}
	if len(removed) != 1 || removed[0] != "f.md.old.1" {
		t.Errorf("removed = %v, want [f.md.old.1]", removed)
	}
	if _, ok := f.entries["f.md.old.2"]; !ok {
		t.Error("f.md.old.2 should survive (within max_age)")
	}
}

func TestPruneOldSiblings_Unlimited(t *testing.T) {
	f := newPruneFixture()
	for n := 1; n <= 5; n++ {
		f.seed(JournalEntry{
			Path: fmt.Sprintf("f.md.old.%d", n), LastOrigin: "local", LastSyncedAt: time.Now(),
		})
	}

	removed, err := PruneOldSiblings(context.Background(), f.deps(t), RetentionPolicy{MaxPerFile: 0}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none (max_per_file: 0 = unlimited)", removed)
	}
}

func TestPruneOldSiblings_KeepsNewest(t *testing.T) {
	// Represents the downstream effect of config.applyDefaults clamping
	// an explicit max_per_file: 0 or negative up to 1 before it ever
	// reaches PruneOldSiblings (see TestLoad_MaxPerFileClamped in the
	// config package): only the single newest sibling survives.
	f := newPruneFixture()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for n := 1; n <= 3; n++ {
		f.seed(JournalEntry{
			Path:         fmt.Sprintf("f.md.old.%d", n),
			LastOrigin:   "local",
			LastSyncedAt: t0.Add(time.Duration(n) * time.Hour),
		})
	}

	removed, err := PruneOldSiblings(context.Background(), f.deps(t), RetentionPolicy{MaxPerFile: 1}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want 2", removed)
	}
	if _, ok := f.entries["f.md.old.3"]; !ok {
		t.Error("f.md.old.3 (newest) should survive")
	}
}

func TestPruneOldSiblings_RemoteSideUsesDeleteFile(t *testing.T) {
	f := newPruneFixture()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.seed(
		JournalEntry{Path: "f.md.old.1", LastOrigin: "remote", LastSyncedAt: t0},
		JournalEntry{Path: "f.md.old.2", LastOrigin: "remote", LastSyncedAt: t0.Add(time.Hour)},
	)

	removed, err := PruneOldSiblings(context.Background(), f.deps(t), RetentionPolicy{MaxPerFile: 1}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %v, want 1", removed)
	}
	if len(f.remoteDeleted) != 1 || f.remoteDeleted[0] != "f.md.old.1" {
		t.Errorf("remoteDeleted = %v, want [f.md.old.1]", f.remoteDeleted)
	}
	if len(f.localDeleted) != 0 {
		t.Errorf("localDeleted = %v, want none (loser lives on the remote side)", f.localDeleted)
	}
}

func TestPruneOldSiblings_DeleteFails_KeepsJournalEntry(t *testing.T) {
	f := newPruneFixture()
	f.deleteLocalErr = errors.New("disk full")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.seed(
		JournalEntry{Path: "f.md.old.1", LastOrigin: "local", LastSyncedAt: t0},
		JournalEntry{Path: "f.md.old.2", LastOrigin: "local", LastSyncedAt: t0.Add(time.Hour)},
	)

	removed, err := PruneOldSiblings(context.Background(), f.deps(t), RetentionPolicy{MaxPerFile: 1}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none (file delete failed)", removed)
	}
	// The §11.5 ordering invariant: the journal entry must survive a
	// failed file delete, since it is what makes .old.<N> reuse safe.
	if _, ok := f.entries["f.md.old.1"]; !ok {
		t.Error("journal entry for f.md.old.1 must survive a failed file delete")
	}
}

func TestPruneOldSiblings_DryRun(t *testing.T) {
	f := newPruneFixture()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.seed(
		JournalEntry{Path: "f.md.old.1", LastOrigin: "local", LastSyncedAt: t0},
		JournalEntry{Path: "f.md.old.2", LastOrigin: "local", LastSyncedAt: t0.Add(time.Hour)},
	)

	deps := f.deps(t)
	deps.DryRun = true

	removed, err := PruneOldSiblings(context.Background(), deps, RetentionPolicy{MaxPerFile: 1}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none in dry-run", removed)
	}
	if len(f.localDeleted) != 0 {
		t.Errorf("localDeleted = %v, want none in dry-run", f.localDeleted)
	}
	if _, ok := f.entries["f.md.old.1"]; !ok {
		t.Error("journal entry must survive a dry-run")
	}
}

func TestPruneOldSiblings_IgnoresNestedChains(t *testing.T) {
	f := newPruneFixture()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.seed(
		JournalEntry{Path: "f.md.old.1", LastOrigin: "local", LastSyncedAt: t0},
		// A nested chain link: TrimOne("f.md.old.1.old.2") = ("f.md.old.1", 2),
		// not a direct sibling of "f.md" -- left untouched by retention;
		// only the repair pass collapses these (PLAN.md §11.5).
		JournalEntry{Path: "f.md.old.1.old.2", LastOrigin: "local", LastSyncedAt: t0.Add(time.Hour)},
	)

	removed, err := PruneOldSiblings(context.Background(), f.deps(t), RetentionPolicy{MaxPerFile: 1}, "f.md")
	if err != nil {
		t.Fatalf("PruneOldSiblings: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none: only one true sibling exists, within max_per_file:1", removed)
	}
}

func TestSweepOldFiles_GroupsByBaseAndAppliesPolicy(t *testing.T) {
	f := newPruneFixture()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.seed(
		JournalEntry{Path: "a.md.old.1", LastOrigin: "local", LastSyncedAt: t0},
		JournalEntry{Path: "a.md.old.2", LastOrigin: "local", LastSyncedAt: t0.Add(time.Hour)},
		JournalEntry{Path: "b.md.old.1", LastOrigin: "local", LastSyncedAt: t0},
	)

	removed, err := SweepOldFiles(context.Background(), f.deps(t), RetentionPolicy{MaxPerFile: 1})
	if err != nil {
		t.Fatalf("SweepOldFiles: %v", err)
	}

	if len(removed) != 1 || removed[0] != "a.md.old.1" {
		t.Errorf("removed = %v, want [a.md.old.1] (b.md has only one sibling, within the cap)", removed)
	}
	if _, ok := f.entries["a.md.old.2"]; !ok {
		t.Error("a.md.old.2 (newest of its group) should survive")
	}
	if _, ok := f.entries["b.md.old.1"]; !ok {
		t.Error("b.md.old.1 (only sibling of its group) should survive")
	}
}
