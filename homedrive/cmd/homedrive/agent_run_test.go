package main

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
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

// TestRunPullLoop_BacksOffUnderSustainedOAuthClientMissing is the fix for
// the issue #67 PR review's blocking finding: syncer.Puller.Run() applied
// the OAuth "no client_id" backoff correctly, but the shipped binary never
// calls Run() -- it calls Agent.runPullLoop, which used to own a plain
// *time.Ticker at a fixed interval and never consulted the backoff at
// all. A test that only drove Run() in isolation passed while production
// kept polling Drive's token endpoint at full cadence through a sustained
// outage. This test drives runPullLoop itself -- the actual shipped pull
// loop -- through a sustained ErrOAuthClientMissing streak and asserts
// the real wall-clock gap between polls grows, which is the one thing the
// original PR's tests never exercised.
func TestRunPullLoop_BacksOffUnderSustainedOAuthClientMissing(t *testing.T) {
	j := newTestJournal(t)
	remote := &fakeRemoteFSWithOAuthClientMissing{fakeRemoteFS: newFakeRemoteFS()}

	const baseInterval = 25 * time.Millisecond
	a := &Agent{
		log:      slog.Default(),
		bisyncMu: &sync.RWMutex{},
		cfg: &config.Config{
			Pull: config.PullConfig{
				ChangesAPIInterval: config.Duration{Duration: baseInterval},
			},
		},
		puller: syncer.NewPuller(
			syncer.PullerConfig{Interval: baseInterval, LocalRoot: t.TempDir()},
			remote,
			store.NewJournalStore(j, slog.Default()),
			nil,
			noopPublisher{},
			slog.Default(),
			time.Now,
		),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		a.runPullLoop(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPullLoop did not return after context deadline")
	}

	calls := remote.callTimes()
	if len(calls) < 4 {
		t.Fatalf("expected at least 4 poll calls to observe cadence growth, got %d (times=%v)", len(calls), calls)
	}

	// A flat 25ms ticker would fit roughly 20 calls in a 500ms window.
	// Backing off must keep this well below that -- this is exactly the
	// count that stayed near 20 in the pre-fix code, since the Ticker
	// never consulted NextPollInterval at all.
	if len(calls) > 10 {
		t.Errorf("got %d poll calls in a 500ms window with a 25ms base interval; "+
			"want the backoff to keep this well under a flat-ticker count (~20)", len(calls))
	}

	// The gap between consecutive calls must strictly grow -- proof the
	// loop's own timer is actually being re-armed with a longer interval
	// each cycle, not just that some internal counter (oauthMissingStreak)
	// advances while the real schedule stays flat (the exact bug this
	// test exists to catch).
	gaps := make([]time.Duration, 0, len(calls)-1)
	for i := 1; i < len(calls); i++ {
		gaps = append(gaps, calls[i].Sub(calls[i-1]))
	}
	for i := 1; i < len(gaps); i++ {
		if gaps[i] <= gaps[i-1] {
			t.Errorf("gap[%d]=%v did not exceed gap[%d]=%v -- real polling cadence is not backing off (gaps=%v)",
				i, gaps[i], i-1, gaps[i-1], gaps)
		}
	}

	if got := a.puller.NextPollInterval(); got <= baseInterval {
		t.Errorf("a.puller.NextPollInterval() = %v after a sustained outage, want > base interval %v", got, baseInterval)
	}
}

// TestRunPullLoop_NoBackoffWhenOAuthClientConfigured is the negative
// control: with a healthy remote (no ErrOAuthClientMissing), runPullLoop
// must keep polling at the flat configured interval -- proving the timer
// rework didn't silently slow down the common case.
func TestRunPullLoop_NoBackoffWhenOAuthClientConfigured(t *testing.T) {
	j := newTestJournal(t)
	remote := newFakeRemoteFS() // default ListChanges: succeeds, no error

	const baseInterval = 20 * time.Millisecond
	a := &Agent{
		log:      slog.Default(),
		bisyncMu: &sync.RWMutex{},
		cfg: &config.Config{
			Pull: config.PullConfig{
				ChangesAPIInterval: config.Duration{Duration: baseInterval},
			},
		},
		puller: syncer.NewPuller(
			syncer.PullerConfig{Interval: baseInterval, LocalRoot: t.TempDir()},
			remote,
			store.NewJournalStore(j, slog.Default()),
			nil,
			noopPublisher{},
			slog.Default(),
			time.Now,
		),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	a.runPullLoop(ctx)

	if got := a.puller.NextPollInterval(); got != baseInterval {
		t.Errorf("a.puller.NextPollInterval() = %v after a healthy run, want unchanged base interval %v", got, baseInterval)
	}
	if a.lastPullUnixNano.Load() == 0 {
		t.Error("expected lastPullUnixNano to be recorded by a successful poll")
	}
}
