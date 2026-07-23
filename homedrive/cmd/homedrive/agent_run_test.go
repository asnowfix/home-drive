package main

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/internal/watcher"
)

func TestToSyncerOp_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   watcher.Op
		want syncer.Op
	}{
		{"create", watcher.OpCreate, syncer.OpCreate},
		{"write", watcher.OpWrite, syncer.OpWrite},
		{"remove", watcher.OpRemove, syncer.OpRemove},
		{"rename", watcher.OpRename, syncer.OpRename},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toSyncerOp(tc.in); got != tc.want {
				t.Errorf("toSyncerOp(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func newDispatchTestAgent() *Agent {
	return &Agent{
		log:         slog.Default(),
		pushEvents:  make(chan syncer.Event, 4),
		pushRenames: make(chan syncer.DirRename, 4),
	}
}

func TestDispatchWatchEvent_ForwardsFileEvent(t *testing.T) {
	a := newDispatchTestAgent()
	ev := watcher.WatchEvent{Event: &watcher.Event{Path: "a.txt", Op: watcher.OpWrite, At: time.Now()}}

	a.dispatchWatchEvent(context.Background(), ev)

	select {
	case got := <-a.pushEvents:
		if got.Path != "a.txt" || got.Op != syncer.OpWrite {
			t.Errorf("unexpected event: %+v", got)
		}
	default:
		t.Fatal("expected an event on pushEvents")
	}
	if a.lastPushUnixNano.Load() == 0 {
		t.Error("expected lastPushUnixNano to be recorded")
	}
}

func TestDispatchWatchEvent_ForwardsDirRename(t *testing.T) {
	a := newDispatchTestAgent()
	ev := watcher.WatchEvent{DirRename: &watcher.DirRename{From: "old", To: "new", At: time.Now()}}

	a.dispatchWatchEvent(context.Background(), ev)

	select {
	case got := <-a.pushRenames:
		if got.From != "old" || got.To != "new" {
			t.Errorf("unexpected dir rename: %+v", got)
		}
	default:
		t.Fatal("expected a dir rename on pushRenames")
	}
}

func TestDispatchWatchEvent_DroppedWhilePaused(t *testing.T) {
	a := newDispatchTestAgent()
	a.paused.Store(true)
	ev := watcher.WatchEvent{Event: &watcher.Event{Path: "a.txt", Op: watcher.OpCreate, At: time.Now()}}

	a.dispatchWatchEvent(context.Background(), ev)

	select {
	case got := <-a.pushEvents:
		t.Fatalf("expected no event while paused, got %+v", got)
	default:
	}
	if a.lastPushUnixNano.Load() != 0 {
		t.Error("expected lastPushUnixNano to stay unset while paused")
	}
}

func TestDispatchWatchEvent_ResumeForwardsAgain(t *testing.T) {
	a := newDispatchTestAgent()
	a.paused.Store(true)
	a.dispatchWatchEvent(context.Background(), watcher.WatchEvent{
		Event: &watcher.Event{Path: "dropped.txt", Op: watcher.OpCreate, At: time.Now()},
	})
	a.paused.Store(false)
	a.dispatchWatchEvent(context.Background(), watcher.WatchEvent{
		Event: &watcher.Event{Path: "kept.txt", Op: watcher.OpCreate, At: time.Now()},
	})

	select {
	case got := <-a.pushEvents:
		if got.Path != "kept.txt" {
			t.Errorf("expected kept.txt, got %s", got.Path)
		}
	default:
		t.Fatal("expected the post-resume event to be forwarded")
	}
}

func TestPollOnceGuarded_BlocksDuringBisyncLock(t *testing.T) {
	j := newTestJournal(t)
	remote := newFakeRemoteFS()

	a := &Agent{
		log:      slog.Default(),
		bisyncMu: &sync.RWMutex{},
		puller: syncer.NewPuller(
			syncer.PullerConfig{LocalRoot: t.TempDir()},
			remote,
			store.NewJournalStore(j, slog.Default()),
			nil,
			noopPublisher{},
			slog.Default(),
			time.Now,
		),
	}

	a.bisyncMu.Lock() // simulate a bisync run in progress

	done := make(chan struct{})
	go func() {
		a.pollOnceGuarded(context.Background())
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("pollOnceGuarded should block while bisync holds the write lock")
	case <-time.After(50 * time.Millisecond):
	}

	a.bisyncMu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollOnceGuarded did not complete after the bisync lock was released")
	}

	if a.lastPullUnixNano.Load() == 0 {
		t.Error("expected lastPullUnixNano to be recorded after a successful poll")
	}
}
