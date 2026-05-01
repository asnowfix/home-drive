// Package syncer implements the push worker pool that consumes watcher
// events, dispatches them to the remote filesystem via retries with
// exponential backoff, records sync state in the journal, and emits
// MQTT events and audit log entries.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Config holds the push syncer configuration.
type Config struct {
	Workers   int         // Number of concurrent push workers (default 2).
	Retry     RetryConfig // Exponential backoff configuration.
	DryRun    bool        // If true, log intended actions without executing.
	LocalRoot string      // Root of the local sync directory.
}

// DefaultConfig returns a Config with PLAN.md defaults.
func DefaultConfig() Config {
	return Config{
		Workers: 2,
		Retry:   DefaultRetryConfig(),
	}
}

// Syncer is the push worker pool that processes watcher events.
type Syncer struct {
	cfg       Config
	remote    RemoteFS
	store     Store
	auditLog  AuditLog
	publisher Publisher
	logger    *slog.Logger

	// bisyncMu coordinates push workers (RLock) with bisync (Lock).
	bisyncMu *sync.RWMutex

	// sleepFn is injectable for testing; defaults to contextSleep.
	sleepFn func(context.Context, time.Duration)

	// nowFn is injectable for testing; defaults to time.Now.
	nowFn func() time.Time
}

// Option configures a Syncer.
type Option func(*Syncer)

// WithSleepFunc overrides the sleep function used by retry logic.
// Intended for tests to avoid real delays.
func WithSleepFunc(fn func(context.Context, time.Duration)) Option {
	return func(s *Syncer) { s.sleepFn = fn }
}

// WithNowFunc overrides the clock function. Intended for tests.
func WithNowFunc(fn func() time.Time) Option {
	return func(s *Syncer) { s.nowFn = fn }
}

// WithBisyncMutex sets the shared RWMutex for push/bisync coordination.
func WithBisyncMutex(mu *sync.RWMutex) Option {
	return func(s *Syncer) { s.bisyncMu = mu }
}

