package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Issue #154: deletions homedrive performs itself must not be echoed back to
// Drive as push `remove` ops, while genuine user deletions still are.
//
// Production model (since #165): the Trasher rmdirs directories (watcher:
// OpRemove) and moves files into the trash (watcher: unpaired OpRename, which
// the push side drops). Tests use the real Trasher and feed those shapes.

const guardTTL = 30 * time.Second

type guardClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *guardClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *guardClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newGuardClock() *guardClock { return &guardClock{t: deleteT0} }

type removerFunc func(string) error

func (f removerFunc) Remove(p string) error { return f(p) }

// pushResult is what the push syncer did with a batch of watcher events.
type pushResult struct {
	deletes    []string // Drive DeleteFile calls
	suppressed []string // remove_suppressed audit paths
}

// pushEvents feeds evs to a push syncer wired with g (one worker, direct
// handleEvent calls, so order is deterministic).
func pushEvents(t *testing.T, root string, g *DeleteGuard, evs ...Event) pushResult {
	t.Helper()
	remote := newMockRemoteFS()
	audit := newMockAuditLogger()
	s := New(Config{LocalRoot: root, Workers: 1, Retry: DefaultRetryConfig()}, remote, newMockStore(),
		audit, newMockPublisher(), newDiscardLogger(),
		WithSleepFunc(noSleep), WithDeleteGuard(g))
	for _, ev := range evs {
		s.handleEvent(context.Background(), ev)
	}
	res := pushResult{deletes: append([]string(nil), remote.getDeleteCalls()...)}
	for _, e := range audit.getEntries() {
		if e.Op == "remove_suppressed" {
			res.suppressed = append(res.suppressed, e.Path)
		}
	}
	return res
}

func rmEv(p string) Event     { return Event{Path: p, Op: OpRemove, At: deleteT0} }
func renameEv(p string) Event { return Event{Path: p, Op: OpRename, At: deleteT0} }
func createEv(p string) Event { return Event{Path: p, Op: OpCreate, At: deleteT0} }

func (g *DeleteGuard) markCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.marks)
}

func TestDeleteGuard_Marks(t *testing.T) {
	inRoot := func(r string) string { return filepath.Join(r, "a", "p") }
	tests := []struct {
		name      string
		inner     LocalRemover
		isDir     bool
		path      func(root string) string
		advance   time.Duration
		forget    bool
		removeTwo bool
		query     string
		want      bool
		wantMarks int // marks left after the removal(s), before query
	}{
		{name: "directory removed", inner: osRemover{}, isDir: true, path: inRoot, query: "a/p", want: true, wantMarks: 1},
		{name: "file is never marked", inner: osRemover{}, path: inRoot, query: "a/p", wantMarks: 0},
		{name: "repeat removal of an absent dir keeps the mark", inner: osRemover{}, isDir: true,
			path: inRoot, removeTwo: true, query: "a/p", want: true, wantMarks: 1},
		{name: "other path", inner: osRemover{}, isDir: true, path: inRoot, query: "a/q", wantMarks: 1},
		{name: "expired", inner: osRemover{}, isDir: true, path: inRoot, advance: guardTTL + time.Second,
			query: "a/p", wantMarks: 1},
		{name: "forgotten", inner: osRemover{}, isDir: true, path: inRoot, forget: true, query: "a/p"},
		{name: "failed removal not marked", inner: removerFunc(func(string) error { return errors.New("boom") }),
			isDir: true, path: inRoot, query: "a/p"},
		{name: "outside root not tracked", inner: osRemover{}, isDir: true,
			path:  func(r string) string { return filepath.Join(filepath.Dir(r), "outside-"+filepath.Base(r)) },
			query: "../outside", wantMarks: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			clk := newGuardClock()
			g := NewDeleteGuard(root, tt.inner, guardTTL, clk.now)
			p := tt.path(root)
			if tt.isDir {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_ = g.Remove(p)
			if tt.removeTwo {
				if err := g.Remove(p); err == nil {
					t.Fatal("second removal of an absent path should fail")
				}
			}
			clk.advance(tt.advance)
			if tt.forget {
				g.Forget(tt.query)
			}
			if got := g.markCount(); got != tt.wantMarks && !tt.forget && tt.name != "failed removal not marked" {
				t.Errorf("marks = %d, want %d", got, tt.wantMarks)
			}
			if got := g.Expected(tt.query); got != tt.want {
				t.Errorf("Expected = %v, want %v", got, tt.want)
			}
			if tt.want && g.Expected(tt.query) {
				t.Error("mark must be consumed by the first match")
			}
		})
	}
}

