package main

import (
	"context"
	"encoding/json"
	"github.com/asnowfix/home-drive/homedrive/internal/mqtt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
	"github.com/asnowfix/home-drive/homedrive/internal/stats"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/pkg/homedrive"
)

func TestBuildStatusV2_Cases(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		paused bool
		setup  func(tr *stats.Tracker)
		check  func(t *testing.T, d homedrive.StatusV2)
	}{
		{"fresh_agent_has_nulls", false, func(*stats.Tracker) {}, func(t *testing.T, d homedrive.StatusV2) {
			if d.State != homedrive.StateRunning || d.LastPush != nil || d.LastActivity != nil || d.LastError != nil {
				t.Errorf("doc = %+v", d)
			}
			if len(d.Counts) != 3 {
				t.Errorf("counts windows = %d, want 3", len(d.Counts))
			}
		}},
		{"paused_state", true, func(*stats.Tracker) {}, func(t *testing.T, d homedrive.StatusV2) {
			if d.State != homedrive.StatePaused {
				t.Errorf("state = %s", d.State)
			}
		}},
		{"counts_match_tracker", false, func(tr *stats.Tracker) {
			tr.Record(syncer.Outcome{Op: syncer.OutPull, Dir: syncer.DirDown, Bytes: 99, At: now.Add(-time.Minute)})
		}, func(t *testing.T, d homedrive.StatusV2) {
			if d.Counts["24h"].BytesDown != 99 || d.LastPull == nil || d.LastActivity == nil {
				t.Errorf("doc = %+v", d)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := stats.New(stats.Config{})
			tc.setup(tr)
			a := &Agent{
				log: slog.Default(), version: "v", stats: tr, startTime: now.Add(-time.Hour),
				pushEvents: make(chan syncer.Event, 1), pushRenames: make(chan syncer.DirRename, 1),
			}
			a.paused.Store(tc.paused)
			d := a.buildStatusV2(now)
			if d.UptimeS != 3600 {
				t.Errorf("uptime_s = %d", d.UptimeS)
			}
			tc.check(t, d)
		})
	}
}

