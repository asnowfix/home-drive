package syncer

import (
	"context"
	"sort"
	"testing"

	"github.com/asnowfix/home-drive/homedrive/internal/oldsuffix"
)

// testRepairFixture is an in-memory harness for RepairChains: local and
// remote path sets, plus a fake journal, all mutated in place by the
// RenameLocal/RenameRemote/Journal* closures so assertions can inspect
// post-repair state directly.
type testRepairFixture struct {
	local   map[string]struct{}
	remote  map[string]struct{}
	journal map[string]JournalEntry
}

func newTestRepairFixture() *testRepairFixture {
	return &testRepairFixture{
		local:   make(map[string]struct{}),
		remote:  make(map[string]struct{}),
		journal: make(map[string]JournalEntry),
	}
}

func (f *testRepairFixture) seedLocal(paths ...string) {
	for _, p := range paths {
		f.local[p] = struct{}{}
	}
}

func (f *testRepairFixture) seedRemote(paths ...string) {
	for _, p := range paths {
		f.remote[p] = struct{}{}
	}
}

func (f *testRepairFixture) seedJournal(path string) {
	f.journal[path] = JournalEntry{Path: path, LastOrigin: "local"}
}

func (f *testRepairFixture) locals() []LocalFileInfo {
	out := make([]LocalFileInfo, 0, len(f.local))
	for p := range f.local {
		out = append(out, LocalFileInfo{Path: p})
	}
	return out
}

func (f *testRepairFixture) remotes() []RemoteObject {
	out := make([]RemoteObject, 0, len(f.remote))
	for p := range f.remote {
		out = append(out, RemoteObject{Path: p})
	}
	return out
}

func (f *testRepairFixture) deps() RepairDeps {
	m, _ := oldsuffix.New("")
	return RepairDeps{
		Matcher: m,
		RenameLocal: func(oldPath, newPath string) error {
			delete(f.local, oldPath)
			f.local[newPath] = struct{}{}
			return nil
		},
		RenameRemote: func(_ context.Context, oldPath, newPath string) error {
			delete(f.remote, oldPath)
			f.remote[newPath] = struct{}{}
			return nil
		},
		JournalGet: func(path string) (*JournalEntry, error) {
			e, ok := f.journal[path]
			if !ok {
				return nil, nil
			}
			return &e, nil
		},
		JournalDelete: func(path string) error {
			delete(f.journal, path)
			return nil
		},
		JournalPut: func(entry JournalEntry) error {
			f.journal[entry.Path] = entry
			return nil
		},
	}
}

// TestRepairChains_MultiLevel is the issue #65 regression: a 13-deep
// nested chain on myhome-kiosk.md collapses onto the base's flat
// namespace, with no content lost (every link survives as a renamed
// path) and every final depth == 1.
func TestRepairChains_MultiLevel(t *testing.T) {
	f := newTestRepairFixture()
	base := "myhome-kiosk.md"
	f.seedLocal(base)
	nested := base
	var chain []string
	for range 13 {
		nested += ".old.1"
		chain = append(chain, nested)
	}
	f.seedLocal(chain...)
	for _, p := range chain {
		f.seedJournal(p)
	}

	report, err := RepairChains(context.Background(), f.deps(), f.locals(), nil)
	if err != nil {
		t.Fatalf("RepairChains: %v", err)
	}
	// Of the 13 links, the shallowest (base+".old.1", depth 1) is
	// already correctly named -- only the 12 deeper (depth >= 2) links
	// are repair candidates.
	if report.Scanned != 12 {
		t.Errorf("Scanned = %d, want 12", report.Scanned)
	}
	if len(report.Links) != 12 {
		t.Fatalf("len(Links) = %d, want 12", len(report.Links))
	}

	// No content lost: exactly 14 paths survive (base + 13 renumbered
	// links), and every surviving .old path has depth exactly 1.
	if len(f.local) != 14 {
		t.Fatalf("len(local) = %d, want 14 (base + 13 links), got %v", len(f.local), f.local)
	}
	m, _ := oldsuffix.New("")
	seenN := make(map[int]bool)
	for p := range f.local {
		if p == base {
			continue
		}
		_, n, ok := m.TrimOne(p)
		if !ok {
			t.Errorf("surviving path %q is not base+single-suffix", p)
			continue
		}
		if _, depth := m.Base(p); depth != 1 {
			t.Errorf("surviving path %q has depth %d, want 1", p, depth)
		}
		if seenN[n] {
			t.Errorf("duplicate N=%d among surviving links", n)
		}
		seenN[n] = true
	}
	// N values should be exactly 1..13, no gaps or collisions.
	for n := 1; n <= 13; n++ {
		if !seenN[n] {
			t.Errorf("missing N=%d among surviving links", n)
		}
	}
}

// TestRepairChains_Idempotent verifies a second pass over already-repaired
// output is a no-op (every remaining path has depth <= 1, so nothing
// matches the depth>=2 candidate filter).
func TestRepairChains_Idempotent(t *testing.T) {
	f := newTestRepairFixture()
	base := "notes.md"
	f.seedLocal(base, base+".old.1.old.1", base+".old.1.old.2")
	f.seedJournal(base + ".old.1.old.1")
	f.seedJournal(base + ".old.1.old.2")

	if _, err := RepairChains(context.Background(), f.deps(), f.locals(), nil); err != nil {
		t.Fatalf("first RepairChains: %v", err)
	}

	report, err := RepairChains(context.Background(), f.deps(), f.locals(), nil)
	if err != nil {
		t.Fatalf("second RepairChains: %v", err)
	}
	if report.Scanned != 0 || len(report.Links) != 0 {
		t.Errorf("second pass should be a no-op, got scanned=%d links=%v", report.Scanned, report.Links)
	}
}