// The real trasher leaves no mark for a trashed file, and one for a removed
// directory (the only shape of echo that production emits as OpRemove).
func TestDeleteGuard_RealTrasher(t *testing.T) {
	te := newTrashEnv(t, nil, nil)
	g := NewDeleteGuard(te.root, te.tr, guardTTL, newGuardClock().now)
	createLocalFile(t, te.root, "d/f.txt", deleteT0)
	if err := g.Remove(filepath.Join(te.root, "d", "f.txt")); err != nil {
		t.Fatalf("trash file: %v", err)
	}
	if !fileExists(filepath.Join(te.home, "files", "f.txt")) {
		t.Fatal("file not trashed")
	}
	if n := g.markCount(); n != 0 {
		t.Errorf("marks after trashing a file = %d, want 0", n)
	}
	if err := g.Remove(filepath.Join(te.root, "d")); err != nil {
		t.Fatalf("rmdir: %v", err)
	}
	if !g.Expected("d") {
		t.Error("removed directory not marked")
	}
}

// Puller: a Drive deletion trashes local files and removes subdirectories and
// the directory itself; none of the resulting watcher events may reach Drive.
func TestPullerDelete_NotEchoed(t *testing.T) {
	del := func(p string) Change { return Change{Path: p, Deleted: true} }
	tests := []struct {
		name     string
		files    []string
		changes  []Change
		fileEvs  []string // trashed files: unpaired renames
		dirEvs   []string // removed directories: removes
		wantSupp int
	}{
		{name: "file", files: []string{"d/f.txt"}, changes: []Change{del("d/f.txt")},
			fileEvs: []string{"d/f.txt"}},
		{name: "directory tree", files: []string{"d/a.txt", "d/s/b.txt"},
			changes: []Change{del("d/a.txt"), del("d/s/b.txt"), del("d/s"), del("d")},
			fileEvs: []string{"d/a.txt", "d/s/b.txt"}, dirEvs: []string{"d/s", "d"}, wantSupp: 2},
		{name: "folder deletion only", files: []string{"d/a.txt", "d/s/b.txt"}, changes: []Change{del("d")},
			fileEvs: []string{"d/a.txt", "d/s/b.txt"}, dirEvs: []string{"d/s", "d"}, wantSupp: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTrashEnv(t, nil, nil)
			g := NewDeleteGuard(te.root, te.tr, guardTTL, newGuardClock().now)
			st := newMockStore()
			for _, f := range tt.files {
				createLocalFile(t, te.root, f, deleteT0)
				_ = st.Put(context.Background(), JournalEntry{
					Path: f, LocalMtime: deleteT0, RemoteMtime: deleteT0, RemoteMD5: "m"})
			}
			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{Items: tt.changes, NextPageToken: "token-2"}
			p := NewPuller(PullerConfig{Remover: g, Interval: 30 * time.Second, LocalRoot: te.root,
				ConflictPolicy: PolicyNewerWins}, remote, st, newMockAuditLogger(),
				newMockPublisher(), newDiscardLogger(), fixedClock(deleteT0))
			if err := p.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v", err)
			}
			var evs []Event
			for _, f := range tt.fileEvs {
				if fileExists(filepath.Join(te.root, f)) {
					t.Fatalf("precondition: %s not removed locally", f)
				}
				evs = append(evs, renameEv(f))
			}
			for _, d := range tt.dirEvs {
				if fileExists(filepath.Join(te.root, d)) {
					t.Fatalf("precondition: %s not removed locally", d)
				}
				evs = append(evs, rmEv(d))
			}
			res := pushEvents(t, te.root, g, evs...)
			if len(res.deletes) != 0 {
				t.Errorf("echoed to Drive: %v", res.deletes)
			}
			if len(res.suppressed) != tt.wantSupp {
				t.Errorf("remove_suppressed audit = %v, want %d", res.suppressed, tt.wantSupp)
			}
			if n := g.markCount(); n != 0 {
				t.Errorf("%d marks left after the echoes", n)
			}
		})
	}
}

