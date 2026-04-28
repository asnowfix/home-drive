// bisync.go implements the periodic bisync safety net described in
// PLAN.md sections 7.2 and 14 (Phase 7). It performs a full directory
// diff between the local filesystem and the remote, syncing any drift
// found. It acquires a global write lock to block push/pull workers
// during execution.
package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Bisync is the periodic safety-net syncer that performs a full directory
// diff between local and remote, resolving any drift found.
type Bisync struct {
	cfg     BisyncConfig
	remote  RemoteFS
	journal Journal
	mqtt    EventPublisher // may be nil if MQTT is disabled
	audit   AuditWriter    // may be nil if audit is disabled
	clock   Clock
	log     *slog.Logger

	// mu is the global RWMutex shared with push/pull workers.
	// Bisync takes Lock(); push workers take RLock().
	mu *sync.RWMutex

	// forceCh receives signals from the /resync endpoint.
	forceCh chan struct{}

	// running tracks whether bisync is currently executing, protected
	// by runMu.
	runMu   sync.Mutex
	running bool
}

// BisyncOpts are constructor options for Bisync.
type BisyncOpts struct {
	Config  BisyncConfig
	Remote  RemoteFS
	Journal Journal
	MQTT    EventPublisher // optional
	Audit   AuditWriter    // optional
	Clock   Clock          // defaults to realClock
	Logger  *slog.Logger   // defaults to slog.Default()
	Mu      *sync.RWMutex  // shared push/bisync mutex
}

