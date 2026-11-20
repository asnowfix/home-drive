package syncer

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/pathfilter"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
)

var rootGuardT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// rootGuardEnv is one guard over a temp parent dir (parent), its local_root
// at parent/gdrive, a mock journal and recording audit, MQTT and log sinks.
type rootGuardEnv struct {
	parent  string
	root    string
	journal *mockJournal
	pub     *mockPublisher
	audit   *threadSafeBuffer
	logs    *threadSafeBuffer
	guard   *RootGuard
}

func newRootGuardEnv(t *testing.T, dryRun bool) *rootGuardEnv {
	t.Helper()
	parent := t.TempDir()
	e := &rootGuardEnv{
		parent:  parent,
		root:    filepath.Join(parent, "gdrive"),
		journal: newMockJournal(),
		pub:     newMockPublisher(),
		audit:   &threadSafeBuffer{},
		logs:    &threadSafeBuffer{},
	}
	log := slog.New(slog.NewJSONHandler(e.logs, nil))
	e.guard = NewRootGuard(RootGuardConfig{
		Root:     e.root,
		Identity: e.journal,
		Auditor:  store.NewAuditor(e.audit, log, store.WithClock(fixedClock(rootGuardT0))),
		MQTT:     e.pub,
		Log:      log,
		Clock:    fixedClock(rootGuardT0),
		DryRun:   dryRun,
	})
	return e
}

func (e *rootGuardEnv) mkRoot(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(e.root, 0o755); err != nil {
		t.Fatal(err)
	}
}

func (e *rootGuardEnv) writeFile(t *testing.T, rel string) {
	t.Helper()
	createLocalFile(t, e.root, rel, rootGuardT0)
}

func (e *rootGuardEnv) seedJournal(paths ...string) {
	for _, p := range paths {
		e.journal.Seed(JournalEntry{Path: p, LocalMtime: rootGuardT0, RemoteMtime: rootGuardT0, RemoteMD5: "m"})
	}
}

func (e *rootGuardEnv) marker() string {
	b, err := os.ReadFile(filepath.Join(e.root, pathfilter.RootMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// establish makes root a healthy, previously synced tree: marker, stored
// identity and a populated journal.
func (e *rootGuardEnv) establish(t *testing.T) {
	t.Helper()
	e.mkRoot(t)
	e.seedJournal("a.txt")
	e.writeFile(t, "a.txt")
	if err := e.guard.Check(); err != nil {
		t.Fatalf("establish: %v", err)
	}
	e.audit.buf.Reset()
	e.logs.buf.Reset()
}

func TestRootGuard_Check(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, e *rootGuardEnv)
		wantReason string // "" means the check passes
	}{
		{name: "first_install_empty_root_empty_journal",
			setup: func(t *testing.T, e *rootGuardEnv) { e.mkRoot(t) }},
		{name: "healthy_after_first_install",
			setup: func(t *testing.T, e *rootGuardEnv) { e.establish(t) }},
		{name: "missing_root_populated_journal", wantReason: RootBlockMissing,
			setup: func(t *testing.T, e *rootGuardEnv) { e.establish(t); _ = os.RemoveAll(e.root) }},
		{name: "dangling_symlink", wantReason: RootBlockMissing,
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.establish(t)
				target := filepath.Join(e.parent, "disk")
				must(t, os.Rename(e.root, target))
				must(t, os.Symlink(target, e.root))
				_ = os.RemoveAll(target) // the disk went away; the link dangles
			}},
		{name: "root_is_a_file", wantReason: RootBlockMissing,
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.establish(t)
				_ = os.RemoveAll(e.root)
				must(t, os.WriteFile(e.root, []byte("x"), 0o644))
			}},
		{name: "replaced_by_empty_dir", wantReason: RootBlockNoMarker,
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.establish(t)
				_ = os.RemoveAll(e.root)
				e.mkRoot(t)
			}},
		{name: "replaced_by_other_populated_tree_without_marker", wantReason: RootBlockNoMarker,
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.establish(t)
				_ = os.RemoveAll(e.root)
				e.writeFile(t, "a.txt") // same name, no marker: still not the tree
			}},
		{name: "other_installs_marker", wantReason: RootBlockMismatch,
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.establish(t)
				must(t, os.WriteFile(filepath.Join(e.root, pathfilter.RootMarker), []byte("someone-else\n"), 0o644))
			}},
		{name: "symlinked_root_healthy",
			setup: func(t *testing.T, e *rootGuardEnv) {
				disk := filepath.Join(e.parent, "disk")
				must(t, os.MkdirAll(disk, 0o755))
				must(t, os.Symlink(disk, e.root))
				e.seedJournal("a.txt")
				createLocalFile(t, disk, "a.txt", rootGuardT0)
				_ = e.guard.Check() // adopt through the link
				e.logs.buf.Reset()
			}},
		{name: "upgrade_populated_journal_tree_present",
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.mkRoot(t)
				e.seedJournal("a.txt", "b/c.txt")
				e.writeFile(t, "b/c.txt")
			}},
		{name: "upgrade_populated_journal_empty_root", wantReason: RootBlockNoMarker,
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.mkRoot(t)
				e.seedJournal("a.txt", "b/c.txt")
			}},
		{name: "state_reset_marker_present",
			setup: func(t *testing.T, e *rootGuardEnv) {
				e.establish(t)
				e.journal.meta = map[string]string{} // fresh state DB, tree kept
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newRootGuardEnv(t, false)
			tc.setup(t, e)
			err := e.guard.Check()
			blk := e.guard.Block()
			if tc.wantReason == "" {
				if err != nil || blk != nil {
					t.Fatalf("want pass, got err=%v block=%+v", err, blk)
				}
				if e.marker() == "" {
					t.Error("healthy tree must carry the marker after the check")
				}
				return
			}
			if !errors.Is(err, ErrRootBlocked) {
				t.Fatalf("want ErrRootBlocked, got %v", err)
			}
			if blk == nil || blk.Reason != tc.wantReason {
				t.Fatalf("want reason %q, got %+v", tc.wantReason, blk)
			}
		})
	}
}