// Bisync: the file trashed because it was removed on Drive, and the parent
// directories it empties, are not echoed either.
func TestBisyncDelete_NotEchoed(t *testing.T) {
	const rel = "d/sub/f.txt"
	te := newTrashEnv(t, nil, nil)
	g := NewDeleteGuard(te.root, te.tr, guardTTL, newGuardClock().now)
	b, _, remote, journal, _, _ := newTestBisync(t, te.root, false)
	b.cfg.Remover = g
	seedBystander(t, te.root, remote, journal)
	createLocalFile(t, te.root, rel, deleteT0)
	seedBaseline(journal, rel)

	b.execute(context.Background())

	if fileExists(filepath.Join(te.root, "d")) {
		t.Fatal("precondition: d not removed")
	}
	res := pushEvents(t, te.root, g, renameEv(rel), rmEv("d/sub"), rmEv("d"))
	if len(res.deletes) != 0 {
		t.Errorf("echoed to Drive: %v", res.deletes)
	}
	if len(res.suppressed) != 2 {
		t.Errorf("remove_suppressed audit = %v, want d/sub and d", res.suppressed)
	}
}

// Genuine user deletions are still pushed, including right after the agent
// touched the same path.
func TestUserDelete_StillPushed(t *testing.T) {
	tests := []struct {
		name         string
		agentRemoves []string // relative paths the agent removes first (dir "d", file "f.txt")
		agentWrites  string   // dir the agent re-creates without any watcher event
		advance      time.Duration
		events       []Event
		wantPushes   int
	}{
		{name: "never touched by the agent", events: []Event{rmEv("d")}, wantPushes: 1},
		{name: "agent removed dir, echo then user recreate and remove",
			agentRemoves: []string{"d"}, events: []Event{rmEv("d"), createEv("d"), rmEv("d")}, wantPushes: 1},
		{name: "agent removed dir, user recreated and removed before the echo",
			agentRemoves: []string{"d"}, events: []Event{createEv("d"), rmEv("d")}, wantPushes: 1},
		{name: "agent removed dir, user removes after the window",
			agentRemoves: []string{"d"}, advance: guardTTL + time.Second, events: []Event{rmEv("d")}, wantPushes: 1},
		{name: "agent removed dir, agent re-created it (event dropped as self-induced), user removes",
			agentRemoves: []string{"d"}, agentWrites: "d", events: []Event{rmEv("d")}, wantPushes: 1},
		{name: "agent trashed file, agent re-downloaded it, user deletes",
			agentRemoves: []string{"f.txt"}, events: []Event{renameEv("f.txt"), rmEv("f.txt")}, wantPushes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTrashEnv(t, nil, nil)
			clk := newGuardClock()
			g := NewDeleteGuard(te.root, te.tr, guardTTL, clk.now)
			if err := os.MkdirAll(filepath.Join(te.root, "d"), 0o755); err != nil {
				t.Fatal(err)
			}
			createLocalFile(t, te.root, "f.txt", deleteT0)
			for _, r := range tt.agentRemoves {
				if err := g.Remove(filepath.Join(te.root, r)); err != nil {
					t.Fatalf("agent remove %s: %v", r, err)
				}
			}
			if tt.agentWrites != "" {
				noteLocalWrite(g, filepath.Join(te.root, tt.agentWrites, "x.txt"))
			}
			clk.advance(tt.advance)
			if got := pushEvents(t, te.root, g, tt.events...); len(got.deletes) != tt.wantPushes {
				t.Errorf("deletes = %v, want %d", got.deletes, tt.wantPushes)
			}
		})
	}
}