// TestRepairChains_CollisionPicksNextFreeN verifies that when .old.1 and
// .old.2 already exist as real (non-nested) siblings, a nested chain
// collapsing onto the same base skips straight to .old.3.
func TestRepairChains_CollisionPicksNextFreeN(t *testing.T) {
	f := newTestRepairFixture()
	base := "report.md"
	f.seedLocal(base, base+".old.1", base+".old.2", base+".old.1.old.1")
	f.seedJournal(base + ".old.1.old.1")

	report, err := RepairChains(context.Background(), f.deps(), f.locals(), nil)
	if err != nil {
		t.Fatalf("RepairChains: %v", err)
	}
	if len(report.Links) != 1 {
		t.Fatalf("len(Links) = %d, want 1", len(report.Links))
	}
	if got := report.Links[0].NewPath; got != base+".old.3" {
		t.Errorf("NewPath = %q, want %q", got, base+".old.3")
	}
	if _, ok := f.local[base+".old.3"]; !ok {
		t.Errorf("expected %s.old.3 to exist after repair", base)
	}
}

// TestRepairChains_SkipsUnknownBase verifies a user file that merely
// looks like a nested chain (no real base file on that side) is left
// untouched -- collapsing it would silently rename a file the user
// created themselves.
func TestRepairChains_SkipsUnknownBase(t *testing.T) {
	f := newTestRepairFixture()
	f.seedLocal("budget.old.2.old.1") // no "budget" or "budget.old.2" present

	report, err := RepairChains(context.Background(), f.deps(), f.locals(), nil)
	if err != nil {
		t.Fatalf("RepairChains: %v", err)
	}
	if report.Scanned != 0 || len(report.Links) != 0 {
		t.Errorf("expected no candidates for an unknown base, got scanned=%d links=%v", report.Scanned, report.Links)
	}
	if _, ok := f.local["budget.old.2.old.1"]; !ok {
		t.Error("expected budget.old.2.old.1 to survive untouched")
	}
}

// TestRepairChains_DryRun verifies a dry run reports what would be
// renumbered without renaming anything.
func TestRepairChains_DryRun(t *testing.T) {
	f := newTestRepairFixture()
	base := "notes.md"
	f.seedLocal(base, base+".old.1.old.1")
	f.seedJournal(base + ".old.1.old.1")

	deps := f.deps()
	deps.DryRun = true

	report, err := RepairChains(context.Background(), deps, f.locals(), nil)
	if err != nil {
		t.Fatalf("RepairChains: %v", err)
	}
	if len(report.Links) != 1 {
		t.Fatalf("len(Links) = %d, want 1", len(report.Links))
	}
	if got := report.Links[0].NewPath; got != base+".old.1" {
		t.Errorf("NewPath = %q, want %q", got, base+".old.1")
	}
	// Nothing actually renamed.
	if _, ok := f.local[base+".old.1.old.1"]; !ok {
		t.Error("dry run should not have renamed the nested chain link")
	}
	if _, ok := f.local[base+".old.1"]; ok {
		t.Error("dry run should not have created the target path")
	}
}

// TestRepairChains_RemoteSide verifies a nested chain living only on the
// remote side is repaired via RenameRemote, independent of local state.
func TestRepairChains_RemoteSide(t *testing.T) {
	f := newTestRepairFixture()
	base := "notes.md"
	f.seedRemote(base, base+".old.1.old.1")
	f.seedJournal(base + ".old.1.old.1")

	report, err := RepairChains(context.Background(), f.deps(), nil, f.remotes())
	if err != nil {
		t.Fatalf("RepairChains: %v", err)
	}
	if len(report.Links) != 1 {
		t.Fatalf("len(Links) = %d, want 1", len(report.Links))
	}
	if got := report.Links[0].Side; got != "remote" {
		t.Errorf("Side = %q, want %q", got, "remote")
	}
	if _, ok := f.remote[base+".old.1"]; !ok {
		t.Error("expected notes.md.old.1 to exist on remote after repair")
	}
	if _, ok := f.remote[base+".old.1.old.1"]; ok {
		t.Error("expected the nested remote path to be gone after repair")
	}
}

// TestRepairChains_DeepestFirstAvoidsCollision verifies deterministic
// deepest-first ordering: two chains sharing a base never collide on the
// same target N, regardless of map iteration order.
func TestRepairChains_DeepestFirstAvoidsCollision(t *testing.T) {
	f := newTestRepairFixture()
	base := "shared.md"
	f.seedLocal(
		base,
		base+".old.1.old.1",       // depth 2
		base+".old.1.old.1.old.1", // depth 3
	)
	f.seedJournal(base + ".old.1.old.1")
	f.seedJournal(base + ".old.1.old.1.old.1")

	report, err := RepairChains(context.Background(), f.deps(), f.locals(), nil)
	if err != nil {
		t.Fatalf("RepairChains: %v", err)
	}
	if len(report.Links) != 2 {
		t.Fatalf("len(Links) = %d, want 2", len(report.Links))
	}
	got := make([]string, len(report.Links))
	for i, l := range report.Links {
		got[i] = l.NewPath
	}
	sort.Strings(got)
	want := []string{base + ".old.1", base + ".old.2"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("targets = %v, want %v (no collision)", got, want)
	}
}
