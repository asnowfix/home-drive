package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// PullerConfig configures the pull loop.
type PullerConfig struct {
	// Interval between polling cycles (default 30s).
	Interval time.Duration

	// LocalRoot is the absolute path to the local sync directory.
	LocalRoot string

	// ConflictPolicy determines how conflicts are resolved.
	ConflictPolicy ConflictPolicy

	// DryRun when true logs intended actions without writing.
	DryRun bool
}

// Puller polls the Drive Changes API and downloads remote modifications.
type Puller struct {
	cfg    PullerConfig
	remote RemoteFS
	store  Store
	audit  AuditLogger
	pub    Publisher
	log    *slog.Logger
	clock  func() time.Time
}

// NewPuller creates a Puller. Pass a nil Publisher to disable MQTT events.
func NewPuller(
	cfg PullerConfig,
	remote RemoteFS,
	store Store,
	audit AuditLogger,
	pub Publisher,
	log *slog.Logger,
	clockFn func() time.Time,
) *Puller {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.ConflictPolicy == "" {
		cfg.ConflictPolicy = PolicyNewerWins
	}
	if clockFn == nil {
		clockFn = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Puller{
		cfg:    cfg,
		remote: remote,
		store:  store,
		audit:  audit,
		pub:    pub,
		log:    log,
		clock:  clockFn,
	}
}

// Run starts the pull polling loop. It blocks until ctx is cancelled.
// The first poll happens immediately, then every cfg.Interval.
func (p *Puller) Run(ctx context.Context) error {
	p.log.Info("puller starting",
		"interval", p.cfg.Interval.String(),
		"dry_run", p.cfg.DryRun,
		"local_root", p.cfg.LocalRoot,
	)

	// Run one cycle immediately at startup.
	p.poll(ctx)

	ticker := time.NewTicker(p.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.log.Info("puller stopping", "reason", ctx.Err())
			return ctx.Err()
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// PollOnce executes a single pull cycle. Exported for testing.
func (p *Puller) PollOnce(ctx context.Context) error {
	return p.poll(ctx)
}

// poll executes one pull cycle: fetch changes, process each one.
func (p *Puller) poll(ctx context.Context) error {
	token, err := p.ensurePageToken(ctx)
	if err != nil {
		p.log.Error("failed to get page token",
			"op", "pull",
			"error", err,
		)
		return fmt.Errorf("getting page token: %w", err)
	}

	changes, err := p.fetchChanges(ctx, token)
	if err != nil {
		return err
	}

	if len(changes.Items) == 0 {
		p.log.Debug("no remote changes",
			"op", "pull",
			"page_token", token,
		)
		// Still persist the next token even if no changes.
		return p.persistToken(ctx, changes.NextPageToken)
	}

	p.log.Info("processing remote changes",
		"op", "pull",
		"count", len(changes.Items),
		"origin", "remote",
	)

	var lastErr error
	for i := range changes.Items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := p.processChange(ctx, changes.Items[i]); err != nil {
			lastErr = err
			// Continue processing remaining changes; individual failures
			// are logged and emit pull.failure events.
		}
	}

	// Persist the new token only after processing all changes.
	if err := p.persistToken(ctx, changes.NextPageToken); err != nil {
		return err
	}

	return lastErr
}

// ensurePageToken retrieves the persisted page token from the store,
// or obtains a fresh start token if none exists.
func (p *Puller) ensurePageToken(ctx context.Context) (string, error) {
	token, err := p.store.GetPageToken(ctx)
	if err != nil {
		return "", fmt.Errorf("reading page token from store: %w", err)
	}
	if token != "" {
		return token, nil
	}

	// No persisted token: obtain a fresh start token.
	p.log.Info("no persisted page token, obtaining start token",
		"op", "pull",
	)
	startToken, err := p.remote.GetStartPageToken(ctx)
	if err != nil {
		return "", fmt.Errorf("obtaining start page token: %w", err)
	}
	if err := p.store.SetPageToken(ctx, startToken); err != nil {
		return "", fmt.Errorf("persisting start page token: %w", err)
	}
	return startToken, nil
}

// fetchChanges calls ListChanges and handles 410 GONE by resetting.
func (p *Puller) fetchChanges(ctx context.Context, token string) (Changes, error) {
	changes, err := p.remote.ListChanges(ctx, token)
	if err == nil {
		return changes, nil
	}

	if !errors.Is(err, ErrGone) {
		p.log.Error("ListChanges failed",
			"op", "pull",
			"error", err,
			"page_token", token,
		)
		p.emitPullFailure("", err)
		return Changes{}, fmt.Errorf("listing changes: %w", err)
	}

	// 410 GONE: token is stale, reset.
	p.log.Warn("page token expired (410 GONE), resetting",
		"op", "pull",
		"stale_token", token,
	)
	if p.pub != nil {
		_ = p.pub.PublishJSON(p.pub.Topic("events", "pull.failure"), map[string]any{
			"ts":    p.clock().UTC().Format(time.RFC3339),
			"type":  "pull.failure",
			"error": "page token expired (410 GONE), resetting",
		})
	}

	newToken, err := p.remote.GetStartPageToken(ctx)
	if err != nil {
		return Changes{}, fmt.Errorf("obtaining new start page token after 410: %w", err)
	}
	if err := p.store.SetPageToken(ctx, newToken); err != nil {
		return Changes{}, fmt.Errorf("persisting reset page token: %w", err)
	}

	// Retry with the fresh token.
	changes, err = p.remote.ListChanges(ctx, newToken)
	if err != nil {
		return Changes{}, fmt.Errorf("listing changes after token reset: %w", err)
	}
	return changes, nil
}

// persistToken saves the next page token to the store.
func (p *Puller) persistToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := p.store.SetPageToken(ctx, token); err != nil {
		return fmt.Errorf("persisting page token: %w", err)
	}
	return nil
}

// emitPullSuccess publishes a pull.success MQTT event.
func (p *Puller) emitPullSuccess(path string, bytes int64) {
	if p.pub == nil {
		return
	}
	_ = p.pub.PublishJSON(p.pub.Topic("events", "pull.success"), map[string]any{
		"ts":    p.clock().UTC().Format(time.RFC3339),
		"type":  "pull.success",
		"path":  path,
		"bytes": bytes,
	})
}

// emitPullFailure publishes a pull.failure MQTT event.
func (p *Puller) emitPullFailure(path string, err error) {
	if p.pub == nil {
		return
	}
	payload := map[string]any{
		"ts":    p.clock().UTC().Format(time.RFC3339),
		"type":  "pull.failure",
		"error": err.Error(),
	}
	if path != "" {
		payload["path"] = path
	}
	_ = p.pub.PublishJSON(p.pub.Topic("events", "pull.failure"), payload)
}

// logAudit writes an entry to the audit log, ignoring errors (best-effort).
func (p *Puller) logAudit(entry AuditEntry) {
	if p.audit == nil {
		return
	}
	if err := p.audit.Log(entry); err != nil {
		p.log.Error("failed to write audit log",
			"op", entry.Op,
			"path", entry.Path,
			"error", err,
		)
	}
}