// New creates a push Syncer with the given dependencies.
func New(
	cfg Config,
	remote RemoteFS,
	store Store,
	auditLog AuditLog,
	publisher Publisher,
	logger *slog.Logger,
	opts ...Option,
) *Syncer {
	if cfg.Workers < 1 {
		cfg.Workers = 2
	}
	s := &Syncer{
		cfg:       cfg,
		remote:    remote,
		store:     store,
		auditLog:  auditLog,
		publisher: publisher,
		logger:    logger,
		bisyncMu:  &sync.RWMutex{},
		sleepFn:   contextSleep,
		nowFn:     time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run starts the worker pool, consuming events and dirRenames from the
// provided channels. It blocks until ctx is cancelled. All workers are
// guaranteed to have stopped when Run returns.
func (s *Syncer) Run(
	ctx context.Context,
	events <-chan Event,
	dirRenames <-chan DirRename,
) {
	var wg sync.WaitGroup

	// Merge both channels into a single work channel.
	work := make(chan any, s.cfg.Workers*4)

	// Fan-in goroutine for events.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				select {
				case work <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Fan-in goroutine for dir renames.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case dr, ok := <-dirRenames:
				if !ok {
					return
				}
				select {
				case work <- dr:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Start workers.
	var workerWg sync.WaitGroup
	for i := 0; i < s.cfg.Workers; i++ {
		workerWg.Add(1)
		go func(id int) {
			defer workerWg.Done()
			s.worker(ctx, id, work)
		}(i)
	}

	// Wait for context cancellation and fan-in goroutines to stop,
	// then close the work channel so workers drain and exit.
	wg.Wait()
	close(work)
	workerWg.Wait()
}

// worker processes items from the work channel until it is closed.
func (s *Syncer) worker(ctx context.Context, id int, work <-chan any) {
	s.logger.Info("push worker started", "worker_id", id)
	defer s.logger.Info("push worker stopped", "worker_id", id)

	for item := range work {
		if ctx.Err() != nil {
			return
		}
		switch v := item.(type) {
		case Event:
			s.handleEvent(ctx, v)
		case DirRename:
			s.handleDirRename(ctx, v)
		}
	}
}

// handleEvent processes a single file event with retry and coordination.
func (s *Syncer) handleEvent(ctx context.Context, ev Event) {
	start := s.nowFn()
	logger := s.logger.With("path", ev.Path, "op", ev.Op.String())

	if s.cfg.DryRun {
		s.logDryRun(logger, ev)
		return
	}

	// Acquire read lock for push/bisync coordination.
	s.bisyncMu.RLock()
	defer s.bisyncMu.RUnlock()

	err := retryFunc(ctx, s.cfg.Retry, logger, ev.Op.String(),
		s.sleepFn, func(ctx context.Context) error {
			return s.execEvent(ctx, ev)
		},
	)

	elapsed := s.nowFn().Sub(start)
	s.recordResult(ctx, ev.Path, ev.Op.String(), err, elapsed)
}

// execEvent dispatches a single event to the remote filesystem.
func (s *Syncer) execEvent(ctx context.Context, ev Event) error {
	switch ev.Op {
	case OpCreate, OpWrite:
		_, err := s.remote.CopyFile(ctx, ev.Path, ev.Path)
		if err != nil {
			return fmt.Errorf("copy file %q: %w", ev.Path, err)
		}
		return nil
	case OpRemove:
		if err := s.remote.DeleteFile(ctx, ev.Path); err != nil {
			return fmt.Errorf("delete file %q: %w", ev.Path, err)
		}
		return nil
	default:
		return fmt.Errorf("unhandled operation: %s", ev.Op)
	}
}

// handleDirRename processes a directory rename with a single MoveFile
// call and a bulk store prefix rewrite.
func (s *Syncer) handleDirRename(ctx context.Context, dr DirRename) {
	start := s.nowFn()
	logger := s.logger.With("op", "dir_rename", "from", dr.From, "to", dr.To)

	if s.cfg.DryRun {
		s.logDryRunDirRename(logger, dr)
		return
	}

	// Acquire read lock for push/bisync coordination.
	s.bisyncMu.RLock()
	defer s.bisyncMu.RUnlock()

	err := retryFunc(ctx, s.cfg.Retry, logger, "dir_rename",
		s.sleepFn, func(ctx context.Context) error {
			return s.remote.MoveFile(ctx, dr.From, dr.To)
		},
	)

	elapsed := s.nowFn().Sub(start)

	if err == nil {
		count, rewriteErr := s.store.RewritePrefix(dr.From, dr.To)
		if rewriteErr != nil {
			logger.Error("store prefix rewrite failed",
				"error", rewriteErr.Error(),
				"duration_ms", elapsed.Milliseconds(),
			)
		} else {
			logger.Info("directory rename synced",
				"files_count", count,
				"duration_ms", elapsed.Milliseconds(),
			)
		}
		s.appendAudit(AuditEntry{
			Timestamp:  s.nowFn(),
			Op:         "dir_rename",
			From:       dr.From,
			To:         dr.To,
			FilesCount: count,
			DurationMs: elapsed.Milliseconds(),
		})
		s.publishEvent("push.success", map[string]any{
			"ts":          s.nowFn(),
			"type":        "dir_rename",
			"from":        dr.From,
			"to":          dr.To,
			"files_count": count,
			"duration_ms": elapsed.Milliseconds(),
		})
		return
	}

	logger.Error("directory rename failed",
		"error", err.Error(),
		"duration_ms", elapsed.Milliseconds(),
	)
	s.appendAudit(AuditEntry{
		Timestamp:  s.nowFn(),
		Op:         "dir_rename",
		From:       dr.From,
		To:         dr.To,
		Error:      err.Error(),
		DurationMs: elapsed.Milliseconds(),
	})
	s.publishEvent("push.failure", map[string]any{
		"ts":          s.nowFn(),
		"type":        "dir_rename",
		"from":        dr.From,
		"to":          dr.To,
		"error":       err.Error(),
		"duration_ms": elapsed.Milliseconds(),
	})
}

// recordResult records the outcome of a push operation in the store,
// audit log, and MQTT.
func (s *Syncer) recordResult(
	_ context.Context,
	path, op string,
	err error,
	elapsed time.Duration,
) {
	if err != nil {
		s.logger.Error("push failed",
			"path", path,
			"op", op,
			"error", err.Error(),
			"duration_ms", elapsed.Milliseconds(),
		)
		s.appendAudit(AuditEntry{
			Timestamp:  s.nowFn(),
			Op:         op,
			Path:       path,
			Error:      err.Error(),
			DurationMs: elapsed.Milliseconds(),
		})
		s.publishEvent("push.failure", map[string]any{
			"ts":          s.nowFn(),
			"type":        op,
			"path":        path,
			"error":       err.Error(),
			"duration_ms": elapsed.Milliseconds(),
		})
		return
	}

	s.logger.Info("push succeeded",
		"path", path,
		"op", op,
		"duration_ms", elapsed.Milliseconds(),
	)

	// Record sync in journal (ignore store errors, they are logged).
	record := SyncRecord{
		Path:         path,
		LastSyncedAt: s.nowFn(),
		LastOrigin:   "local",
	}
	if putErr := s.store.Put(record); putErr != nil {
		s.logger.Error("store put failed",
			"path", path,
			"error", putErr.Error(),
		)
	}

	s.appendAudit(AuditEntry{
		Timestamp:  s.nowFn(),
		Op:         op,
		Path:       path,
		DurationMs: elapsed.Milliseconds(),
	})
	s.publishEvent("push.success", map[string]any{
		"ts":          s.nowFn(),
		"type":        op,
		"path":        path,
		"duration_ms": elapsed.Milliseconds(),
	})
}

// logDryRun logs what would happen for a file event in dry-run mode.
func (s *Syncer) logDryRun(logger *slog.Logger, ev Event) {
	logger.Info("dry-run: would execute push",
		"path", ev.Path,
		"op", ev.Op.String(),
	)
	s.appendAudit(AuditEntry{
		Timestamp: s.nowFn(),
		Op:        ev.Op.String(),
		Path:      ev.Path,
		DryRun:    true,
	})
}

// logDryRunDirRename logs what would happen for a dir rename in dry-run mode.
func (s *Syncer) logDryRunDirRename(logger *slog.Logger, dr DirRename) {
	logger.Info("dry-run: would execute dir rename",
		"from", dr.From,
		"to", dr.To,
	)
	s.appendAudit(AuditEntry{
		Timestamp: s.nowFn(),
		Op:        "dir_rename",
		From:      dr.From,
		To:        dr.To,
		DryRun:    true,
	})
}

// appendAudit writes to the audit log, logging any write errors.
func (s *Syncer) appendAudit(entry AuditEntry) {
	if s.auditLog == nil {
		return
	}
	if err := s.auditLog.Append(entry); err != nil {
		s.logger.Error("audit log append failed", "error", err.Error())
	}
}

// publishEvent publishes an MQTT event, logging any publish errors.
func (s *Syncer) publishEvent(eventType string, payload map[string]any) {
	if s.publisher == nil {
		return
	}
	topic := s.publisher.Topic("events", eventType)
	if err := s.publisher.PublishJSON(topic, payload); err != nil {
		s.logger.Error("mqtt publish failed",
			"topic", topic,
			"error", err.Error(),
		)
	}
}

// contextSleep sleeps for the given duration, returning early if the
// context is cancelled.
func contextSleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
