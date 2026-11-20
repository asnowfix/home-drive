package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #154: deletions homedrive performs itself must not be echoed back to
// Drive as push `remove` ops, while genuine user deletions still are.

const guardTTL = 30 * time.Second

type guardClock struct{ t time.Time }

func (c *guardClock) now() time.Time { return c.t }

func newGuardClock() *guardClock { return &guardClock{t: deleteT0} }

type removerFunc func(string) error

func (f removerFunc) Remove(p string) error { return f(p) }

// pushRemoves feeds the events to a push syncer wired with g and returns the
// paths it asked Drive to delete.
func pushRemoves(t *testing.T, root string, g *DeleteGuard, evs ...Event) []string {
	t.Helper()
	remote := newMockRemoteFS()
	s := New(Config{LocalRoot: root, Workers: 1, Retry: DefaultRetryConfig()}, remote, newMockStore(),
		newMockAuditLogger(), newMockPublisher(), newDiscardLogger(),
		WithSleepFunc(noSleep), WithDeleteGuard(g))
	for _, ev := range evs {
		s.handleEvent(context.Background(), ev)
	}
	return append([]string(nil), remote.getDeleteCalls()...)
}

func rmEv(p string) Event { return Event{Path: p, Op: OpRemove, At: deleteT0} }

func TestDeleteGuard_Unit(t *testing.T) {
	failing := removerFunc(func(string) error { return errors.New("boom") })
	inRoot := func(r string) string { return filepath.Join(r, "a", "f") }
	tests := []struct {
		name    string
		inner   LocalRemover
		path    func(root string) string
		advance time.Duration
		forget  bool
		twice   bool
		query   string
		want    bool
	}{
		{"match", osRemover{}, inRoot, 0, false, false, "a/f", true},
		{"repeat removal of an absent path keeps the mark", osRemover{}, inRoot, 0, false, true, "a/f", true},
		{"other path", osRemover{}, inRoot, 0, false, false, "a/g", false},
		{"expired", osRemover{}, inRoot, guardTTL + time.Second, false, false, "a/f", false},
		{"forgotten", osRemover{}, inRoot, 0, true, false, "a/f", false},
		{"failed removal not marked", failing, inRoot, 0, false, false, "a/f", false},
		{"outside root ignored", osRemover{}, func(r string) string { return filepath.Join(filepath.Dir(r), "x") },
			0, false, false, "../x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			clk := newGuardClock()
			g := NewDeleteGuard(root, tt.inner, guardTTL, clk.now)
			p := tt.path(root)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("x"), 0o644)
			_ = g.Remove(p)
			if tt.twice {
				_ = g.Remove(p) // fails: already gone
			}
			clk.t = clk.t.Add(tt.advance)
			if tt.forget {
				g.Forget(tt.query)
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

// Puller: a Drive deletion removes local files, subdirectories and the
// directory itself; none of the resulting watcher events may reach Drive.
func TestPullerDelete_NotEchoed(t *testing.T) {
	del := func(p string) Change { return Change{Path: p, Deleted: true} }
	tests := []struct {
		name    string
		files   []string
		changes []Change
		events  []string
	}{
		{"file", []string{"d/f.txt"}, []Change{del("d/f.txt")}, []string{"d/f.txt"}},
		{"directory tree", []string{"d/a.txt", "d/s/b.txt"},
			[]Change{del("d/a.txt"), del("d/s/b.txt"), del("d/s"), del("d")},
			[]string{"d/a.txt", "d/s/b.txt", "d/s", "d"}},
		{"folder deletion only", []string{"d/a.txt", "d/s/b.txt"}, []Change{del("d")},
			[]string{"d/a.txt", "d/s/b.txt", "d/s", "d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			g := NewDeleteGuard(root, osRemover{}, guardTTL, newGuardClock().now)
			st := newMockStore()
			for _, f := range tt.files {
				createLocalFile(t, root, f, deleteT0)
				_ = st.Put(context.Background(), JournalEntry{
					Path: f, LocalMtime: deleteT0, RemoteMtime: deleteT0, RemoteMD5: "m"})
			}
			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{Items: tt.changes, NextPageToken: "token-2"}
			p := NewPuller(PullerConfig{Remover: g, Interval: 30 * time.Second, LocalRoot: root,
				ConflictPolicy: PolicyNewerWins}, remote, st, newMockAuditLogger(),
				newMockPublisher(), newDiscardLogger(), fixedClock(deleteT0))
			if err := p.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v", err)
			}
			last := filepath.Join(root, filepath.FromSlash(tt.events[len(tt.events)-1]))
			if _, err := os.Stat(last); !os.IsNotExist(err) {
				t.Fatalf("precondition: %s not removed locally (err=%v)", last, err)
			}
			var evs []Event
			for _, e := range tt.events {
				evs = append(evs, rmEv(e))
			}
			if got := pushRemoves(t, root, g, evs...); len(got) != 0 {
				t.Errorf("echoed to Drive: %v", got)
			}
		})
	}
}

// Bisync: local deletions of files removed on Drive, and of the parent
// directories they empty, are not echoed either.
func TestBisyncDelete_NotEchoed(t *testing.T) {
	const rel = "d/sub/f.txt"
	root := t.TempDir()
	g := NewDeleteGuard(root, osRemover{}, guardTTL, newGuardClock().now)
	b, _, remote, journal, _, _ := newTestBisync(t, root, false)
	b.cfg.Remover = g
	seedBystander(t, root, remote, journal)
	createLocalFile(t, root, rel, deleteT0)
	seedBaseline(journal, rel)

	b.execute(context.Background())

	if _, err := os.Lstat(filepath.Join(root, "d")); !os.IsNotExist(err) {
		t.Fatalf("precondition: d not removed (err=%v)", err)
	}
	if got := pushRemoves(t, root, g, rmEv(rel), rmEv("d/sub"), rmEv("d")); len(got) != 0 {
		t.Errorf("echoed to Drive: %v", got)
	}
}

// Genuine user deletions are still pushed, including right after the agent
// touched the same path.
func TestUserDelete_StillPushed(t *testing.T) {
	setup := func(t *testing.T) (string, *DeleteGuard, *guardClock) {
		root := t.TempDir()
		clk := newGuardClock()
		g := NewDeleteGuard(root, osRemover{}, guardTTL, clk.now)
		createLocalFile(t, root, "d/f.txt", deleteT0)
		return root, g, clk
	}
	create := Event{Path: "d/f.txt", Op: OpCreate, At: deleteT0}
	t.Run("never touched by the agent", func(t *testing.T) {
		root, g, _ := setup(t)
		if got := pushRemoves(t, root, g, rmEv("d/f.txt")); len(got) != 1 {
			t.Errorf("deletes = %v, want 1", got)
		}
	})
	t.Run("agent deleted, user recreated and deleted again", func(t *testing.T) {
		root, g, _ := setup(t)
		_ = g.Remove(filepath.Join(root, "d", "f.txt"))
		// agent's own echo, then the user's create and delete
		got := pushRemoves(t, root, g, rmEv("d/f.txt"), create, rmEv("d/f.txt"))
		if len(got) != 1 {
			t.Errorf("deletes = %v, want exactly the user's one", got)
		}
	})
	t.Run("agent deleted, user recreated and deleted before echo arrived", func(t *testing.T) {
		root, g, _ := setup(t)
		_ = g.Remove(filepath.Join(root, "d", "f.txt"))
		got := pushRemoves(t, root, g, create, rmEv("d/f.txt"))
		if len(got) != 1 {
			t.Errorf("deletes = %v, want 1", got)
		}
	})
	t.Run("user deletion after the window", func(t *testing.T) {
		root, g, clk := setup(t)
		_ = g.Remove(filepath.Join(root, "d", "f.txt"))
		clk.t = clk.t.Add(guardTTL + time.Second)
		if got := pushRemoves(t, root, g, rmEv("d/f.txt")); len(got) != 1 {
			t.Errorf("deletes = %v, want 1", got)
		}
	})
}
