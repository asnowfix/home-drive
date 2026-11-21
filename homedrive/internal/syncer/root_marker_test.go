package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
)

// failSetMeta is a RootIdentityStore whose SetMeta fails while fail is set,
// like a crash between writing the marker and recording the id.
type failSetMeta struct {
	*mockJournal
	fail bool
}

func (f *failSetMeta) SetMeta(key []byte, val string) error {
	if f.fail {
		return errors.New("simulated crash")
	}
	return f.mockJournal.SetMeta(key, val)
}

func TestWriteRootMarker_Cases(t *testing.T) {
	tests := []struct {
		name string
		pre  func(t *testing.T, root string)
	}{
		{name: "fresh"},
		{name: "overwrites_truncated_marker", pre: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, pathfilter.RootMarker), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "overwrites_stale_tmp", pre: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, pathfilter.RootMarker+".tmp"), []byte("junk"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.pre != nil {
				tc.pre(t, root)
			}
			if err := writeRootMarker(root, "abc123"); err != nil {
				t.Fatal(err)
			}
			if got, _ := readRootMarker(root); got != "abc123" {
				t.Errorf("marker = %q", got)
			}
			if _, err := os.Lstat(filepath.Join(root, pathfilter.RootMarker+".tmp")); err == nil {
				t.Error("temp file left behind")
			}
		})
	}
}

func TestRootGuard_CrashOrderings(t *testing.T) {
	e := newRootGuardEnv(t, false)
	fs := &failSetMeta{mockJournal: e.journal, fail: true}
	e.guard = NewRootGuard(RootGuardConfig{Root: e.root, Identity: fs})
	e.mkRoot(t)

	// Crash after the marker, before the id is recorded.
	if err := e.guard.Check(); !errors.Is(err, ErrRootBlocked) {
		t.Fatalf("want blocked while the id cannot be recorded, got %v", err)
	}
	id := e.marker()
	if id == "" {
		t.Fatal("the marker must be written before the id is recorded")
	}
	// After the restart: the marker without a stored id is adopted.
	fs.fail = false
	if err := e.guard.Check(); err != nil {
		t.Fatalf("marker without stored id must be adopted: %v", err)
	}
	if stored, _ := e.journal.GetMeta(keyRootID); stored != id {
		t.Errorf("stored %q, want the marker's %q", stored, id)
	}
}

func TestRootGuard_TruncatedMarker(t *testing.T) {
	e := newRootGuardEnv(t, false)
	e.mkRoot(t)
	marker := filepath.Join(e.root, pathfilter.RootMarker)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// No stored id: the marker is rewritten and the check passes.
	if err := e.guard.Check(); err != nil || e.marker() == "" {
		t.Fatalf("truncated marker without stored id must self-heal: err=%v marker=%q", err, e.marker())
	}
	// A stored id with a truncated marker blocks and names the id to restore.
	stored, _ := e.journal.GetMeta(keyRootID)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.guard.Check(); !errors.Is(err, ErrRootBlocked) {
		t.Fatalf("want blocked, got %v", err)
	}
	blk := e.guard.Block()
	if blk.Reason != RootBlockNoMarker || !strings.Contains(blk.Detail, stored) {
		t.Errorf("detail must carry the expected id %q: %+v", stored, blk)
	}
	// Restoring the file as the detail says clears the block.
	if err := os.WriteFile(marker, []byte(stored+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.guard.Check(); err != nil {
		t.Fatalf("restored marker must pass: %v", err)
	}
}

func TestRootGuard_DryRunUpgradePasses(t *testing.T) {
	e := newRootGuardEnv(t, true)
	e.mkRoot(t)
	e.seedJournal("a.txt")
	e.writeFile(t, "a.txt")
	if err := e.guard.Check(); err != nil {
		t.Fatalf("dry-run upgrade must pass: %v", err)
	}
	if stored, _ := e.journal.GetMeta(keyRootID); stored != "" || e.marker() != "" {
		t.Error("dry-run must write neither marker nor id")
	}
}

func TestRootGuard_DryRunStillBlocksAndReports(t *testing.T) {
	e := newRootGuardEnv(t, true)
	e.mkRoot(t)
	_ = e.journal.SetMeta(keyRootID, "some-id")
	if err := e.guard.Check(); !errors.Is(err, ErrRootBlocked) {
		t.Fatalf("want blocked in dry-run, got %v", err)
	}
	if !strings.Contains(e.audit.String(), `"op":"root_blocked"`) {
		t.Error("dry-run block must still be audited")
	}
}

func TestRootGuard_OnResumeCalledOncePerRecovery(t *testing.T) {
	e := newRootGuardEnv(t, false)
	e.establish(t)
	calls := 0
	e.guard = NewRootGuard(RootGuardConfig{Root: e.root, Identity: e.journal, OnResume: func() { calls++ }})
	disk := e.root + ".disk"
	if err := os.Rename(e.root, disk); err != nil {
		t.Fatal(err)
	}
	_ = e.guard.Check()
	_ = e.guard.Check()
	if err := os.Rename(disk, e.root); err != nil {
		t.Fatal(err)
	}
	// The check that sees the root come back, and every later one, refuses:
	// the agent is exiting and nothing may act on the way out.
	for i := 0; i < 2; i++ {
		if err := e.guard.Check(); !errors.Is(err, ErrRootBlocked) {
			t.Errorf("check %d after the recovery = %v, want ErrRootBlocked", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("OnResume calls = %d, want 1", calls)
	}
}
