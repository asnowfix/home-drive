package syncer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// flipGate passes the first okChecks checks, then refuses: the disk dropping
// at a chosen point of a cycle.
type flipGate struct {
	mu       sync.Mutex
	okChecks int
}

func (g *flipGate) Check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.okChecks > 0 {
		g.okChecks--
		return nil
	}
	return ErrRootBlocked
}

func TestPuller_RootGate_DiskDropsMidCycle_TokenNotPersisted(t *testing.T) {
	tests := []struct {
		name    string
		changes []Change
	}{
		{name: "with_changes", changes: []Change{
			{Path: "new.txt", Object: &RemoteObject{Path: "new.txt", ModTime: subtreeT0, RemoteID: "id-new", MD5: "n"}}}},
		{name: "no_changes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remote := newMockRemoteFS()
			remote.startToken = "start-1"
			remote.changes["start-1"] = Changes{NextPageToken: "token-2", Items: tc.changes}
			st := newMockStore()
			gate := &flipGate{okChecks: 1} // passes at the top of the cycle only
			p := NewPuller(PullerConfig{Remover: osRemover{}, LocalRoot: t.TempDir(), Gate: gate},
				remote, st, newMockAuditLogger(), newMockPublisher(), newDiscardLogger(), fixedClock(subtreeT0))

			if err := p.PollOnce(context.Background()); !errors.Is(err, ErrRootBlocked) {
				t.Fatalf("want ErrRootBlocked, got %v", err)
			}
			if tok, _ := st.GetPageToken(context.Background()); tok != "start-1" {
				t.Errorf("page token advanced to %q although local_root dropped mid-cycle", tok)
			}
		})
	}
}

func TestSyncer_RootGate_DropsWhileWaitingForBisyncLock(t *testing.T) {
	remote := newMockRemoteFS()
	gate := &flipGate{okChecks: 1} // the pre-lock check passes, every later one refuses
	mu := &sync.RWMutex{}
	cfg := DefaultConfig()
	cfg.LocalRoot = t.TempDir()
	s := New(cfg, remote, newMockStore(), newMockAuditLogger(), newMockPublisher(), newDiscardLogger(),
		WithSleepFunc(noSleep), WithRootGate(gate), WithBisyncMutex(mu))

	mu.Lock() // a bisync pass is running
	events := make(chan Event, 1)
	renames := make(chan DirRename, 1)
	events <- Event{Path: "b.txt", Op: OpRemove, At: time.Now()}
	go func() { time.Sleep(100 * time.Millisecond); mu.Unlock() }()
	runSyncer(t, s, events, renames, func() {})

	if n := len(remote.getDeleteCalls()); n != 0 {
		t.Errorf("remote delete after the disk dropped while waiting for the bisync lock: %d", n)
	}
}

func TestSyncer_RootGate_DirRenameDropsWhileWaitingForBisyncLock(t *testing.T) {
	remote := newMockRemoteFS()
	gate := &flipGate{okChecks: 1}
	mu := &sync.RWMutex{}
	cfg := DefaultConfig()
	cfg.LocalRoot = t.TempDir()
	st := newMockStore()
	s := New(cfg, remote, st, newMockAuditLogger(), newMockPublisher(), newDiscardLogger(),
		WithSleepFunc(noSleep), WithRootGate(gate), WithBisyncMutex(mu))

	mu.Lock()
	events := make(chan Event, 1)
	renames := make(chan DirRename, 1)
	renames <- DirRename{From: "d1", To: "d2", At: time.Now()}
	go func() { time.Sleep(100 * time.Millisecond); mu.Unlock() }()
	runSyncer(t, s, events, renames, func() {})

	if n := len(remote.getMoveDirCalls()) + len(st.getRewriteCalls()); n != 0 {
		t.Errorf("remote move or journal rewrite after the disk dropped: %d", n)
	}
}

func TestBisync_RootGate_Cases(t *testing.T) {
	newB := func(gate RootGate, mu *sync.RWMutex) (*Bisync, *mockRemoteFS) {
		remote := newMockRemoteFS()
		remote.Seed("other.txt", subtreeT0, "m")
		b, _ := NewBisync(BisyncOpts{
			Config:  BisyncConfig{Interval: time.Hour, LocalRoot: t.TempDir(), Remover: osRemover{}},
			Remote:  remote,
			Journal: newMockJournal(),
			MQTT:    newMockMQTT(),
			Clock:   newMockClock(rootGuardT0),
			Mu:      mu,
			Gate:    gate,
		})
		return b, remote
	}

	t.Run("force_run_refused", func(t *testing.T) {
		b, _ := newB(&flipGate{}, nil)
		if err := b.ForceRun(context.Background()); !errors.Is(err, ErrRootBlocked) {
			t.Errorf("ForceRun = %v, want ErrRootBlocked", err)
		}
	})
	t.Run("chain_repair_refused", func(t *testing.T) {
		b, remote := newB(&flipGate{}, nil)
		if _, err := b.RunChainRepair(context.Background(), false); !errors.Is(err, ErrRootBlocked) {
			t.Errorf("RunChainRepair = %v, want ErrRootBlocked", err)
		}
		if len(remote.getDeleteCalls())+len(remote.getMoveCalls()) != 0 {
			t.Error("remote op during a refused repair")
		}
	})
	t.Run("drops_while_waiting_for_the_lock", func(t *testing.T) {
		mu := &sync.RWMutex{}
		b, remote := newB(&flipGate{okChecks: 1}, mu)
		mu.Lock()
		go func() { time.Sleep(100 * time.Millisecond); mu.Unlock() }()
		b.execute(context.Background())
		if len(remote.downloadedFiles)+len(remote.getCopyCalls())+len(remote.getDeleteCalls()) != 0 {
			t.Error("bisync acted after the disk dropped while it waited for its lock")
		}
	})
}
