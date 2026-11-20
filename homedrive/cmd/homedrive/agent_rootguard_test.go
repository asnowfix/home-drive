package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	httpctl "github.com/asnowfix/home-drive/homedrive/internal/http"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/internal/trash"
)

// newBlockedRootAgent returns an Agent whose root guard is blocked: the
// journal says a tree was synced and local_root is gone. disk is where the
// tree went; renaming it back to a.cfg.LocalRoot brings the disk back.
func newBlockedRootAgent(t *testing.T) (a *Agent, disk string, logs *bytes.Buffer) {
	t.Helper()
	a, _ = newIntegrationTestAgent(t)
	root := a.cfg.LocalRoot
	if err := a.journal.Put(store.JournalEntry{Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.rootGuard.Check(); err != nil {
		t.Fatalf("establish: %v", err)
	}
	disk = root + ".disk"
	if err := os.Rename(root, disk); err != nil {
		t.Fatal(err)
	}
	if err := a.rootGuard.Check(); !errors.Is(err, syncer.ErrRootBlocked) {
		t.Fatalf("guard should be blocked, got %v", err)
	}
	logs = &bytes.Buffer{}
	a.log = slog.New(slog.NewJSONHandler(logs, nil))
	return a, disk, logs
}

func TestAgent_Status_LocalRootBlocked(t *testing.T) {
	a, _, _ := newBlockedRootAgent(t)
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
	// The wire name is what #144's status v2 must keep.
	raw, _ := json.Marshal(info)
	if !strings.Contains(string(raw), `"local_root_blocked":{"reason":"missing"`) {
		t.Errorf("status JSON = %s", raw)
	}
}

func TestAgent_Status_HealthyRootOmitsBlock(t *testing.T) {
	a := &Agent{log: slog.Default(), rfs: newFakeRemoteFS()}
	info, _ := a.Status(context.Background())
	raw, _ := json.Marshal(info)
	if info.LocalRootBlocked != nil || info.State != "running" || strings.Contains(string(raw), "local_root_blocked") {
		t.Errorf("unexpected block/state: %s", raw)
	}
}

// TestAgent_RootGuardWiring fails if the guard stops being passed to the
// puller, the push syncer or bisync, or the control endpoints stop honouring it.
func TestAgent_RootGuardWiring(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, a *Agent, remote *fakeRemoteFS)
	}{
		{"puller", func(t *testing.T, a *Agent, _ *fakeRemoteFS) {
			if err := a.puller.PollOnce(context.Background()); !errors.Is(err, syncer.ErrRootBlocked) {
				t.Errorf("PollOnce = %v, want ErrRootBlocked", err)
			}
		}},
		{"push", func(t *testing.T, a *Agent, remote *fakeRemoteFS) {
			events := make(chan syncer.Event, 1)
			renames := make(chan syncer.DirRename)
			events <- syncer.Event{Path: "a.txt", Op: syncer.OpWrite, At: time.Now()}
			close(events)
			close(renames)
			a.pushSyncer.Run(context.Background(), events, renames)
			remote.mu.Lock()
			defer remote.mu.Unlock()
			if len(remote.copyCalls) != 0 {
				t.Errorf("push reached the remote while blocked: %v", remote.copyCalls)
			}
		}},
		{"force_resync", func(t *testing.T, a *Agent, _ *fakeRemoteFS) {
			if err := a.ForceResync(context.Background()); !errors.Is(err, httpctl.ErrLocalRootBlocked) {
				t.Errorf("ForceResync = %v, want ErrLocalRootBlocked", err)
			}
		}},
		{"chain_repair", func(t *testing.T, a *Agent, _ *fakeRemoteFS) {
			if _, err := a.RunChainRepair(context.Background(), false); !errors.Is(err, httpctl.ErrLocalRootBlocked) {
				t.Errorf("RunChainRepair = %v, want ErrLocalRootBlocked", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newBlockedRootAgent(t)
			tc.run(t, a, a.rfs.(*fakeRemoteFS))
		})
	}
}

func TestPollOnceGuarded_RootBlocked(t *testing.T) {
	a, _, logs := newBlockedRootAgent(t)
	a.pollOnceGuarded(context.Background())
	if a.lastPullUnixNano.Load() != 0 {
		t.Error("a blocked cycle must not count as a successful pull")
	}
	if strings.Contains(logs.String(), "poll error") {
		t.Errorf("a paused cycle must not log a poll error every interval: %s", logs.String())
	}
}

func TestAgent_PurgeTrash_SkippedWhileBlocked(t *testing.T) {
	a, disk, _ := newBlockedRootAgent(t)
	a.trasher = trash.New(trash.Config{
		PurgeAge: time.Hour, Records: a.journal, Log: slog.Default(), LocalRoot: a.cfg.LocalRoot,
	})
	rec := store.TrashRecord{Dir: filepath.Join(t.TempDir(), "gone", "Trash"), Name: "x", Path: "/p/x",
		DeletedAt: time.Now().Add(-48 * time.Hour)}
	if err := a.journal.PutTrashRecord(rec); err != nil {
		t.Fatal(err)
	}
	count := func() int { l, _ := a.journal.ListTrashRecords(); return len(l) }

	a.purgeTrash()
	if count() != 1 {
		t.Fatal("the purge dropped a trash record while local_root was blocked")
	}
	// Sensitivity: with the disk back, the same purge does run (the record's
	// directory is gone, so it is dropped).
	if err := os.Rename(disk, a.cfg.LocalRoot); err != nil {
		t.Fatal(err)
	}
	a.purgeTrash()
	if count() != 0 {
		t.Error("the purge did not run once local_root was back")
	}
}

func TestEnsureLocalRoot_Cases(t *testing.T) {
	tests := []struct {
		name      string
		populated bool
		wantMade  bool
	}{
		{name: "first_install_creates_it", wantMade: true},
		{name: "populated_journal_leaves_a_missing_root_alone", populated: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := newTestJournal(t)
			if tc.populated {
				if err := j.Put(store.JournalEntry{Path: "a.txt"}); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(t.TempDir(), "mnt", "gdrive")
			if err := ensureLocalRoot(root, j); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(root)
			if made := err == nil; made != tc.wantMade {
				t.Errorf("created = %v, want %v", made, tc.wantMade)
			}
		})
	}
}

// TestAgent_Run_BlockedOnlyThenExitsOnRecovery covers the NAS layout: the
// data disk is absent at start. The agent reports instead of crash-looping,
// starts nothing, and exits non-zero once the disk is back.
func TestAgent_Run_BlockedOnlyThenExitsOnRecovery(t *testing.T) {
	a, disk, _ := newBlockedRootAgent(t)
	a.cfg.Pull.ChangesAPIInterval = config.Duration{Duration: 20 * time.Millisecond}
	done := runAgentUntilExit(t, a)

	deadline := time.Now().Add(3 * time.Second)
	for a.localRootBlock() == nil { // Status itself races with Run's startTime write
		if time.Now().After(deadline) {
			t.Fatal("the guard never reported the block")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.lastPullUnixNano.Load() != 0 {
		t.Error("the pull loop ran in blocked-only mode")
	}

	if err := os.Rename(disk, a.cfg.LocalRoot); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRootResumed) {
			t.Errorf("Run = %v, want ErrRootResumed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not exit after local_root came back")
	}
}

// TestAgent_Run_ExitsWhenRootReturnsWhileRunning: a healthy agent whose disk
// drops and returns exits non-zero so the watcher is re-armed by a restart.
func TestAgent_Run_ExitsWhenRootReturnsWhileRunning(t *testing.T) {
	a, _ := newIntegrationTestAgent(t)
	done := runAgentUntilExit(t, a)
	time.Sleep(100 * time.Millisecond) // startup check recorded the identity

	disk := a.cfg.LocalRoot + ".disk"
	if err := os.Rename(a.cfg.LocalRoot, disk); err != nil {
		t.Fatal(err)
	}
	if err := a.rootGuard.Check(); !errors.Is(err, syncer.ErrRootBlocked) {
		t.Fatalf("want blocked, got %v", err)
	}
	if err := os.Rename(disk, a.cfg.LocalRoot); err != nil {
		t.Fatal(err)
	}
	_ = a.rootGuard.Check()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRootResumed) {
			t.Errorf("Run = %v, want ErrRootResumed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not exit after the recovery")
	}
}

// runAgentUntilExit runs a.Run and returns its result channel; unlike
// runAgentInBackground the test itself waits for Run to return.
func runAgentUntilExit(t *testing.T, a *Agent) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	return done
}
