// agent_rootguard.go wires the local_root guard (issue #170) into the agent:
// construction, the first-install-only creation of local_root, the
// blocked-only mode, and the exit on recovery.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
)

// ErrRootResumed is what Run returns when local_root became the synced tree
// again after a block. The agent exits non-zero on purpose: the inotify
// watches died with the unmounted tree (or sit on the directory a mount now
// covers), and systemd's Restart=on-failure re-arms them with a fresh start.
var ErrRootResumed = errors.New("local_root is the synced tree again: exiting so the supervisor restarts the agent")

// openState creates the state directory, opens the journal and, on a first
// install, creates local_root.
func openState(cfg *config.Config, log *slog.Logger) (*store.Journal, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.State.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	journal, err := store.OpenJournal(cfg.State.Path, log)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	if err := ensureLocalRoot(cfg.LocalRoot, journal); err != nil {
		_ = journal.Close()
		return nil, err
	}
	return journal, nil
}

// ensureLocalRoot creates local_root for a first install (empty journal). A
// populated journal means the tree existed: a missing root is the guard's
// business to report, and creating an empty directory (on the root
// filesystem when the data disk is absent) would hide the failure.
func ensureLocalRoot(root string, j *store.Journal) error {
	n, err := j.Count()
	if err != nil {
		return fmt.Errorf("count journal: %w", err)
	}
	if n > 0 {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create local root: %w", err)
	}
	return nil
}

// newRootGuard builds the local_root guard; its OnResume stops the agent.
func (a *Agent) newRootGuard(j *store.Journal, auditor *store.Auditor, pub syncer.Publisher) *syncer.RootGuard {
	return syncer.NewRootGuard(syncer.RootGuardConfig{
		Root:     a.cfg.LocalRoot,
		Identity: j,
		Auditor:  auditor,
		MQTT:     pub,
		Log:      a.log,
		DryRun:   a.cfg.DryRun,
		OnResume: a.onRootResumed,
	})
}

// onRootResumed asks Run to shut down; Run then returns ErrRootResumed.
func (a *Agent) onRootResumed() {
	a.rootResumed.Store(true)
	if a.cancelRun != nil {
		a.cancelRun()
	}
}

// runRootRecheck is the whole agent while local_root is blocked at startup:
// it re-checks every interval until the guard passes, which stops the agent
// via onRootResumed.
func (a *Agent) runRootRecheck(ctx context.Context) {
	interval := a.cfg.Pull.ChangesAPIInterval.Duration
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = a.rootGuard.Check()
		}
	}
}
