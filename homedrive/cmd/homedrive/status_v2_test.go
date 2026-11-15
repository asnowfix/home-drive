package main

import (
	"context"
	"log/slog"
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
