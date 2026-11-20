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

// rootAway moves a healthy, established local_root out of the way, as an
// unmounted data disk would, and returns a func that brings it back.
func rootAway(t *testing.T, e *rootGuardEnv, mode string) (restore func()) {
	t.Helper()
	disk := e.root + ".disk"
	if err := os.Rename(e.root, disk); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "missing":
	case "dangling":
		if err := os.Symlink(filepath.Join(disk, "gone"), e.root); err != nil {
			t.Fatal(err)
		}
	case "empty_dir":
		e.mkRoot(t)
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	return func() {
		_ = os.RemoveAll(e.root)
		if err := os.Rename(disk, e.root); err != nil {
			t.Fatal(err)
		}
	}
}

func rootGateModes() []string { return []string{"missing", "dangling", "empty_dir"} }

func TestPuller_RootGate_NoActionWhileBlocked(t *testing.T) {
	for _, mode := range rootGateModes() {
		t.Run(mode, func(t *testing.T) {
			e := newRootGuardEnv(t, false)
			e.establish(t)
			restore := rootAway(t, e, mode)

			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{
				NextPageToken: "token-2",
				Items: []Change{
					{Path: "new.txt", Object: &RemoteObject{Path: "new.txt", ModTime: subtreeT0, RemoteID: "id-new", MD5: "n"}},
					{Path: "a.txt", Deleted: true},
				},
			}
			st := newMockStore()
			putBaseline(t, st, "a.txt", "id-a")
			p := NewPuller(
				PullerConfig{Remover: osRemover{}, LocalRoot: e.root, Gate: e.guard},
				remote, st, newMockAuditLogger(), newMockPublisher(), newDiscardLogger(), fixedClock(subtreeT0),
			)

			err := p.PollOnce(context.Background())
			if !errors.Is(err, ErrRootBlocked) {
				t.Fatalf("want ErrRootBlocked, got %v", err)
			}
			if n := len(remote.downloadedFiles); n != 0 {
				t.Errorf("downloads while blocked: %d", n)
			}
			if _, ok, _ := st.Get(context.Background(), "a.txt"); !ok || len(st.entries) != 1 {
				t.Errorf("journal changed while blocked: %+v", st.entries)
			}
			if tok, _ := st.GetPageToken(context.Background()); tok != "" {
				t.Errorf("page token moved while blocked: %q", tok)
			}
			if mode == "missing" {
				if _, err := os.Lstat(e.root); err == nil {
					t.Error("puller created local_root while blocked")
				}
			}

			// Recovery: the disk is back, the next cycle goes through.
			restore()
			if err := p.PollOnce(context.Background()); err != nil {
				t.Fatalf("poll after recovery: %v", err)
			}
			if n := len(remote.downloadedFiles); n != 1 {
				t.Errorf("want 1 download after recovery, got %d", n)
			}
		})
	}
}

func TestSyncer_RootGate_NoActionWhileBlocked(t *testing.T) {
	for _, mode := range rootGateModes() {
		t.Run(mode, func(t *testing.T) {
			e := newRootGuardEnv(t, false)
			e.establish(t)
			restore := rootAway(t, e, mode)

			remote := newMockRemoteFS()
			remote.Seed("a.txt", subtreeT0, "m")
			st := newMockStore()
			putBaseline(t, st, "a.txt", "id-a")
			s := New(Config{LocalRoot: e.root}, remote, st, newMockAuditLogger(),
				newMockPublisher(), newDiscardLogger(), WithSleepFunc(noSleep), WithRootGate(e.guard))

			events := make(chan Event, 4)
			renames := make(chan DirRename, 1)
			events <- Event{Path: "a.txt", Op: OpRemove, At: time.Now()}
			events <- Event{Path: "b.txt", Op: OpWrite, At: time.Now()}
			renames <- DirRename{From: "d1", To: "d2", At: time.Now()}
			runSyncer(t, s, events, renames, func() {})

			if n := len(remote.getDeleteCalls()) + len(remote.getCopyCalls()) +
				len(remote.getMoveCalls()) + len(remote.getMoveDirCalls()); n != 0 {
				t.Errorf("remote ops while blocked: %d", n)
			}
			if len(st.getPuts()) != 1 || len(st.entries) != 1 || len(st.getRewriteCalls()) != 0 {
				t.Errorf("journal changed while blocked: puts=%d entries=%d rewrites=%d",
					len(st.getPuts()), len(st.entries), len(st.getRewriteCalls()))
			}

			// Recovery: a later event is pushed.
			restore()
			createLocalFile(t, e.root, "b.txt", subtreeT0)
			events = make(chan Event, 1)
			renames = make(chan DirRename)
			events <- Event{Path: "b.txt", Op: OpWrite, At: time.Now()}
			runSyncer(t, s, events, renames, func() {})
			if n := len(remote.getCopyCalls()); n != 1 {
				t.Errorf("want 1 upload after recovery, got %d", n)
			}
		})
	}
}

func TestBisync_RootGate_NoActionWhileBlocked(t *testing.T) {
	for _, mode := range rootGateModes() {
		t.Run(mode, func(t *testing.T) {
			e := newRootGuardEnv(t, false)
			e.establish(t)
			rootAway(t, e, mode)

			remote := newMockRemoteFS()
			remote.Seed("a.txt", subtreeT0, "m")
			remote.Seed("other.txt", subtreeT0, "m")
			mq := newMockMQTT()
			b, _ := NewBisync(BisyncOpts{
				Config:  BisyncConfig{Interval: time.Hour, LocalRoot: e.root, Remover: osRemover{}},
				Remote:  remote,
				Journal: e.journal,
				MQTT:    mq,
				Clock:   newMockClock(rootGuardT0),
				Mu:      &sync.RWMutex{},
				Gate:    e.guard,
			})
			b.execute(context.Background())

			if len(remote.getDeleteCalls())+len(remote.getCopyCalls())+len(remote.downloadedFiles) != 0 {
				t.Error("remote or local transfer while blocked")
			}
			if len(mq.Events()) != 0 {
				t.Errorf("bisync reported a run while blocked: %+v", mq.Events())
			}
			if !e.journal.Exists("a.txt") {
				t.Error("journal entry lost while blocked")
			}
		})
	}
}
