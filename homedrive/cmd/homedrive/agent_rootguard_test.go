package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

// newBlockedRootAgent returns an Agent whose root guard is blocked: the
// journal says a tree was synced and local_root is gone.
func newBlockedRootAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	j := newTestJournal(t)
	root := filepath.Join(t.TempDir(), "gdrive")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := j.Put(store.JournalEntry{Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := syncer.NewRootGuard(syncer.RootGuardConfig{Root: root, Identity: j, Log: slog.Default()})
	if err := g.Check(); err != nil {
		t.Fatalf("establish: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(); err == nil {
		t.Fatal("guard should be blocked")
	}
	a := &Agent{
		log: slog.Default(), journal: j, rfs: newFakeRemoteFS(), rootGuard: g,
		bisyncMu: &sync.RWMutex{},
		puller: syncer.NewPuller(
			syncer.PullerConfig{LocalRoot: root, Gate: g},
			newFakeRemoteFS(), store.NewJournalStore(j, slog.Default()),
			nil, noopPublisher{}, slog.Default(), time.Now,
		),
		pushEvents:  make(chan syncer.Event, 1),
		pushRenames: make(chan syncer.DirRename, 1),
	}
	return a, root
}

func TestAgent_Status_LocalRootBlocked(t *testing.T) {
	a, _ := newBlockedRootAgent(t)
	info, err := a.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.LocalRootBlocked == nil || info.LocalRootBlocked.Reason != syncer.RootBlockMissing ||
		info.LocalRootBlocked.Since == "" {
		t.Fatalf("status lacks the blocked reason: %+v", info.LocalRootBlocked)
	}
	if info.State != "root_blocked" {
		t.Errorf("state = %q, want root_blocked", info.State)
	}
}

func TestAgent_Status_HealthyRootOmitsBlock(t *testing.T) {
	a := &Agent{log: slog.Default(), rfs: newFakeRemoteFS()}
	info, _ := a.Status(context.Background())
	if info.LocalRootBlocked != nil || info.State != "running" {
		t.Errorf("unexpected block/state: %+v %q", info.LocalRootBlocked, info.State)
	}
}

func TestPollOnceGuarded_RootBlocked(t *testing.T) {
	a, root := newBlockedRootAgent(t)
	a.pollOnceGuarded(context.Background())
	if a.lastPullUnixNano.Load() != 0 {
		t.Error("a blocked cycle must not count as a successful pull")
	}
	if _, err := os.Lstat(root); err == nil {
		t.Error("blocked pull created local_root")
	}
}
