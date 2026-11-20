package syncer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// toggleGate is open until close is called: the disk dropping at a moment
// the test chooses, independent of how many times the code checks.
type toggleGate struct{ closed atomic.Bool }

func (g *toggleGate) Check() error {
	if g.closed.Load() {
		return ErrRootBlocked
	}
	return nil
}

// dropOnList closes the gate while the puller is between its top-of-cycle
// check and the page-token save.
type dropOnList struct {
	*mockRemoteFS
	gate *toggleGate
}

func (r *dropOnList) ListChanges(ctx context.Context, tok string) (Changes, error) {
	r.gate.closed.Store(true)
	return r.mockRemoteFS.ListChanges(ctx, tok)
}

func TestPuller_RootGate_DiskDropsMidCycle_TokenNotPersisted(t *testing.T) {
	remote := newMockRemoteFS()
	remote.startToken = "start-1"
	remote.changes["start-1"] = Changes{NextPageToken: "token-2", Items: []Change{
		{Path: "new.txt", Object: &RemoteObject{Path: "new.txt", ModTime: subtreeT0, RemoteID: "id-new", MD5: "n"}}}}
	gate := &toggleGate{}
	st := newMockStore()
	p := NewPuller(PullerConfig{Remover: osRemover{}, LocalRoot: t.TempDir(), Gate: gate},
		&dropOnList{mockRemoteFS: remote, gate: gate}, st, newMockAuditLogger(), newMockPublisher(),
		newDiscardLogger(), fixedClock(subtreeT0))

	if err := p.PollOnce(context.Background()); !errors.Is(err, ErrRootBlocked) {
		t.Fatalf("want ErrRootBlocked, got %v", err)
	}
	if tok, _ := st.GetPageToken(context.Background()); tok != "start-1" {
		t.Errorf("page token advanced to %q although local_root dropped mid-cycle", tok)
	}
}

// whileBisyncLockHeld runs fn with the shared bisync lock held, closes the
// gate while fn's goroutine waits on the lock, then releases it.
func whileBisyncLockHeld(mu *sync.RWMutex, gate *toggleGate, run func()) {
	mu.Lock()
	done := make(chan struct{})
	go func() { run(); close(done) }()
	time.Sleep(100 * time.Millisecond) // the worker is parked on the lock
	gate.closed.Store(true)
	mu.Unlock()
	<-done
}

func TestSyncer_RootGate_DropsWhileWaitingForBisyncLock(t *testing.T) {
	tests := []struct {
		name  string
		send  func(events chan Event, renames chan DirRename)
		moved func(r *mockRemoteFS, st *mockStore) int
	}{
		{"file_remove",
			func(e chan Event, _ chan DirRename) { e <- Event{Path: "b.txt", Op: OpRemove, At: time.Now()} },
			func(r *mockRemoteFS, _ *mockStore) int { return len(r.getDeleteCalls()) }},
		{"dir_rename",
			func(_ chan Event, d chan DirRename) { d <- DirRename{From: "d1", To: "d2", At: time.Now()} },
			func(r *mockRemoteFS, st *mockStore) int { return len(r.getMoveDirCalls()) + len(st.getRewriteCalls()) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remote, st := newMockRemoteFS(), newMockStore()
			gate, mu := &toggleGate{}, &sync.RWMutex{}
			cfg := DefaultConfig()
			cfg.LocalRoot = t.TempDir()
			s := New(cfg, remote, st, newMockAuditLogger(), newMockPublisher(), newDiscardLogger(),
				WithSleepFunc(noSleep), WithRootGate(gate), WithBisyncMutex(mu))
			events, renames := make(chan Event, 1), make(chan DirRename, 1)
			tc.send(events, renames)

			whileBisyncLockHeld(mu, gate, func() { runSyncer(t, s, events, renames, func() {}) })

			if n := tc.moved(remote, st); n != 0 {
				t.Errorf("remote op or journal change after the disk dropped while waiting for the lock: %d", n)
			}
		})
	}
}

func TestBisync_RootGate_Cases(t *testing.T) {
	newB := func(gate RootGate, mu *sync.RWMutex) (*Bisync, *mockRemoteFS, *mockMQTT) {
		remote := newMockRemoteFS()
		remote.Seed("other.txt", subtreeT0, "m")
		mq := newMockMQTT()
		b, _ := NewBisync(BisyncOpts{
			Config:  BisyncConfig{Interval: time.Hour, LocalRoot: t.TempDir(), Remover: osRemover{}},
			Remote:  remote,
			Journal: newMockJournal(),
			MQTT:    mq,
			Clock:   newMockClock(rootGuardT0),
			Mu:      mu,
			Gate:    gate,
		})
		return b, remote, mq
	}
	closed := &toggleGate{}
	closed.closed.Store(true)

	t.Run("force_run_refused", func(t *testing.T) {
		b, _, _ := newB(closed, nil)
		if err := b.ForceRun(context.Background()); !errors.Is(err, ErrRootBlocked) {
			t.Errorf("ForceRun = %v, want ErrRootBlocked", err)
		}
	})
	t.Run("chain_repair_refused", func(t *testing.T) {
		b, remote, _ := newB(closed, nil)
		if _, err := b.RunChainRepair(context.Background(), false); !errors.Is(err, ErrRootBlocked) {
			t.Errorf("RunChainRepair = %v, want ErrRootBlocked", err)
		}
		if len(remote.getDeleteCalls())+len(remote.getMoveCalls()) != 0 {
			t.Error("remote op during a refused repair")
		}
	})
	t.Run("drops_while_waiting_for_the_lock", func(t *testing.T) {
		mu, gate := &sync.RWMutex{}, &toggleGate{}
		b, remote, mq := newB(gate, mu)
		whileBisyncLockHeld(mu, gate, func() { b.execute(context.Background()) })
		if len(remote.downloadedFiles)+len(remote.getCopyCalls())+len(remote.getDeleteCalls()) != 0 {
			t.Error("bisync acted after the disk dropped while it waited for its lock")
		}
		if len(mq.Events()) != 0 {
			t.Errorf("a refused pass published events: %+v", mq.Events())
		}
	})
}