func TestStatusInterval_Cases(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"configured", 10 * time.Second, 10 * time.Second},
		{"unset_defaults", 0, 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{cfg: &config.Config{MQTT: config.MQTTConfig{PublishInterval: config.Duration{Duration: tc.in}}}}
			if got := a.statusInterval(); got != tc.want {
				t.Errorf("interval = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRunStatusLoop_FlushesAndSurvivesRestart: counters recorded while the
// loop runs are on disk when it stops, and a new tracker over the same
// journal sees them (bug (c)).
func TestRunStatusLoop_FlushesAndSurvivesRestart(t *testing.T) {
	j := newTestJournal(t)
	tr := stats.New(stats.Config{Store: j})
	a := &Agent{
		log: slog.Default(), journal: j, stats: tr, rfs: newFakeRemoteFS(),
		cfg:        &config.Config{MQTT: config.MQTTConfig{PublishInterval: config.Duration{Duration: 20 * time.Millisecond}}},
		pushEvents: make(chan syncer.Event, 1), pushRenames: make(chan syncer.DirRename, 1),
	}
	tr.Record(syncer.Outcome{Op: syncer.OutPull, Dir: syncer.DirDown, Bytes: 20 << 20, At: time.Now()})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	a.runStatusLoop(ctx)

	tr2 := stats.New(stats.Config{Store: j})
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := tr2.Snapshot(time.Now()).Counts["24h"].BytesDown; got != 20<<20 {
		t.Errorf("bytes_down after restart = %d, want %d", got, 20<<20)
	}
}

func TestRefreshQuota_CachesBetweenTicks(t *testing.T) {
	remote := newFakeRemoteFS()
	remote.quota = rcloneclient.Quota{Used: 50, Total: 200}
	a := &Agent{rfs: remote}
	now := time.Now()

	a.refreshQuota(context.Background(), now)
	if p := a.quota.get(); p == nil || *p != 25 {
		t.Fatalf("quota = %v, want 25", p)
	}
	remote.quota = rcloneclient.Quota{Used: 100, Total: 200}
	a.refreshQuota(context.Background(), now.Add(time.Minute)) // within 5 min: cached
	if p := a.quota.get(); *p != 25 {
		t.Errorf("quota refreshed too early: %v", *p)
	}
	a.refreshQuota(context.Background(), now.Add(6*time.Minute))
	if p := a.quota.get(); *p != 50 {
		t.Errorf("quota after refresh = %v, want 50", *p)
	}
}

func TestStatusGuards_Cases(t *testing.T) {
	since := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	t.Run("none_blocking", func(t *testing.T) {
		a := &Agent{log: slog.Default(), stats: stats.New(stats.Config{})}
		d := a.buildStatusV2(since)
		if d.Guards.LocalRoot != nil || d.Guards.BisyncDeletes != nil || d.State != homedrive.StateRunning {
			t.Errorf("doc = %+v", d)
		}
	})

	t.Run("root_blocked_keeps_codes_only", func(t *testing.T) {
		a, _, _ := newBlockedRootAgent(t)
		a.stats = stats.New(stats.Config{})
		d := a.buildStatusV2(since)
		if d.State != homedrive.StateRootBlocked {
			t.Errorf("state = %q, want root_blocked", d.State)
		}
		g := d.Guards.LocalRoot
		if g == nil || g.Reason != syncer.RootBlockMissing || g.Since.IsZero() {
			t.Fatalf("guards.local_root = %+v", g)
		}
		out, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		// The block's Detail carries the lstat error with the local_root
		// path: none of it may reach the document.
		if strings.Contains(string(out), a.cfg.LocalRoot) || strings.Contains(string(out), "no such file") {
			t.Errorf("document leaks a path or error text: %s", out)
		}
	})

	t.Run("delete_guard_mapping", func(t *testing.T) {
		if deleteGuardStatus(nil) != nil || rootGuardStatus(nil) != nil {
			t.Error("nil block must map to nil")
		}
		g := deleteGuardStatus(&syncer.DeleteBlock{Planned: 412, Threshold: 100, Reason: syncer.BlockThreshold, Since: since})
		if g == nil || g.Reason != "threshold" || g.Planned != 412 || g.Threshold != 100 || !g.Since.Equal(since) {
			t.Errorf("guard = %+v", g)
		}
	})
}

// fakeStatusPub is an mqtt.Publisher capturing retained status documents.
type fakeStatusPub struct {
	mu   sync.Mutex
	docs []homedrive.StatusV2
}

func (f *fakeStatusPub) Publish(_ string, _ byte, _ bool, payload any) error {
	if d, ok := payload.(homedrive.StatusV2); ok {
		f.mu.Lock()
		f.docs = append(f.docs, d)
		f.mu.Unlock()
	}
	return nil
}
func (f *fakeStatusPub) PublishJSON(string, any) error { return nil }
func (f *fakeStatusPub) Topic(parts ...string) string  { return strings.Join(parts, "/") }
func (f *fakeStatusPub) Close(context.Context) error   { return nil }

func (f *fakeStatusPub) first() (homedrive.StatusV2, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.docs) == 0 {
		return homedrive.StatusV2{}, false
	}
	return f.docs[0], true
}

// TestAgent_Run_BlockedOnlyPublishesStatus: when local_root is blocked at
// startup nothing syncs, but the status document (state root_blocked,
// guards.local_root) must still be published: that is the case schema 2.1
// exists to report.
func TestAgent_Run_BlockedOnlyPublishesStatus(t *testing.T) {
	// No prior Check(): the real startup path must evaluate the guard
	// before the first document is built.
	a, _ := newIntegrationTestAgent(t)
	if err := os.RemoveAll(a.cfg.LocalRoot); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	a.log = slog.New(slog.NewJSONHandler(logs, nil))
	a.stats = stats.New(stats.Config{})
	pub := &fakeStatusPub{}
	a.statePub = mqtt.NewStatePublisher(pub,
		func() homedrive.StatusV2 { return a.buildStatusV2(time.Now()) }, time.Minute, slog.Default())
	runAgentUntilExit(t, a)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if d, ok := pub.first(); ok {
			if d.State != homedrive.StateRootBlocked || d.Guards.LocalRoot == nil ||
				d.Guards.LocalRoot.Reason != syncer.RootBlockMissing {
				t.Fatalf("first document state=%q guards=%+v", d.State, d.Guards)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no status document published in blocked-only mode: %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// issue #191: guards.skipped_symlinks is null with no skipped link, else a
// count and since, never a path.
func TestStatusGuards_SkippedSymlinks(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	tr := syncer.NewSymlinkTracker(slog.New(slog.DiscardHandler))
	a := &Agent{log: slog.Default(), stats: stats.New(stats.Config{}), symlinks: tr}

	if g := a.buildStatusV2(now).Guards.SkippedSymlinks; g != nil {
		t.Fatalf("guards.skipped_symlinks = %+v, want nil", g)
	}
	tr.Report("secret/path/link", syncer.LinkUnresolved)
	tr.Report("other", syncer.LinkInRoot)
	d := a.buildStatusV2(now)
	g := d.Guards.SkippedSymlinks
	if g == nil || g.Count != 2 || g.Since.IsZero() {
		t.Fatalf("guards.skipped_symlinks = %+v, want count 2 with since", g)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "secret/path") {
		t.Errorf("status document leaks a path: %s", out)
	}
	if d.State != homedrive.StateRunning {
		t.Errorf("state = %q: skipped links must not change it", d.State)
	}
}
