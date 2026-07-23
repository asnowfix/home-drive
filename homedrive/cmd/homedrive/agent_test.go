package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

// newIntegrationTestAgent builds a fully-wired Agent using the exact same
// build* steps newAgent uses, but backed by a fakeRemoteFS instead of a
// real rclone remote and with MQTT disabled -- exercising the real wiring
// end to end (watcher -> pump -> push syncer -> pull loop -> bisync ->
// HTTP server) without ever touching Google Drive or a real broker, per
// the homedrive-test-mocks skill.
func newIntegrationTestAgent(t *testing.T) (*Agent, *fakeRemoteFS) {
	t.Helper()

	cfg := &config.Config{
		LocalRoot: t.TempDir(),
		Watcher: config.WatcherConfig{
			Debounce:            config.Duration{Duration: 20 * time.Millisecond},
			DirRenamePairWindow: config.Duration{Duration: 50 * time.Millisecond},
		},
		Push: config.PushConfig{
			Workers: 2,
			Retry: config.RetryConfig{
				MaxAttempts:    1,
				InitialBackoff: config.Duration{Duration: time.Millisecond},
				MaxBackoff:     config.Duration{Duration: time.Millisecond},
			},
		},
		Pull: config.PullConfig{
			// Long enough to not interfere with the push-focused tests
			// below; the pull loop itself is covered by agent_run_test.go.
			ChangesAPIInterval: config.Duration{Duration: time.Hour},
			BisyncInterval:     config.Duration{Duration: time.Hour},
		},
		State: config.StateConfig{Path: filepath.Join(t.TempDir(), "state.db")},
		HTTP:  config.HTTPConfig{Listen: "127.0.0.1:0"},
	}

	journal, err := store.OpenJournal(cfg.State.Path, slog.Default())
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	remote := newFakeRemoteFS()

	a := &Agent{
		cfg:         cfg,
		version:     "test",
		log:         slog.Default(),
		journal:     journal,
		rfs:         remote,
		bisyncMu:    &sync.RWMutex{},
		pushEvents:  make(chan syncer.Event, 16),
		pushRenames: make(chan syncer.DirRename, 16),
		startTime:   time.Now(),
	}
	a.reloadedDryRun.Store(cfg.DryRun)

	if err := a.buildWatcher(); err != nil {
		t.Fatalf("build watcher: %v", err)
	}
	a.buildPushSyncer(noopPublisher{}, nil)
	a.buildPuller(noopPublisher{}, nil)
	a.buildBisync(noopPublisher{}, nil)
	if err := a.buildHTTPServer(); err != nil {
		t.Fatalf("build http server: %v", err)
	}

	return a, remote
}

// runAgentInBackground starts a.Run in a goroutine and registers a cleanup
// that cancels ctx and waits (bounded) for Run to return, failing the test
// if shutdown does not complete in time.
func runAgentInBackground(t *testing.T, a *Agent) (ctx context.Context, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancel = context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("agent did not shut down within 5s of cancellation")
		}
	})
	return ctx, cancel, runDone
}

// TestAgent_LocalEditPushesToRemote proves the watcher -> pump -> push
// syncer wiring: a local file write under local_root results in a push to
// the (fake) remote within debounce + worker latency. Skipped on macOS
// only for large directory-rename scenarios elsewhere in this repo; a
// plain file write/debounce/push cycle works via fsnotify on both
// inotify (Linux) and FSEvents (macOS), so this test runs everywhere.
func TestAgent_LocalEditPushesToRemote(t *testing.T) {
	a, remote := newIntegrationTestAgent(t)
	filePath := filepath.Join(a.cfg.LocalRoot, "hello.txt")

	runAgentInBackground(t, a)

	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !remote.HasCopiedFrom(filePath) {
		select {
		case <-tick.C:
			// Retry the write on every tick: the watcher's initial
			// directory walk may not have completed by the time the
			// first write happens, so a single write can race the
			// watch registration. This avoids a blind time.Sleep.
			if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the local edit to be pushed to the remote")
		}
	}
}

