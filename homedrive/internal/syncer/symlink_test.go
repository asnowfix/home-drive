package syncer

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// issue #191: symlinks in local_root are skipped and reported, never pushed
// or retried.

func skipIfNoSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
}

// linkFixture builds a root holding one link of each kind, plus a regular
// file, and returns the root and a directory outside it.
func linkFixture(t *testing.T) (root string) {
	t.Helper()
	skipIfNoSymlinks(t)
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(root, "real.txt"):   "x",
		filepath.Join(outside, "out.txt"): "y",
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"dangling":   filepath.Join(base, "missing"),
		"outside":    filepath.Join(outside, "out.txt"),
		"outsidedir": outside,
		"inroot":     filepath.Join(root, "real.txt"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestClassifyLink_Cases(t *testing.T) {
	root := linkFixture(t)
	tests := []struct {
		name       string
		rel        string
		wantLink   bool
		wantReason string
	}{
		{"Dangling", "dangling", true, LinkUnresolved},
		{"Loop", "loop", true, LinkUnresolved},
		{"OutsideFile", "outside", true, LinkOutsideRoot},
		{"OutsideDir", "outsidedir", true, LinkOutsideRoot},
		{"InRoot", "inroot", true, LinkInRoot},
		{"RegularFile", "real.txt", false, ""},
		{"Missing", "nope", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isLink, reason := classifyLink(root, filepath.Join(root, tt.rel))
			if isLink != tt.wantLink || reason != tt.wantReason {
				t.Errorf("classifyLink = (%v, %q), want (%v, %q)", isLink, reason, tt.wantLink, tt.wantReason)
			}
		})
	}
}

func TestSymlinkTracker_Cases(t *testing.T) {
	var buf bytes.Buffer
	tr := NewSymlinkTracker(slog.New(slog.NewJSONHandler(&buf, nil)))
	errLines := func() int { return strings.Count(buf.String(), `"level":"ERROR"`) }

	tr.Report("a", LinkUnresolved)
	tr.Report("a", LinkUnresolved)
	tr.Report("b", LinkInRoot)
	if tr.Count() != 2 || errLines() != 2 {
		t.Fatalf("after reports: count=%d errors=%d, want 2 and 2", tr.Count(), errLines())
	}
	if tr.Since().IsZero() {
		t.Error("Since is zero while links are tracked")
	}
	tr.Clear("a")
	if tr.Count() != 1 {
		t.Errorf("after Clear: count=%d, want 1", tr.Count())
	}
	// Reconcile: b left, c is new; a cleared earlier is reported again if it
	// reappears (a new problem).
	tr.Reconcile(map[string]string{"c": LinkOutsideRoot})
	if tr.Count() != 1 || errLines() != 3 {
		t.Errorf("after Reconcile: count=%d errors=%d, want 1 and 3", tr.Count(), errLines())
	}
	tr.Reconcile(map[string]string{"c": LinkOutsideRoot})
	if errLines() != 3 {
		t.Errorf("repeated Reconcile logged again: errors=%d, want 3", errLines())
	}
	tr.Reconcile(nil)
	if tr.Count() != 0 || !tr.Since().IsZero() {
		t.Errorf("after empty Reconcile: count=%d since=%v", tr.Count(), tr.Since())
	}
}

func TestSymlinkTracker_Bound(t *testing.T) {
	tr := NewSymlinkTracker(newDiscardLogger())
	found := make(map[string]string)
	for i := 0; i < maxTrackedLinks+50; i++ {
		found[string(rune('a'+i%26))+strings.Repeat("x", i/26)] = LinkUnresolved
	}
	tr.Reconcile(found)
	if tr.Count() != len(found) {
		t.Errorf("Count = %d, want %d (overflow still counted)", tr.Count(), len(found))
	}
	if len(tr.links) != maxTrackedLinks {
		t.Errorf("tracked set = %d, want bound %d", len(tr.links), maxTrackedLinks)
	}
	tr.Reconcile(nil)
	if tr.Count() != 0 {
		t.Errorf("Count after clearing = %d, want 0", tr.Count())
	}
}

// runPushLinks pushes events for names under a link fixture and returns the
// remote mock, store, tracker, sleep count and log buffer.
func TestSyncer_SymlinkEvents_SkippedNotRetried(t *testing.T) {
	tests := []struct {
		name      string
		link      string
		wantCount int
	}{
		{"Dangling", "dangling", 1},
		{"Loop", "loop", 1},
		{"OutsideRoot", "outside", 1},
		{"OutsideRootDir", "outsidedir", 1},
		{"InRootLink", "inroot", 1},
		{"RegularFile", "real.txt", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := linkFixture(t)
			remote := newMockRemoteFS()
			store := newMockStore()
			var logBuf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logBuf, nil))
			tr := NewSymlinkTracker(log)
			var sleeps atomic.Int32
			cfg := DefaultConfig()
			cfg.LocalRoot = root
			s := New(cfg, remote, store, newMockAuditLogger(), newMockPublisher(), log,
				WithSleepFunc(func(context.Context, time.Duration) { sleeps.Add(1) }),
				WithSymlinkTracker(tr))

			events := make(chan Event, 4)
			// Repeated events for the same path: reported once.
			for i := 0; i < 3; i++ {
				events <- Event{Path: tt.link, Op: OpWrite, At: time.Now()}
			}
			runSyncer(t, s, events, make(chan DirRename), func() {})

			wantCopies := 0
			if tt.wantCount == 0 {
				wantCopies = 3
			}
			if got := len(remote.getCopyCalls()); got != wantCopies {
				t.Errorf("CopyFile calls = %d, want %d", got, wantCopies)
			}
			if tt.wantCount > 0 && (len(store.getPuts()) != 0 || sleeps.Load() != 0) {
				t.Errorf("puts=%d sleeps=%d: skipped link must not reach the retry loop",
					len(store.getPuts()), sleeps.Load())
			}
			if got := tr.Count(); got != tt.wantCount {
				t.Errorf("tracker count = %d, want %d", got, tt.wantCount)
			}
			if got := strings.Count(logBuf.String(), `"msg":"symlink skipped: not synced"`); got != tt.wantCount {
				t.Errorf("ERROR lines = %d, want %d", got, tt.wantCount)
			}
			if strings.Contains(logBuf.String(), `"msg":"push failed"`) {
				t.Error("a push failure was logged for a skipped link")
			}
		})
	}
}