// NewBisync creates a new bisync runner. The returned ForceCh can be
// used to trigger an immediate bisync run (e.g., from /resync).
func NewBisync(opts BisyncOpts) (*Bisync, chan<- struct{}) {
	clk := opts.Clock
	if clk == nil {
		clk = realClock{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	mu := opts.Mu
	if mu == nil {
		mu = &sync.RWMutex{}
	}

	forceCh := make(chan struct{}, 1)

	b := &Bisync{
		cfg:     opts.Config,
		remote:  opts.Remote,
		journal: opts.Journal,
		mqtt:    opts.MQTT,
		audit:   opts.Audit,
		clock:   clk,
		log:     logger,
		mu:      mu,
		forceCh: forceCh,
	}
	return b, forceCh
}

// Mu returns the shared RWMutex so push workers can take RLock.
func (b *Bisync) Mu() *sync.RWMutex {
	return b.mu
}

// Run starts the bisync ticker loop. It blocks until ctx is canceled.
func (b *Bisync) Run(ctx context.Context) error {
	interval := b.cfg.Interval
	if interval <= 0 {
		interval = time.Hour
	}

	tickCh, stopTicker := b.clock.NewTicker(interval)
	defer stopTicker()

	b.log.Info("bisync started",
		"interval", interval.String(),
		"local_root", b.cfg.LocalRoot,
		"dry_run", b.cfg.DryRun,
	)

	for {
		select {
		case <-ctx.Done():
			return ErrBisyncCanceled
		case <-tickCh:
			b.execute(ctx)
		case <-b.forceCh:
			b.log.Info("bisync force triggered")
			b.execute(ctx)
		}
	}
}

// ForceRun triggers an immediate bisync execution. Returns
// ErrBisyncRunning if a run is already in progress.
func (b *Bisync) ForceRun(_ context.Context) error {
	b.runMu.Lock()
	if b.running {
		b.runMu.Unlock()
		return ErrBisyncRunning
	}
	b.runMu.Unlock()

	select {
	case b.forceCh <- struct{}{}:
		return nil
	default:
		return ErrBisyncRunning
	}
}

// execute performs a single bisync pass.
func (b *Bisync) execute(ctx context.Context) {
	b.runMu.Lock()
	if b.running {
		b.runMu.Unlock()
		b.log.Warn("bisync skipped, already running")
		return
	}
	b.running = true
	b.runMu.Unlock()
	defer func() {
		b.runMu.Lock()
		b.running = false
		b.runMu.Unlock()
	}()

	start := b.clock.Now()

	b.publishEvent(BisyncEvent{
		Timestamp: start.UTC().Format(time.RFC3339),
		Type:      "bisync.started",
		DryRun:    b.cfg.DryRun,
	})

	// Acquire exclusive lock, blocking push/pull workers.
	b.log.Debug("bisync acquiring global lock")
	b.mu.Lock()
	defer b.mu.Unlock()
	b.log.Debug("bisync global lock acquired")

	// Perform the diff.
	diffs, err := b.diff(ctx)
	if err != nil {
		b.log.Error("bisync diff failed", "error", err)
		b.writeAudit(start, 0, 0, 0, err)
		b.publishEvent(BisyncEvent{
			Timestamp: b.clock.Now().UTC().Format(time.RFC3339),
			Type:      "bisync.completed",
			Error:     err.Error(),
			DryRun:    b.cfg.DryRun,
		})
		return
	}

	pushed, pulled, conflicts := b.syncDiffs(ctx, diffs)
	elapsed := b.clock.Now().Sub(start)

	b.log.Info("bisync completed",
		"duration_ms", elapsed.Milliseconds(),
		"files_pushed", pushed,
		"files_pulled", pulled,
		"conflicts", conflicts,
		"dry_run", b.cfg.DryRun,
	)

	b.writeAudit(start, pushed, pulled, conflicts, nil)
	b.publishEvent(BisyncEvent{
		Timestamp:    b.clock.Now().UTC().Format(time.RFC3339),
		Type:         "bisync.completed",
		DurationMs:   elapsed.Milliseconds(),
		FilesChanged: pushed + pulled,
		Conflicts:    conflicts,
		DryRun:       b.cfg.DryRun,
	})
}

// syncDiffs iterates over diffs and syncs each one.
func (b *Bisync) syncDiffs(
	ctx context.Context, diffs []FileDiff,
) (pushed, pulled, conflicts int) {
	for _, d := range diffs {
		if ctx.Err() != nil {
			break
		}
		switch d.Kind {
		case DiffLocalOnly:
			if err := b.syncLocalToRemote(ctx, d); err != nil {
				b.log.Error("bisync push failed",
					"path", d.Path, "error", err)
				continue
			}
			pushed++
		case DiffRemoteOnly:
			if err := b.syncRemoteToLocal(ctx, d); err != nil {
				b.log.Error("bisync pull failed",
					"path", d.Path, "error", err)
				continue
			}
			pulled++
		case DiffConflict:
			if err := b.resolveConflict(ctx, d); err != nil {
				b.log.Error("bisync conflict resolution failed",
					"path", d.Path, "error", err)
			}
			conflicts++
		}
	}
	return pushed, pulled, conflicts
}

// ---------------------------------------------------------------------------
// Audit and MQTT helpers
// ---------------------------------------------------------------------------

// writeAudit appends a JSONL line to the audit log.
func (b *Bisync) writeAudit(
	start time.Time,
	pushed, pulled, conflicts int,
	syncErr error,
) {
	if b.audit == nil {
		return
	}

	elapsed := b.clock.Now().Sub(start)
	entry := AuditEntry{
		Timestamp:    start.UTC().Format(time.RFC3339),
		Op:           "bisync",
		Duration:     fmt.Sprintf("%d", elapsed.Milliseconds()),
		FilesChanged: pushed + pulled,
		FilesPushed:  pushed,
		FilesPulled:  pulled,
		Conflicts:    conflicts,
		DryRun:       b.cfg.DryRun,
	}
	if syncErr != nil {
		entry.Error = syncErr.Error()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		b.log.Error("failed to marshal audit entry", "error", err)
		return
	}

	line := string(data) + "\n"
	if _, err := b.audit.Write([]byte(line)); err != nil {
		b.log.Error("failed to write audit entry", "error", err)
	}
}

// publishEvent publishes a bisync MQTT event. No-op if MQTT is nil.
func (b *Bisync) publishEvent(event BisyncEvent) {
	if b.mqtt == nil {
		return
	}

	parts := strings.Split(event.Type, ".")
	topic := b.mqtt.Topic(append([]string{"events"}, parts...)...)

	if err := b.mqtt.PublishJSON(topic, event); err != nil {
		b.log.Error("failed to publish bisync event",
			"type", event.Type,
			"error", err,
		)
	}
}