func TestRootGuard_FirstInstallRecordsIdentity(t *testing.T) {
	e := newRootGuardEnv(t, false)
	e.mkRoot(t)
	if err := e.guard.Check(); err != nil {
		t.Fatalf("first install must pass: %v", err)
	}
	id := e.marker()
	if id == "" {
		t.Fatal("marker not written")
	}
	stored, _ := e.journal.GetMeta(keyRootID)
	if stored != id {
		t.Errorf("stored identity %q != marker %q", stored, id)
	}
	if e.guard.Block() != nil {
		t.Error("no block expected")
	}
}

func TestRootGuard_DryRunWritesNothing(t *testing.T) {
	e := newRootGuardEnv(t, true)
	e.mkRoot(t)
	if err := e.guard.Check(); err != nil {
		t.Fatalf("dry-run first install must pass: %v", err)
	}
	if e.marker() != "" {
		t.Error("dry-run must not write the marker")
	}
	if stored, _ := e.journal.GetMeta(keyRootID); stored != "" {
		t.Error("dry-run must not record an identity")
	}
}

func TestRootGuard_ReportsOnceAndRecovers(t *testing.T) {
	e := newRootGuardEnv(t, false)
	e.establish(t)
	disk := e.root + ".disk"
	if err := os.Rename(e.root, disk); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ { // three cycles while the disk is away
		if err := e.guard.Check(); !errors.Is(err, ErrRootBlocked) {
			t.Fatalf("cycle %d: want blocked, got %v", i, err)
		}
	}
	blocked := e.pub.getMessagesByTopic(e.pub.Topic("events", "root", "blocked"))
	if len(blocked) != 1 {
		t.Fatalf("want exactly 1 blocked event for 3 blocked cycles, got %d", len(blocked))
	}
	if ev, ok := blocked[0].Payload.(RootEvent); !ok || ev.Type != "root.blocked" || ev.Reason != RootBlockMissing || ev.Timestamp == "" {
		t.Errorf("blocked payload = %+v", blocked[0].Payload)
	}
	if n := strings.Count(e.audit.String(), `"op":"root_blocked"`); n != 1 {
		t.Errorf("want 1 root_blocked audit line, got %d:\n%s", n, e.audit.String())
	}
	if !strings.Contains(e.audit.String(), `"reason":"`+RootBlockMissing+`"`) {
		t.Errorf("audit line lacks the reason: %s", e.audit.String())
	}
	if !strings.Contains(e.logs.String(), `"level":"ERROR"`) ||
		!strings.Contains(e.logs.String(), RootBlockMissing) {
		t.Errorf("want an ERROR log naming the reason, got %s", e.logs.String())
	}
	blk := e.guard.Block()
	if blk == nil || !blk.Since.Equal(rootGuardT0) {
		t.Fatalf("block/since not recorded: %+v", blk)
	}

	// The disk comes back: the next check passes by itself.
	if err := os.Rename(disk, e.root); err != nil {
		t.Fatal(err)
	}
	if err := e.guard.Check(); err != nil {
		t.Fatalf("want recovery, got %v", err)
	}
	if e.guard.Block() != nil {
		t.Error("block must clear on recovery")
	}
	resumed := e.pub.getMessagesByTopic(e.pub.Topic("events", "root", "resumed"))
	if len(resumed) != 1 {
		t.Fatalf("want 1 resumed event, got %d", len(resumed))
	}
	if ev, ok := resumed[0].Payload.(RootEvent); !ok || ev.Type != "root.resumed" || ev.Reason != RootBlockMissing || ev.Timestamp == "" {
		t.Errorf("resumed payload = %+v", resumed[0].Payload)
	}
	if !strings.Contains(e.audit.String(), `"op":"root_resumed"`) {
		t.Errorf("want a root_resumed audit line: %s", e.audit.String())
	}
}

// must fails the test on a setup error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