// A link replaced by a regular file leaves the count.
func TestSyncer_SymlinkBecomesRegular_LeavesCount(t *testing.T) {
	root := linkFixture(t)
	tr := NewSymlinkTracker(newDiscardLogger())
	cfg := DefaultConfig()
	cfg.LocalRoot = root
	s := New(cfg, newMockRemoteFS(), newMockStore(), newMockAuditLogger(), newMockPublisher(),
		newDiscardLogger(), WithSleepFunc(noSleep), WithSymlinkTracker(tr))

	run := func(op Op) {
		events := make(chan Event, 1)
		events <- Event{Path: "dangling", Op: op, At: time.Now()}
		runSyncer(t, s, events, make(chan DirRename), func() {})
	}
	run(OpCreate)
	if tr.Count() != 1 {
		t.Fatalf("count = %d, want 1", tr.Count())
	}
	if err := os.Remove(filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	run(OpRemove)
	if tr.Count() != 0 {
		t.Errorf("count after removal = %d, want 0", tr.Count())
	}
	if err := os.WriteFile(filepath.Join(root, "dangling"), []byte("now a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(OpCreate)
	if tr.Count() != 0 {
		t.Errorf("count after regular file = %d, want 0", tr.Count())
	}
}

func TestBisync_Symlinks_SkippedAndReportedOnce(t *testing.T) {
	root := linkFixture(t)
	b, _, remote, _, _, _ := newTestBisync(t, root, false)
	var logBuf bytes.Buffer
	b.links = NewSymlinkTracker(slog.New(slog.NewJSONHandler(&logBuf, nil)))

	for pass := 0; pass < 3; pass++ {
		b.execute(context.Background())
	}

	for _, name := range []string{"dangling", "loop", "outside", "outsidedir", "inroot"} {
		if remote.HasFile(name) {
			t.Errorf("link %q was uploaded", name)
		}
	}
	if !remote.HasFile("real.txt") {
		t.Error("regular file was not pushed")
	}
	if got := b.links.Count(); got != 5 {
		t.Errorf("tracker count = %d, want 5", got)
	}
	if got := strings.Count(logBuf.String(), `"msg":"symlink skipped: not synced"`); got != 5 {
		t.Errorf("ERROR lines over 3 passes = %d, want 5 (once per path)", got)
	}
	if err := os.Remove(filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	b.execute(context.Background())
	if got := b.links.Count(); got != 4 {
		t.Errorf("count after deleting a link = %d, want 4", got)
	}
}

// A link at a path that an earlier version pushed to Drive as a file must
// not cause a Drive deletion (nor a pull through the link).
func TestBisync_Symlinks_NotDeletedFromDrive(t *testing.T) {
	root := linkFixture(t)
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	seedBystander(t, root, remote, journal)
	remote.Seed("dangling", deleteT0, "m0")
	seedBaseline(journal, "dangling")

	b.execute(context.Background())

	if got := remote.getDeleteCalls(); len(got) != 0 {
		t.Errorf("remote deletes = %v, want none", got)
	}
	if !remote.HasFile("dangling") || !journal.Exists("dangling") {
		t.Error("Drive copy or journal entry of the skipped link was removed")
	}
	if len(remote.downloadedFiles) != 0 {
		t.Errorf("downloads = %v, want none (no write through the link)", remote.downloadedFiles)
	}
}