// TestAgent_GracefulShutdown_DrainsInFlightJob proves that a shutdown
// signal arriving mid-job does not abort the in-flight push: Run must
// block until the job completes, then shut down cleanly.
func TestAgent_GracefulShutdown_DrainsInFlightJob(t *testing.T) {
	a, remote := newIntegrationTestAgent(t)
	remote.copyGate = make(chan struct{})
	remote.copyBlockedCh = make(chan struct{}, 1)
	// Safety net: if an assertion below fails early, this guarantees the
	// blocked CopyFile call (and therefore Agent.Run) can still unblock
	// and return, instead of hanging the whole test binary.
	t.Cleanup(remote.releaseGate)

	filePath := filepath.Join(a.cfg.LocalRoot, "slow.txt")

	_, cancel, runDone := runAgentInBackgroundNoAutoCancel(t, a)

	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
waitBlocked:
	for {
		select {
		case <-remote.copyBlockedCh:
			break waitBlocked
		case <-tick.C:
			if err := os.WriteFile(filePath, []byte("slow"), 0o644); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the push worker to start the in-flight job")
		}
	}

	// Shutdown fires while the worker is still blocked inside CopyFile.
	cancel()

	select {
	case <-runDone:
		t.Fatal("Run() returned before the in-flight job was allowed to complete")
	case <-time.After(150 * time.Millisecond):
		// Good: Run() is still draining.
	}

	remote.releaseGate() // let the blocked CopyFile call finish

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after the in-flight job completed")
	}

	if !remote.HasCopiedFrom(filePath) {
		t.Error("expected the in-flight job to have completed and copied the file before shutdown finished")
	}
}

// runAgentInBackgroundNoAutoCancel is like runAgentInBackground but leaves
// cancellation to the caller (needed to control the exact shutdown timing
// relative to the in-flight job).
func runAgentInBackgroundNoAutoCancel(t *testing.T, a *Agent) (context.Context, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	// The test itself drives cancellation timing and drains runDone; the
	// cleanup only guarantees cancel() has fired so no goroutine leaks
	// past the test even if an assertion fails early.
	t.Cleanup(cancel)
	return ctx, cancel, runDone
}

func TestBuildAuditLog_Disabled_ReturnsNils(t *testing.T) {
	cfg := &config.Config{} // State.AuditLog is empty

	adapter, raw, f, err := buildAuditLog(cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildAuditLog: %v", err)
	}
	if adapter != nil || raw != nil || f != nil {
		t.Errorf("expected all nils when audit_log is unset, got adapter=%v raw=%v f=%v", adapter, raw, f)
	}
}

func TestBuildAuditLog_Enabled_OpensFile(t *testing.T) {
	cfg := &config.Config{
		State: config.StateConfig{AuditLog: filepath.Join(t.TempDir(), "nested", "audit.jsonl")},
	}

	adapter, raw, f, err := buildAuditLog(cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildAuditLog: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	if adapter == nil || raw == nil || f == nil {
		t.Fatal("expected non-nil adapter, raw writer, and file")
	}
	if err := adapter.Log(syncer.AuditEntry{Op: "push"}); err != nil {
		t.Fatalf("adapter.Log: %v", err)
	}
}

func TestAgent_CloseResources_ClosesJournalAndAuditFile(t *testing.T) {
	j := newTestJournal(t)
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	f, err := os.OpenFile(auditPath, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}

	a := &Agent{log: slog.Default(), journal: j, auditFile: f}
	a.closeResources()

	// A second Get after Close should fail because the underlying bolt DB
	// is closed -- proving closeResources actually closed the journal.
	if _, err := j.Get("anything"); err == nil {
		t.Error("expected journal operations to fail after closeResources")
	}
	if _, err := f.WriteString("x"); err == nil {
		t.Error("expected audit file writes to fail after closeResources")
	}
}