// End to end through the real puller: the agent removes d, Drive restores a
// file in d and the puller re-downloads it (no push event, the watcher drops
// it as self-induced), then the user deletes it and d: pushed.
func TestPullerRedownload_UserDeletePushed(t *testing.T) {
	te := newTrashEnv(t, nil, nil)
	g := NewDeleteGuard(te.root, te.tr, guardTTL, newGuardClock().now)
	if err := os.MkdirAll(filepath.Join(te.root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := g.Remove(filepath.Join(te.root, "d")); err != nil {
		t.Fatal(err)
	}
	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{NextPageToken: "token-2", Items: []Change{{
		Path: "d/x.txt", Object: &RemoteObject{Path: "d/x.txt", Size: 1, MD5: "m", ModTime: deleteT0, RemoteID: "f1"}}}}
	p := NewPuller(PullerConfig{Remover: g, Interval: 30 * time.Second, LocalRoot: te.root,
		ConflictPolicy: PolicyNewerWins}, remote, newMockStore(), newMockAuditLogger(),
		newMockPublisher(), newDiscardLogger(), fixedClock(deleteT0))
	if err := p.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if !fileExists(filepath.Join(te.root, "d", "x.txt")) {
		t.Fatal("precondition: file not re-downloaded")
	}
	if err := os.RemoveAll(filepath.Join(te.root, "d")); err != nil {
		t.Fatal(err)
	}
	if got := pushEvents(t, te.root, g, rmEv("d/x.txt"), rmEv("d")); len(got.deletes) != 2 {
		t.Errorf("deletes = %v, want the user's 2", got.deletes)
	}
}

// The guard is shared by the puller, bisync and every push worker.
func TestDeleteGuard_Concurrent(t *testing.T) {
	root := t.TempDir()
	clk := newGuardClock()
	g := NewDeleteGuard(root, osRemover{}, guardTTL, clk.now)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				d := filepath.Join(root, "d", string(rune('a'+(i+w)%5)))
				_ = os.MkdirAll(d, 0o755)
				_ = g.Remove(d)
				rel := "d/" + string(rune('a'+(i+w)%5))
				g.Expected(rel)
				if i%7 == 0 {
					g.Forget(rel)
					noteLocalWrite(g, filepath.Join(d, "x"))
				}
				if i%50 == 0 {
					clk.advance(guardTTL + time.Second) // force the prune path
				}
			}
		}(w)
	}
	wg.Wait()

	// Only 5 distinct paths were ever marked.
	if n := g.markCount(); n > 5 {
		t.Errorf("marks = %d, want <= 5 distinct paths", n)
	}
	// The guard still works after the hammering: a fresh removal is marked
	// once and consumed once.
	fresh := filepath.Join(root, "fresh")
	if err := os.Mkdir(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := g.Remove(fresh); err != nil {
		t.Fatal(err)
	}
	if !g.Expected("fresh") || g.Expected("fresh") {
		t.Error("fresh mark must be expected exactly once")
	}
}

// Bisync's own downloads re-create directories without any watcher event
// reaching the push side, so they must clear marks (noteLocalWrite).
func TestBisyncWrites_ClearGuardMarks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string, remote *mockRemoteFS, journal *mockJournal)
	}{
		{"pull of a remote-only file", func(t *testing.T, root string, remote *mockRemoteFS, journal *mockJournal) {
			remote.Seed("d/x.txt", deleteT0, "m0")
		}},
		{"remote wins conflict", func(t *testing.T, root string, remote *mockRemoteFS, journal *mockJournal) {
			createLocalFile(t, root, "d/x.txt", deleteT0.Add(time.Hour))
			remote.Seed("d/x.txt", deleteT0.Add(2*time.Hour), "new")
			journal.Seed(JournalEntry{Path: "d/x.txt", LocalMtime: deleteT0, RemoteMtime: deleteT0,
				RemoteMD5: "old", LastOrigin: "local"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			g := NewDeleteGuard(root, osRemover{}, guardTTL, newGuardClock().now)
			b, _, remote, journal, _, _ := newTestBisync(t, root, false)
			b.cfg.Remover = g
			seedBystander(t, root, remote, journal)
			tt.setup(t, root, remote, journal)
			g.mark("d") // as if the agent had just removed d

			b.execute(context.Background())

			if !fileExists(filepath.Join(root, "d", "x.txt")) {
				t.Fatal("precondition: file not written")
			}
			if g.Expected("d") {
				t.Error("mark on d survived a bisync write into it")
			}
		})
	}
}
