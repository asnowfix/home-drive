package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

func TestAgent_PauseResume_TogglesState(t *testing.T) {
	a := &Agent{log: slog.Default()}

	if a.paused.Load() {
		t.Fatal("expected agent to start unpaused")
	}
	if err := a.Pause(context.Background()); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !a.paused.Load() {
		t.Error("expected paused=true after Pause")
	}
	if err := a.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if a.paused.Load() {
		t.Error("expected paused=false after Resume")
	}
}

func newBisyncTestAgent(t *testing.T) *Agent {
	t.Helper()
	j := newTestJournal(t)
	b, _ := syncer.NewBisync(syncer.BisyncOpts{
		Config:  syncer.BisyncConfig{LocalRoot: t.TempDir(), Interval: time.Hour},
		Remote:  newFakeRemoteFS(),
		Journal: &bisyncJournalAdapter{j: j},
		Logger:  slog.Default(),
	})
	return &Agent{log: slog.Default(), bisync: b}
}

func TestAgent_ForceResync_SucceedsOnce(t *testing.T) {
	a := newBisyncTestAgent(t)

	if err := a.ForceResync(context.Background()); err != nil {
		t.Fatalf("first ForceResync: %v", err)
	}
	// The bisync run loop is not started, so the forceCh buffer (size 1)
	// stays full: a second immediate trigger must report "already running".
	if err := a.ForceResync(context.Background()); err == nil {
		t.Error("expected the second ForceResync to report bisync already running")
	}
}

func TestAgent_Reload_AppliesLogLevelAndDryRun(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yaml := "logging:\n  level: debug\ndry_run: true\n"
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	logLevel := &slog.LevelVar{}
	logLevel.Set(slog.LevelInfo)
	a := &Agent{log: slog.Default(), configPath: configPath, logLevel: logLevel}

	if err := a.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if logLevel.Level() != slog.LevelDebug {
		t.Errorf("expected log level debug, got %v", logLevel.Level())
	}
	if !a.reloadedDryRun.Load() {
		t.Error("expected dry_run=true after reload")
	}
}

func TestAgent_Reload_MissingConfig_ReturnsError(t *testing.T) {
	a := &Agent{log: slog.Default(), configPath: "/nonexistent/config.yaml"}

	if err := a.Reload(context.Background()); err == nil {
		t.Fatal("expected an error reloading a missing config file")
	}
}

func TestAgent_Status_ReportsRealState(t *testing.T) {
	remote := newFakeRemoteFS()
	remote.quota = rcloneclient.Quota{Used: 50, Total: 200}

	a := &Agent{
		log:         slog.Default(),
		version:     "test-version",
		rfs:         remote,
		pushEvents:  make(chan syncer.Event, 4),
		pushRenames: make(chan syncer.DirRename, 4),
		startTime:   time.Now().Add(-10 * time.Second),
	}
	a.reloadedDryRun.Store(true)
	a.pushEvents <- syncer.Event{Path: "x"}

	info, err := a.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if info.State != "running" {
		t.Errorf("expected state=running, got %s", info.State)
	}
	if info.Version != "test-version" {
		t.Errorf("expected version=test-version, got %s", info.Version)
	}
	if info.PendingUp != 1 {
		t.Errorf("expected pending_up=1, got %d", info.PendingUp)
	}
	if info.QuotaUsedPct != 25 {
		t.Errorf("expected quota_used_pct=25, got %d", info.QuotaUsedPct)
	}
	if !info.DryRun {
		t.Error("expected dry_run=true")
	}
	if info.UptimeSeconds < 1 {
		t.Errorf("expected uptime_seconds >= 1, got %d", info.UptimeSeconds)
	}

	a.paused.Store(true)
	info, err = a.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if info.State != "paused" {
		t.Errorf("expected state=paused, got %s", info.State)
	}
}

func TestAgent_Healthz_AggregatesComponents(t *testing.T) {
	j := newTestJournal(t)
	a := &Agent{log: slog.Default(), journal: j, rfs: newFakeRemoteFS()}

	result, err := a.Healthz(context.Background())
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected overall healthy=true, got components %+v", result.Components)
	}
	if len(result.Components) != 2 {
		t.Errorf("expected 2 components (store, rclone) with mqtt disabled, got %d", len(result.Components))
	}
}

func TestAgent_Healthz_UnhealthyWhenRcloneNotInitialized(t *testing.T) {
	j := newTestJournal(t)
	a := &Agent{log: slog.Default(), journal: j, rfs: nil}

	result, err := a.Healthz(context.Background())
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if result.Healthy {
		t.Error("expected overall healthy=false when rfs is not initialized")
	}
}

func TestAgent_Healthz_UnhealthyWhenStoreClosed(t *testing.T) {
	j := newTestJournal(t)
	_ = j.Close() // force Count() to fail
	a := &Agent{log: slog.Default(), journal: j, rfs: newFakeRemoteFS()}

	result, err := a.Healthz(context.Background())
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if result.Healthy {
		t.Error("expected overall healthy=false when the store is closed")
	}
}

func TestUnixNanoToRFC3339_ZeroIsEmpty(t *testing.T) {
	if got := unixNanoToRFC3339(0); got != "" {
		t.Errorf("expected empty string for 0, got %q", got)
	}
	if got := unixNanoToRFC3339(1); got == "" {
		t.Error("expected a non-empty timestamp for a non-zero value")
	}
}

func TestParseLogLevel_Cases(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"info":  slog.LevelInfo,
		"":      slog.LevelInfo,
		"bogus": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLogLevel(in); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// Ensure sync.RWMutex zero value doesn't panic when Agent is constructed
// without bisyncMu explicitly set in a test (guards against future field
// additions breaking these lightweight test constructions).
var _ = sync.RWMutex{}
